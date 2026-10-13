// Package config loads declarative requirements and company-managed profiles.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"business-brew/internal/model"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/+~-]{0,199}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func read(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("%s: expected one JSON document", path)
	}
	return nil
}

func LoadProfile(path string) (model.Profile, error) {
	var p model.Profile
	if err := read(path, &p); err != nil {
		return p, err
	}
	if p.SchemaVersion != 1 {
		return p, fmt.Errorf("profile schema_version must be 1")
	}
	u, err := url.Parse(p.ServiceURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return p, fmt.Errorf("service_url must be a service origin")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return p, fmt.Errorf("service_url requires HTTPS except on loopback")
	}
	if !identifier.MatchString(p.Organization) || !identifier.MatchString(p.Developer) {
		return p, fmt.Errorf("profile requires valid organization and developer")
	}
	if (p.CredentialEnv == "") == (p.CredentialFile == "") || (p.CredentialEnv != "" && !envName.MatchString(p.CredentialEnv)) || (p.CredentialFile != "" && !filepath.IsAbs(p.CredentialFile)) {
		return p, fmt.Errorf("profile requires exactly one valid credential_env or absolute credential_file")
	}
	seen := map[string]bool{}
	for _, project := range p.Projects {
		if !identifier.MatchString(project) || seen[project] {
			return p, fmt.Errorf("invalid or duplicate profile project")
		}
		seen[project] = true
	}
	for platform, names := range p.Installers {
		if !slices.Contains([]string{"darwin", "windows", "linux"}, platform) || len(names) == 0 {
			return p, fmt.Errorf("invalid installer platform %q", platform)
		}
		used := map[string]bool{}
		for _, name := range names {
			valid := (platform == "darwin" && name == "brew") || (platform == "windows" && name == "winget") || (platform == "linux" && slices.Contains([]string{"brew", "apt", "dnf"}, name))
			if !valid || used[name] {
				return p, fmt.Errorf("invalid or duplicate installer %q for %s", name, platform)
			}
			used[name] = true
		}
	}
	for name, path := range p.NativeBinaries {
		if !slices.Contains([]string{"brew", "winget", "apt", "apt-get", "dnf", "apt-cache", "dpkg-query", "rpm"}, name) || !filepath.IsAbs(path) {
			return p, fmt.Errorf("native_binaries requires known installer or query helper names and absolute paths")
		}
	}
	roots := map[string]bool{}
	for _, repository := range p.Repositories {
		root := filepath.Clean(repository.Root)
		if !filepath.IsAbs(root) || roots[root] || !seen[repository.Project] {
			return p, fmt.Errorf("repositories requires unique absolute roots and permitted projects")
		}
		roots[root] = true
	}
	return p, nil
}

func LoadManifest(path string) (model.Manifest, error) {
	var m model.Manifest
	if err := read(path, &m); err != nil {
		return m, err
	}
	if m.SchemaVersion != 1 || !identifier.MatchString(m.Project) || len(m.Tools) == 0 {
		return m, fmt.Errorf("manifest requires schema_version 1, project and tools")
	}
	seen := map[string]bool{}
	for _, r := range m.Tools {
		if !identifier.MatchString(r.Tool) || !identifier.MatchString(r.Version) || strings.ContainsAny(r.Version, "~*^<>= ") || slices.Contains([]string{"latest", "stable", "head"}, strings.ToLower(r.Version)) || seen[r.Tool] || len(r.Reason) > 2000 {
			return m, fmt.Errorf("invalid or duplicate tool requirement %q", r.Tool)
		}
		seen[r.Tool] = true
	}
	return m, nil
}
