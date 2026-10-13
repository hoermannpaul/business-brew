package catalog

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"business-brew/internal/installers"
	"business-brew/internal/model"
	"business-brew/internal/requests"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+@-]*$`)

func ValidateDraft(draft model.Draft, organization string) error {
	if organization == "" {
		return fmt.Errorf("organization is required")
	}
	if !slices.Contains([]string{"organization", "project", "developer", "project_developer"}, draft.RequestScope) {
		return fmt.Errorf("invalid request_scope")
	}
	if draft.OutsideProjectRequestScope != "" && !slices.Contains([]string{"organization", "developer"}, draft.OutsideProjectRequestScope) {
		return fmt.Errorf("outside_project_request_scope must be organization or developer")
	}
	ids := map[string]bool{}
	mappings := map[string]bool{}
	nativeMappings := map[string]bool{}
	for _, target := range draft.Targets {
		if err := ValidateTarget(target, organization); err != nil {
			return fmt.Errorf("target %q: %w", target.ID, err)
		}
		if ids[target.ID] {
			return fmt.Errorf("duplicate target id %q", target.ID)
		}
		ids[target.ID] = true
		key := strings.Join([]string{target.Tool, target.Version, target.OS, target.Arch, target.Installer}, "\x00")
		if mappings[key] {
			return fmt.Errorf("duplicate mapping for %s %s", target.Tool, target.Version)
		}
		mappings[key] = true
		nativeKey := strings.Join([]string{target.OS, target.Arch, target.Installer, target.Package, target.NativeVersionIdentity(), target.NativePackageType(), target.Source}, "\x00")
		if nativeMappings[nativeKey] {
			return fmt.Errorf("duplicate native mapping for %s %s from %s", target.Package, target.NativeVersion(), target.Source)
		}
		nativeMappings[nativeKey] = true
	}
	connectors := map[string]bool{}
	for _, connector := range draft.Connectors {
		if !identifier.MatchString(connector.ID) || connectors[connector.ID] {
			return fmt.Errorf("invalid or duplicate connector id %q", connector.ID)
		}
		connectors[connector.ID] = true
		if err := requests.ValidateConnector(connector); err != nil {
			return fmt.Errorf("connector %q: %w", connector.ID, err)
		}
	}
	if draft.DefaultConnector != "" && !connectors[draft.DefaultConnector] {
		return fmt.Errorf("default connector does not exist")
	}
	return nil
}

func ValidateTarget(target model.Target, organization string) error {
	for name, value := range map[string]string{"id": target.ID, "tool": target.Tool, "version": target.Version, "package": target.Package} {
		if !identifier.MatchString(value) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	if slices.Contains([]string{"latest", "stable", "head"}, strings.ToLower(target.Version)) || strings.ContainsAny(target.Version, "*@><=~^") {
		return fmt.Errorf("an exact version is required")
	}
	if !slices.Contains([]string{"amd64", "arm64"}, target.Arch) {
		return fmt.Errorf("unsupported architecture")
	}
	if target.Source == "" || strings.ContainsAny(target.Source, "\r\n\x00") {
		return fmt.Errorf("package source is required")
	}
	valid := (target.OS == "darwin" && target.Installer == "brew") || (target.OS == "windows" && target.Installer == "winget") || (target.OS == "linux" && slices.Contains([]string{"brew", "apt", "dnf"}, target.Installer))
	if !valid {
		return fmt.Errorf("unsupported OS/installer combination")
	}
	if target.PackageType != "" && !(target.Installer == "brew" && slices.Contains([]string{"formula", "cask"}, target.PackageType)) {
		return fmt.Errorf("package_type is supported only for Homebrew formula/cask")
	}
	if err := installers.Validate(target); err != nil {
		return err
	}
	for _, grant := range target.Grants {
		if grant.Organization != organization {
			return fmt.Errorf("every grant must explicitly name this organization")
		}
		if grant.Project != "" && !identifier.MatchString(grant.Project) {
			return fmt.Errorf("invalid project grant")
		}
		if grant.Developer != "" && !identifier.MatchString(grant.Developer) {
			return fmt.Errorf("invalid developer grant")
		}
	}
	return nil
}
