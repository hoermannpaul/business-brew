// Package installers invokes preconfigured native package managers without a shell.
package installers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"business-brew/internal/model"
)

var ErrUnavailable = errors.New("exact package version unavailable from approved source")

type Runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Env = append(os.Environ(), "LC_ALL=C", "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_INSTALL_CLEANUP=1")
	out, err := c.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s command failed: %w (native privileges may be required; this tool does not elevate privileges)", name, err)
	}
	return out, nil
}

type State struct {
	Installed bool
	Available bool
}
type Adapter interface {
	Check(context.Context, model.Target) (State, error)
	Install(context.Context, model.Target) error
}
type Native struct{ Runner Runner }

var token = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~:@-]*$`)
var nativeVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~:@,%-]*$`)
var tap = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_-]*$`)
var uri = regexp.MustCompile(`^https?://[A-Za-z0-9][A-Za-z0-9._:/+~-]*$`)

func Validate(t model.Target) error {
	if !token.MatchString(t.Package) || !nativeVersion.MatchString(t.NativeVersion()) || !token.MatchString(t.Arch) {
		return fmt.Errorf("invalid package identifier, native version or architecture")
	}
	switch t.Installer {
	case "brew":
		if !tap.MatchString(t.Source) {
			return fmt.Errorf("brew source must be an existing tap, e.g. homebrew/core")
		}
		if t.PackageType != "" && t.PackageType != "formula" && t.PackageType != "cask" {
			return fmt.Errorf("brew package_type must be formula or cask")
		}
		if t.PackageType == "cask" && t.OS != "darwin" {
			return fmt.Errorf("casks require macOS")
		}
	case "apt":
		if !uri.MatchString(t.Source) {
			return fmt.Errorf("apt source must be a configured repository HTTP(S) URI")
		}
	case "dnf", "winget":
		if !token.MatchString(t.Source) {
			return fmt.Errorf("source must be a configured repository identifier")
		}
	default:
		return fmt.Errorf("unsupported installer %q", t.Installer)
	}
	return nil
}

func (n Native) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if n.Runner == nil {
		return ExecRunner{}.Run(ctx, name, args...)
	}
	return n.Runner.Run(ctx, name, args...)
}

func (n Native) Check(ctx context.Context, t model.Target) (State, error) {
	if err := Validate(t); err != nil {
		return State{}, err
	}
	switch t.Installer {
	case "brew":
		return n.brew(ctx, t)
	case "winget":
		return n.winget(ctx, t)
	case "apt":
		return n.apt(ctx, t)
	case "dnf":
		return n.dnf(ctx, t)
	}
	return State{}, fmt.Errorf("unsupported installer")
}

// Install verifies exact availability before mutation and exact installation afterwards.
func (n Native) Install(ctx context.Context, t model.Target) error {
	s, err := n.Check(ctx, t)
	if err != nil {
		return err
	}
	if s.Installed {
		return nil
	}
	if !s.Available {
		return ErrUnavailable
	}
	var args []string
	name := t.Installer
	switch name {
	case "brew":
		args = []string{"install", brewKind(t), t.Source + "/" + t.Package}
	case "winget":
		args = []string{"install", "--id", t.Package, "--exact", "--version", t.NativeVersion(), "--source", t.Source, "--architecture", wingetArch(t.Arch), "--disable-interactivity"}
	case "apt":
		name = "apt-get"
		args = []string{"install", "--assume-yes", "--no-install-recommends", t.Package + ":" + t.Arch + "=" + t.NativeVersion()}
	case "dnf":
		args = append(dnfSource(t), "--forcearch="+rpmArch(t.Arch), "install", "--assumeyes", t.Package+"-"+t.NativeVersion())
	}
	if _, err := n.run(ctx, name, args...); err != nil {
		return err
	}
	s, err = n.Check(ctx, t)
	if err != nil {
		return err
	}
	if !s.Installed {
		return fmt.Errorf("installer completed but exact approved version was not verified")
	}
	return nil
}

