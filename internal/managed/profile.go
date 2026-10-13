// Package managed resolves company-owned profiles, identities and native executables.
package managed

import (
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"business-brew/internal/model"
)

var loginName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]{0,199}$`)

func DefaultProfilePath(platform string, getenv func(string) string) (string, error) {
	return defaultProfilePath(platform, getenv, os.Geteuid(), func() (string, error) {
		u, err := user.Current()
		if err != nil {
			return "", err
		}
		name := u.Username
		if platform == "windows" {
			// Windows account names may include the domain; profiles use the login name.
			if i := strings.LastIndex(name, `\`); i >= 0 {
				name = name[i+1:]
			}
		}
		return name, nil
	})
}

func defaultProfilePath(platform string, getenv func(string) string, euid int, current func() (string, error)) (string, error) {
	if override := getenv("BUSINESS_BREW_PROFILE"); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("BUSINESS_BREW_PROFILE must be absolute")
		}
		return filepath.Clean(override), nil
	}
	var name string
	var err error
	if platform != "windows" && euid == 0 && getenv("SUDO_USER") != "" {
		name = getenv("SUDO_USER")
	} else {
		name, err = current()
		if err != nil {
			return "", fmt.Errorf("cannot determine managed profile login")
		}
	}
	if !loginName.MatchString(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid managed profile login")
	}
	var directory string
	switch platform {
	case "darwin":
		directory = "/Library/Application Support/BusinessBrew/profiles"
	case "linux":
		directory = "/etc/business-brew/profiles"
	case "windows":
		base := getenv("PROGRAMDATA")
		if !filepath.IsAbs(base) {
			return "", fmt.Errorf("PROGRAMDATA must be absolute")
		}
		directory = filepath.Join(base, "BusinessBrew", "profiles")
	default:
		return "", fmt.Errorf("unsupported profile platform")
	}
	return filepath.Join(directory, name+".json"), nil
}

// Project uses only managed repository mappings; the longest enclosing root wins.
func Project(profile model.Profile, cwd string) (string, error) {
	if !filepath.IsAbs(cwd) {
		return "", fmt.Errorf("working directory must be absolute")
	}
	cwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return "", fmt.Errorf("cannot resolve working directory")
	}
	project, longest := "", -1
	roots := map[string]bool{}
	for _, repository := range profile.Repositories {
		if !filepath.IsAbs(repository.Root) || !slices.Contains(profile.Projects, repository.Project) {
			return "", fmt.Errorf("invalid managed repository mapping")
		}
		root, err := filepath.EvalSymlinks(repository.Root)
		if os.IsNotExist(err) {
			// A repository that has not been cloned cannot contain an existing cwd.
			continue
		}
		if err != nil {
			return "", fmt.Errorf("cannot resolve managed repository root")
		}
		key := root
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if roots[key] {
			return "", fmt.Errorf("duplicate managed repository root")
		}
		roots[key] = true
		relative, err := filepath.Rel(root, cwd)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			continue
		}
		if len(root) > longest {
			project, longest = repository.Project, len(root)
		}
	}
	return project, nil
}

func Credential(profile model.Profile, getenv func(string) string) (string, error) {
	if (profile.CredentialEnv == "") == (profile.CredentialFile == "") {
		return "", fmt.Errorf("exactly one managed credential reference is required")
	}
	var value string
	if profile.CredentialEnv != "" {
		value = getenv(profile.CredentialEnv)
	} else {
		if !filepath.IsAbs(profile.CredentialFile) {
			return "", fmt.Errorf("credential file must be absolute")
		}
		info, err := os.Stat(profile.CredentialFile)
		if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return "", fmt.Errorf("managed credential file must be regular and private")
		}
		file, err := os.Open(profile.CredentialFile)
		if err != nil {
			return "", fmt.Errorf("cannot open managed credential file")
		}
		defer file.Close()
		info, err = file.Stat()
		if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return "", fmt.Errorf("managed credential file must be regular and private")
		}
		data, err := io.ReadAll(io.LimitReader(file, 4097))
		if err != nil || len(data) > 4096 {
			return "", fmt.Errorf("cannot read bounded managed credential")
		}
		value = strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("managed credential is missing or invalid")
	}
	return value, nil
}
