package catalog

import (
	"business-brew/internal/model"
	"testing"
)

func validTarget() model.Target {
	return model.Target{ID: "node-linux", Tool: "node", Version: "22.1.0", OS: "linux", Arch: "amd64", Installer: "brew", Package: "node@22", Source: "homebrew/core", Grants: []model.Grant{{Organization: "acme"}}}
}

func TestOutsideProjectRequestScope(t *testing.T) {
	for _, scope := range []string{"", "developer", "organization", "project", "project_developer", "arbitrary"} {
		draft := model.Draft{RequestScope: "project_developer", OutsideProjectRequestScope: scope}
		err := ValidateDraft(draft, "acme")
		valid := scope == "" || scope == "developer" || scope == "organization"
		if (err == nil) != valid {
			t.Fatalf("scope %q: %v", scope, err)
		}
	}
}

func TestNativeBindingsCannotBePublishedUnderMultipleCanonicalTools(t *testing.T) {
	one := validTarget()
	one.PackageVersion = "22.1.0_1"
	two := one
	two.ID, two.Tool, two.Version = "other-node", "node-runtime", "22.1.0-build1"
	two.PackageType = "formula"
	draft := model.Draft{RequestScope: "developer", Targets: []model.Target{one, two}}
	if err := ValidateDraft(draft, "acme"); err == nil {
		t.Fatal("duplicate native binding under different canonical names accepted")
	}
	two.Source = "company/tools"
	draft.Targets[1] = two
	if err := ValidateDraft(draft, "acme"); err != nil {
		t.Fatalf("different native source must remain distinct: %v", err)
	}
}

func TestDuplicateNativeDNFBindingsNormalizeZeroEpoch(t *testing.T) {
	one := validTarget()
	one.Installer, one.Package, one.Source, one.PackageVersion = "dnf", "nodejs", "fedora", "0:22.1.0-1.fc43"
	two := one
	two.ID, two.Tool, two.Version, two.PackageVersion = "other-node", "node-runtime", "22.1.0-build1", "22.1.0-1.fc43"
	draft := model.Draft{RequestScope: "developer", Targets: []model.Target{one, two}}
	if err := ValidateDraft(draft, "acme"); err == nil {
		t.Fatal("equivalent DNF zero epochs published twice")
	}
}
func TestValidateDraftRejectsUnsafeOrAmbiguousCatalog(t *testing.T) {
	target := validTarget()
	draft := model.Draft{Targets: []model.Target{target}, RequestScope: "project"}
	if err := ValidateDraft(draft, "acme"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		change func(*model.Target)
	}{
		{"empty grant", func(t *model.Target) { t.Grants = []model.Grant{{}} }},
		{"other tenant", func(t *model.Target) { t.Grants = []model.Grant{{Organization: "other"}} }},
		{"version range", func(t *model.Target) { t.Version = "^22" }},
		{"latest", func(t *model.Target) { t.Version = "latest" }},
		{"command as package", func(t *model.Target) { t.Package = "node; touch /tmp/x" }},
		{"option as package", func(t *model.Target) { t.Package = "--evil" }},
		{"unsupported platform", func(t *model.Target) { t.OS = "windows" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			copy := validTarget()
			c.change(&copy)
			draft.Targets = []model.Target{copy}
			if err := ValidateDraft(draft, "acme"); err == nil {
				t.Fatal("invalid draft accepted")
			}
		})
	}
}
func TestZeroGrantsRevokesAndNativeVersionMappingIsAllowed(t *testing.T) {
	target := validTarget()
	target.Grants = nil
	target.PackageVersion = "22.1.0-1ubuntu2"
	if err := ValidateTarget(target, "acme"); err != nil {
		t.Fatal(err)
	}
	draft := model.Draft{Targets: []model.Target{target, target}, RequestScope: "project"}
	if err := ValidateDraft(draft, "acme"); err == nil {
		t.Fatal("duplicate mappings accepted")
	}
}

func TestInstallerSpecificNativeVersions(t *testing.T) {
	cask := validTarget()
	cask.OS = "darwin"
	cask.PackageType = "cask"
	cask.Source = "homebrew/cask"
	cask.PackageVersion = "4.62.0,225307"
	if err := ValidateTarget(cask, "acme"); err != nil {
		t.Fatal(err)
	}
	apt := validTarget()
	apt.Installer = "apt"
	apt.Package = "nodejs"
	apt.Source = "https://packages.example"
	apt.PackageVersion = "22.1.0-1~22.04"
	if err := ValidateTarget(apt, "acme"); err != nil {
		t.Fatal(err)
	}
}

func TestProviderConfigurationIsValidatedBeforePublication(t *testing.T) {
	draft := model.Draft{RequestScope: "project", DefaultConnector: "tickets", Connectors: []model.Connector{{ID: "tickets", Kind: "github", BaseURL: "https://api.github.com", TokenEnv: "GITHUB_TOKEN"}}}
	if err := ValidateDraft(draft, "acme"); err == nil {
		t.Fatal("missing provider settings accepted")
	}
	draft.Connectors[0].Settings = map[string]string{"repository": "acme/tools"}
	draft.Connectors[0].StatusMapping = map[string]string{"closed": "installable"}
	if err := ValidateDraft(draft, "acme"); err == nil {
		t.Fatal("invalid decision mapping accepted")
	}
}
