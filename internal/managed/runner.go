package managed

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

const ActiveEnvironment = "BUSINESS_BREW_WRAPPER_ACTIVE"

type Runner struct {
	NativeBinaries map[string]string
	// Executable overrides os.Executable for focused tests only.
	Executable string
}

func (r Runner) executable() (string, error) {
	if r.Executable != "" {
		return r.Executable, nil
	}
	return os.Executable()
}

func digest(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return [32]byte{}, err
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func (r Runner) Binary(name string) (string, error) {
	if !slices.Contains([]string{"brew", "winget", "apt", "apt-get", "dnf", "apt-cache", "dpkg-query", "rpm"}, name) {
		return "", fmt.Errorf("unsupported native executable %q", name)
	}
	path := r.NativeBinaries[name]
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("configure an absolute native_binaries path for %s", name)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
		return "", fmt.Errorf("configured native %s is not a regular executable", name)
	}
	self, err := r.executable()
	if err != nil {
		return "", fmt.Errorf("cannot identify wrapper executable")
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return "", fmt.Errorf("cannot identify wrapper executable")
	}
	if os.SameFile(info, selfInfo) {
		return "", fmt.Errorf("native %s points to the wrapper", name)
	}
	// Windows shims are copies; also reject copied wrappers on Unix.
	a, err := digest(path)
	if err != nil {
		return "", fmt.Errorf("cannot validate native %s", name)
	}
	b, err := digest(self)
	if err != nil {
		return "", fmt.Errorf("cannot validate wrapper executable")
	}
	if a == b {
		return "", fmt.Errorf("native %s is a copy of the wrapper", name)
	}
	return path, nil
}

// Environment removes shim directories from the child's PATH and suppresses
// Homebrew's automatic update side effect during metadata queries.
func (r Runner) Environment() []string {
	self, _ := r.executable()
	selfInfo, _ := os.Stat(self)
	selfDigest, digestErr := digest(self)
	var directories []string
	for _, directory := range filepath.SplitList(os.Getenv("PATH")) {
		if directory == "" {
			continue
		}
		shim := false
		for _, name := range []string{"brew", "winget", "apt", "apt-get", "dnf"} {
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			info, err := os.Stat(filepath.Join(directory, name))
			if err == nil && selfInfo != nil && os.SameFile(info, selfInfo) {
				shim = true
				break
			}
			if err == nil && info.Mode().IsRegular() && digestErr == nil {
				candidate, hashErr := digest(filepath.Join(directory, name))
				if hashErr == nil && candidate == selfDigest {
					shim = true
					break
				}
			}
		}
		if !shim {
			directories = append(directories, directory)
		}
	}
	var environment []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if slices.Contains([]string{"PATH", ActiveEnvironment, "HOMEBREW_NO_AUTO_UPDATE"}, strings.ToUpper(name)) {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "PATH="+strings.Join(directories, string(os.PathListSeparator)), ActiveEnvironment+"=1", "HOMEBREW_NO_AUTO_UPDATE=1")
}

func (r Runner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := r.Binary(name)
	if err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, path, args...)
	for _, entry := range r.Environment() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, "LC_ALL") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "LC_ALL=C")
	out, err := command.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("native %s command failed: %w", name, err)
	}
	return out, nil
}
