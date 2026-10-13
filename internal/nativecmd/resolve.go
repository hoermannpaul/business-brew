package nativecmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"business-brew/internal/installers"
	"business-brew/internal/model"
)

// Resolve returns the native resolver's exact candidate, never an approximate
// semver choice. The runner must bind query commands to trusted executable paths.
func Resolve(ctx context.Context, inv Invocation, runner installers.Runner, platform, arch string) ([]model.Target, error) {
	if inv.ReadOnly {
		return nil, nil
	}
	if runner == nil {
		return nil, fmt.Errorf("native metadata runner is required")
	}
	var targets []model.Target
	for _, p := range inv.Packages {
		if p.Arch != "" && (inv.Installer != "dnf" || p.Arch != "noarch") && nativeArch(inv.Installer, p.Arch) != nativeArch(inv.Installer, arch) {
			return nil, unsupported("cross-architecture installation is not supported")
		}
		t := model.Target{Tool: p.Name, OS: platform, Arch: arch, Installer: inv.Installer, Package: p.Name, PackageType: p.PackageType}
		var err error
		switch inv.Installer {
		case "brew":
			if platform != "darwin" && platform != "linux" {
				return nil, unsupported("brew platform %s", platform)
			}
			t, err = resolveBrew(ctx, runner, p, t)
		case "winget":
			if platform != "windows" {
				return nil, unsupported("winget requires Windows")
			}
			t, err = resolveWinget(ctx, runner, p, t)
		case "apt":
			if platform != "linux" {
				return nil, unsupported("apt requires Linux")
			}
			t, err = resolveApt(ctx, runner, p, t)
		case "dnf":
			if platform != "linux" {
				return nil, unsupported("dnf requires Linux")
			}
			t, err = resolveDNF(ctx, runner, p, t)
		default:
			return nil, unsupported("installer %s", inv.Installer)
		}
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", p.Name, err)
		}
		if err = installers.Validate(t); err != nil {
			return nil, fmt.Errorf("invalid native metadata: %w", err)
		}
		targets = append(targets, t)
	}
	return targets, nil
}