func (n Native) brew(ctx context.Context, t model.Target) (State, error) {
	out, err := n.run(ctx, "brew", "info", "--json=v2", brewKind(t), t.Source+"/"+t.Package)
	if err != nil {
		return State{}, err
	}
	if t.PackageType == "cask" {
		var info struct {
			Casks []struct {
				FullToken string          `json:"full_token"`
				Tap       string          `json:"tap"`
				Version   string          `json:"version"`
				Installed json.RawMessage `json:"installed"`
			} `json:"casks"`
		}
		if err := json.Unmarshal(out, &info); err != nil {
			return State{}, err
		}
		if len(info.Casks) != 1 {
			return State{}, ErrUnavailable
		}
		c := info.Casks[0]
		if c.Tap != t.Source || (c.FullToken != t.Package && c.FullToken != t.Source+"/"+t.Package) {
			return State{}, fmt.Errorf("cask metadata source/package differs from catalog")
		}
		s := State{Available: c.Version == t.NativeVersion()}
		var installed string
		if json.Unmarshal(c.Installed, &installed) == nil {
			s.Installed = installed == t.NativeVersion()
		}
		var versions []string
		if json.Unmarshal(c.Installed, &versions) == nil {
			for _, v := range versions {
				if v == t.NativeVersion() {
					s.Installed = true
				}
			}
		}
		return s, nil
	}
	var info struct {
		Formulae []struct {
			FullName string `json:"full_name"`
			Tap      string `json:"tap"`
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
			Revision  int `json:"revision"`
			Installed []struct {
				Version string `json:"version"`
			} `json:"installed"`
		} `json:"formulae"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return State{}, fmt.Errorf("invalid brew metadata: %w", err)
	}
	if len(info.Formulae) != 1 {
		return State{}, ErrUnavailable
	}
	f := info.Formulae[0]
	if f.Tap != t.Source || (f.FullName != t.Package && f.FullName != t.Source+"/"+t.Package) {
		return State{}, fmt.Errorf("brew metadata source/package differs from catalog")
	}
	v := f.Versions.Stable
	if f.Revision > 0 {
		v = fmt.Sprintf("%s_%d", v, f.Revision)
	}
	s := State{Available: v == t.NativeVersion()}
	for _, installed := range f.Installed {
		if installed.Version == t.NativeVersion() {
			s.Installed = true
		}
	}
	return s, nil
}

func brewKind(t model.Target) string {
	if t.PackageType == "cask" {
		return "--cask"
	}
	return "--formula"
}

func wingetArch(arch string) string {
	if arch == "amd64" {
		return "x64"
	}
	if arch == "386" {
		return "x86"
	}
	return arch
}

func (n Native) winget(ctx context.Context, t model.Target) (State, error) {
	out, err := n.run(ctx, "winget", "show", "--id", t.Package, "--exact", "--version", t.NativeVersion(), "--source", t.Source, "--architecture", wingetArch(t.Arch), "--disable-interactivity")
	if err != nil {
		if wingetAbsent(err) {
			return State{}, ErrUnavailable
		}
		return State{}, fmt.Errorf("winget exact-version lookup: %w", err)
	}
	if len(out) == 0 {
		return State{}, fmt.Errorf("empty winget package metadata")
	}
	s := State{Available: true}
	out, err = n.run(ctx, "winget", "list", "--id", t.Package, "--exact", "--source", t.Source, "--disable-interactivity")
	if err != nil {
		return s, nil
	} // A nonzero list response also means no installed match.
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if field == t.Package && i+1 < len(fields) && fields[i+1] == t.NativeVersion() {
				s.Installed = true
			}
		}
	}
	return s, nil
}

func wingetAbsent(err error) bool {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) {
		return false
	}
	// WinGet documented HRESULTs: no applicable installer, no package, no manifest.
	switch uint32(exit.ExitCode()) {
	case 0x8A150010, 0x8A150014, 0x8A150017:
		return true
	default:
		return false
	}
}

func (n Native) apt(ctx context.Context, t model.Target) (State, error) {
	out, err := n.run(ctx, "apt-cache", "madison", t.Package+":"+t.Arch)
	if err != nil {
		return State{}, err
	}
	s := State{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 3 || strings.TrimSpace(fields[1]) != t.NativeVersion() {
			continue
		}
		source := strings.Fields(fields[2])
		if len(source) == 0 {
			continue
		}
		if strings.TrimRight(source[0], "/") != strings.TrimRight(t.Source, "/") {
			return State{}, fmt.Errorf("requested apt version also exists in an unapproved repository; source cannot be pinned safely")
		}
		s.Available = true
	}
	out, err = n.run(ctx, "dpkg-query", "--show", "--showformat=${db:Status-Status}|${Version}", t.Package+":"+t.Arch)
	if err == nil {
		s.Installed = strings.TrimSpace(string(out)) == "installed|"+t.NativeVersion()
	}
	return s, nil
}

func rpmArch(arch string) string {
	switch arch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	case "386":
		return "i686"
	}
	return arch
}
func dnfSource(t model.Target) []string {
	return []string{"--disablerepo=*", "--enablerepo=" + t.Source}
}

func (n Native) dnf(ctx context.Context, t model.Target) (State, error) {
	args := append(dnfSource(t), "repoquery", "--available", "--qf", "%{name}|%{epoch}:%{version}-%{release}|%{arch}", t.Package)
	out, err := n.run(ctx, "dnf", args...)
	if err != nil {
		return State{}, err
	}
	s := State{Available: rpmMatches(string(out), t)}
	out, err = n.run(ctx, "rpm", "-q", "--qf", "%{NAME}|%{EPOCHNUM}:%{VERSION}-%{RELEASE}|%{ARCH}\n", t.Package)
	if err == nil {
		s.Installed = rpmMatches(string(out), t)
	}
	return s, nil
}

func rpmMatches(output string, t model.Target) bool {
	for _, line := range strings.Split(output, "\n") {
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) != 3 {
			continue
		}
		version := strings.TrimPrefix(strings.TrimPrefix(f[1], "0:"), "(none):")
		want := t.NativeVersionIdentity()
		if f[0] == t.Package && version == want && (f[2] == rpmArch(t.Arch) || f[2] == "noarch") {
			return true
		}
	}
	return false
}