func resolveBrew(ctx context.Context, r installers.Runner, p Package, t model.Target) (model.Target, error) {
	args := []string{"info", "--json=v2"}
	if p.PackageType != "" {
		args = append(args, "--"+p.PackageType)
	}
	args = append(args, p.Name)
	out, err := r.Run(ctx, "brew", args...)
	if err != nil {
		if strings.Contains(string(out), "No available formula with the name") || strings.Contains(string(out), "No available formula or cask with the name") || strings.Contains(string(out), "No Cask with this name exists") {
			return t, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return t, err
	}
	var info struct {
		Formulae []struct {
			Name     string `json:"name"`
			FullName string `json:"full_name"`
			Tap      string `json:"tap"`
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
			Revision int `json:"revision"`
		} `json:"formulae"`
		Casks []struct {
			Token     string `json:"token"`
			FullToken string `json:"full_token"`
			Tap       string `json:"tap"`
			Version   string `json:"version"`
		} `json:"casks"`
	}
	if err = json.Unmarshal(out, &info); err != nil {
		return t, fmt.Errorf("invalid Homebrew metadata: %w", err)
	}
	if len(info.Formulae)+len(info.Casks) != 1 {
		return t, fmt.Errorf("expected one unambiguous Homebrew formula/cask")
	}
	if len(info.Formulae) == 1 {
		f := info.Formulae[0]
		if p.PackageType == "cask" {
			return t, fmt.Errorf("requested cask resolved as formula")
		}
		t.PackageType = "formula"
		t.Source = f.Tap
		t.Package = brewShort(f.FullName, f.Name, f.Tap)
		t.Version = f.Versions.Stable
		t.PackageVersion = t.Version
		if f.Revision > 0 {
			t.PackageVersion = fmt.Sprintf("%s_%d", t.Version, f.Revision)
		}
	} else {
		c := info.Casks[0]
		if p.PackageType == "formula" || t.OS != "darwin" {
			return t, fmt.Errorf("cask is not valid for requested type/platform")
		}
		t.PackageType = "cask"
		t.Source = c.Tap
		t.Package = brewShort(c.FullToken, c.Token, c.Tap)
		t.Version = c.Version
		t.PackageVersion = c.Version
	}
	if t.Source == "" || t.Package == "" || t.Version == "" || t.Version == "latest" || (p.Source != "" && p.Source != t.Source) {
		return t, fmt.Errorf("Homebrew metadata has no exact version or mismatching source")
	}
	// The native result identifies aliases by their canonical name. A fully
	// qualified invocation additionally binds the source; aliases are allowed.
	t.Tool = t.Package
	return t, nil
}
func brewShort(full, short, tap string) string {
	if full == "" {
		return short
	}
	return strings.TrimPrefix(full, tap+"/")
}

var foundID = regexp.MustCompile(`(?m)^Found [^\r\n]*\[([^\]\r\n]+)\]\s*$`)

func resolveWinget(ctx context.Context, r installers.Runner, p Package, t model.Target) (model.Target, error) {
	// Search identifies the exact native source. Show output often omits Source,
	// so never substitute a default source or infer it from an installer URL.
	// Query all configured sources: WinGet omits the Source table column when
	// search is filtered by --source. Select the requested source explicitly
	// below, otherwise require a unique source.
	args := []string{"search", "--id", p.Name, "--exact", "--disable-interactivity"}
	out, err := r.Run(ctx, "winget", args...)
	if err != nil {
		return t, wingetError(err, p.Version)
	}
	sources := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		for i, s := range f {
			if strings.EqualFold(s, p.Name) && i+2 < len(f) {
				source := f[len(f)-1]
				if identifier.MatchString(source) {
					sources[source] = true
				}
			}
		}
	}
	if p.Source != "" {
		matched := ""
		for source := range sources {
			if strings.EqualFold(source, p.Source) {
				matched = source
			}
		}
		if matched == "" {
			return t, fmt.Errorf("winget requested source did not return the exact ID")
		}
		sources = map[string]bool{matched: true}
	}
	if len(sources) != 1 {
		return t, fmt.Errorf("winget did not return one exact package source")
	}
	for source := range sources {
		t.Source = source
	}
	if p.Source != "" && !strings.EqualFold(p.Source, t.Source) {
		return t, fmt.Errorf("winget source differs from request")
	}
	args = []string{"show", "--id", p.Name, "--exact", "--source", t.Source, "--architecture", nativeArch("winget", t.Arch), "--disable-interactivity"}
	if p.Version != "" {
		args = append(args, "--version", p.Version)
	}
	out, err = r.Run(ctx, "winget", args...)
	if err != nil {
		return t, wingetError(err, p.Version)
	}
	id, version, source := "", "", ""
	if matches := foundID.FindAllSubmatch(out, -1); len(matches) == 1 {
		id = string(matches[0][1])
	} else if len(matches) > 1 {
		return t, fmt.Errorf("winget returned multiple identities")
	}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "Id", "ID":
			if id != "" && !strings.EqualFold(id, value) {
				return t, fmt.Errorf("conflicting winget identity")
			}
			id = value
		case "Version":
			if version != "" && version != value {
				return t, fmt.Errorf("conflicting winget version")
			}
			version = value
		case "Source":
			if source != "" && !strings.EqualFold(source, value) {
				return t, fmt.Errorf("conflicting winget source")
			}
			source = value
		}
	}
	if !strings.EqualFold(id, p.Name) || version == "" || (source != "" && !strings.EqualFold(source, t.Source)) {
		return t, fmt.Errorf("winget exact identity/version/source was not verified (English metadata required)")
	}
	if p.Version != "" && version != p.Version {
		return t, ErrUnavailable
	}
	t.Package = id
	t.Tool = id
	t.Version = version
	t.PackageVersion = version
	return t, nil
}
func wingetError(err error, version string) error {
	var code interface{ ExitCode() int }
	if errors.As(err, &code) {
		switch uint32(code.ExitCode()) {
		case 0x8A150010, 0x8A150014, 0x8A150017:
			return fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	}
	return err
}

func resolveApt(ctx context.Context, r installers.Runner, p Package, t model.Target) (model.Target, error) {
	name := p.Name + ":" + nativeArch("apt", t.Arch)
	version := p.Version
	if version == "" {
		out, err := r.Run(ctx, "apt-cache", "policy", name)
		if err != nil {
			return t, err
		}
		for _, line := range strings.Split(string(out), "\n") {
			if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "Candidate" {
				if version != "" {
					return t, fmt.Errorf("ambiguous apt candidate")
				}
				version = strings.TrimSpace(v)
			}
		}
		if version == "" {
			return t, fmt.Errorf("apt candidate metadata missing")
		}
		if version == "(none)" {
			return t, ErrUnavailable
		}
	}
	out, err := r.Run(ctx, "apt-cache", "madison", name)
	if err != nil {
		return t, err
	}
	sources := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(line, "|")
		if len(f) != 3 || strings.TrimSpace(f[1]) != version {
			continue
		}
		id := strings.TrimSpace(f[0])
		if id != p.Name && id != name {
			continue
		}
		src := strings.Fields(f[2])
		if len(src) > 0 {
			sources[strings.TrimRight(src[0], "/")] = true
		}
	}
	if len(sources) == 0 {
		if p.Version != "" {
			return t, ErrUnavailable
		}
		return t, fmt.Errorf("apt candidate is not available from a repository")
	}
	if len(sources) != 1 {
		return t, fmt.Errorf("apt version is available from multiple repository sources")
	}
	for source := range sources {
		t.Source = source
	}
	t.Version = version
	t.PackageVersion = version
	return t, nil
}

func resolveDNF(ctx context.Context, r installers.Runner, p Package, t model.Target) (model.Target, error) {
	query := p.Name
	if p.Version != "" {
		query = p.Name + "-" + p.Version + "." + p.Arch
	}
	args := []string{"--quiet", "repoquery", "--available", "--latest-limit=1", "--archlist=" + nativeArch("dnf", t.Arch) + ",noarch", "--qf", "%{name}|%{epoch}:%{version}-%{release}|%{arch}|%{reponame}", query}
	out, err := r.Run(ctx, "dnf", args...)
	if err != nil {
		return t, err
	}
	candidates := map[string]model.Target{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) != 4 {
			continue
		}
		if f[0] != p.Name {
			return t, fmt.Errorf("dnf query resolved another package identity")
		}
		if f[2] != nativeArch("dnf", t.Arch) && f[2] != "noarch" {
			continue
		}
		v := strings.TrimPrefix(strings.TrimPrefix(f[1], "0:"), "(none):")
		if p.Version != "" && v != strings.TrimPrefix(p.Version, "0:") {
			continue
		}
		c := t
		c.Source = f[3]
		c.Version = v
		c.PackageVersion = v
		// Catalog Arch remains the host architecture, including noarch RPMs.
		candidates[strings.Join(f, "|")] = c
	}
	if len(candidates) == 0 {
		if strings.TrimSpace(string(out)) == "" || p.Version != "" {
			return t, ErrUnavailable
		}
		return t, fmt.Errorf("dnf has no exact repository candidate")
	}
	if len(candidates) != 1 {
		return t, fmt.Errorf("dnf candidate is ambiguous across architectures/repositories")
	}
	for _, candidate := range candidates {
		return candidate, nil
	}
	panic("unreachable")
}

func nativeArch(installer, arch string) string {
	switch installer {
	case "winget":
		switch arch {
		case "amd64":
			return "x64"
		case "386":
			return "x86"
		}
	case "dnf":
		switch arch {
		case "amd64":
			return "x86_64"
		case "arm64":
			return "aarch64"
		case "386":
			return "i686"
		}
	}
	return arch
}
