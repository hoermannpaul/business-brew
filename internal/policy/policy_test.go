package policy

import (
	"business-brew/internal/model"
	"testing"
)

func TestGrantSelectors(t *testing.T) {
	identity := model.Identity{Organization: "acme", Developer: "alice", Projects: []string{"billing"}}
	cases := []struct {
		name    string
		grants  []model.Grant
		project string
		allowed bool
	}{
		{"deny empty", nil, "billing", false},
		{"org", []model.Grant{{Organization: "acme"}}, "billing", true},
		{"project", []model.Grant{{Organization: "acme", Project: "billing"}}, "billing", true},
		{"developer", []model.Grant{{Organization: "acme", Developer: "alice"}}, "billing", true},
		{"combined", []model.Grant{{Organization: "acme", Project: "billing", Developer: "alice"}}, "billing", true},
		{"wrong dev", []model.Grant{{Organization: "acme", Project: "billing", Developer: "bob"}}, "billing", false},
		{"other org", []model.Grant{{Organization: "other"}}, "billing", false},
		{"unpermitted project", []model.Grant{{Organization: "acme"}}, "secret", false},
		{"OR", []model.Grant{{Organization: "acme", Developer: "bob"}, {Organization: "acme", Developer: "alice"}}, "billing", true},
		{"outside project organization", []model.Grant{{Organization: "acme"}}, "", true},
		{"outside project developer", []model.Grant{{Organization: "acme", Developer: "alice"}}, "", true},
		{"outside project cannot borrow project grant", []model.Grant{{Organization: "acme", Project: "billing"}}, "", false},
		{"outside project cannot borrow combined grant", []model.Grant{{Organization: "acme", Project: "billing", Developer: "alice"}}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Allowed(model.Target{Grants: c.grants}, identity, c.project); got != c.allowed {
				t.Fatalf("got %v want %v", got, c.allowed)
			}
		})
	}
}

func TestProjectlessApprovalRequiresTrustedIdentity(t *testing.T) {
	target := model.Target{Grants: []model.Grant{{Organization: "acme"}}}
	for _, identity := range []model.Identity{{Developer: "alice"}, {Organization: "acme"}} {
		if Allowed(target, identity, "") {
			t.Fatalf("incomplete identity authorized: %+v", identity)
		}
	}
}

func TestNativeRequestKeyBindsCompletePackageTuple(t *testing.T) {
	input := model.RequestInput{Tool: "node", Version: "22.1.0", OS: "linux", Arch: "amd64", Installer: "apt", Package: "nodejs", PackageVersion: "22.1.0-1~22.04", Source: "https://packages.example"}
	key := RequestKey("acme", "developer", input, "alice")
	for _, mutate := range []func(*model.RequestInput){
		func(i *model.RequestInput) { i.Package = "nodejs-other" },
		func(i *model.RequestInput) { i.PackageVersion = "22.1.0-2~22.04" },
		func(i *model.RequestInput) { i.Source = "https://other.example" },
		func(i *model.RequestInput) { i.PackageType = "cask" },
	} {
		other := input
		mutate(&other)
		if key == RequestKey("acme", "developer", other, "alice") {
			t.Fatal("different native binding reused request")
		}
	}
}

func TestNativeRequestKeysIgnoreCanonicalNamesAndNormalizeIdentity(t *testing.T) {
	input := model.RequestInput{RequestType: "approval", Tool: "node@22", Version: "22.1.0_1", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22", PackageVersion: "22.1.0_1", Source: "homebrew/core"}
	canonical := input
	canonical.Tool, canonical.Version, canonical.PackageType = "node", "22.1.0", "formula"
	if RequestKey("acme", "developer", input, "alice") != RequestKey("acme", "developer", canonical, "alice") {
		t.Fatal("canonical display names/formula default duplicated exact native request")
	}
	input.Installer, input.Package, input.Source, input.OS, input.Arch = "dnf", "nodejs", "fedora", "linux", "amd64"
	input.PackageVersion = "0:22.1.0-1.fc43"
	canonical = input
	canonical.PackageVersion = "22.1.0-1.fc43"
	if RequestKey("acme", "developer", input, "alice") != RequestKey("acme", "developer", canonical, "alice") {
		t.Fatal("DNF zero epoch duplicated exact native request")
	}
	canonical.PackageVersion = "1:22.1.0-1.fc43"
	if RequestKey("acme", "developer", input, "alice") == RequestKey("acme", "developer", canonical, "alice") {
		t.Fatal("DNF nonzero epoch must remain distinct")
	}
}

func TestRequestKeysMatchConfiguredScope(t *testing.T) {
	one := model.RequestInput{Tool: "node", Version: "22.1.0", OS: "linux", Arch: "amd64", Project: "one", Reason: "first"}
	two := one
	two.Project = "two"
	two.Reason = "different explanation"
	if RequestKey("acme", "organization", one, "alice") != RequestKey("acme", "organization", two, "bob") {
		t.Fatal("org request should be reusable across developers and projects")
	}
	if RequestKey("acme", "project", one, "alice") == RequestKey("acme", "project", two, "alice") {
		t.Fatal("different projects must not share project requests")
	}
	if RequestKey("acme", "developer", one, "alice") == RequestKey("acme", "developer", one, "bob") {
		t.Fatal("different developers must not share developer requests")
	}
	if RequestKey("acme", "project", one, "alice") == RequestKey("other", "project", one, "alice") {
		t.Fatal("different organizations must not share requests")
	}
	two = one
	two.Version = "22.2.0"
	if RequestKey("acme", "project", one, "alice") == RequestKey("acme", "project", two, "alice") {
		t.Fatal("exact versions must not share requests")
	}
}

func TestRequestVisibility(t *testing.T) {
	i := model.Identity{Organization: "acme", Developer: "alice", Projects: []string{"billing"}}
	r := model.SoftwareRequest{Organization: "acme", Developer: "bob", Project: "billing", Scope: "project"}
	if !RequestVisible(r, i) {
		t.Fatal("project request should be shared")
	}
	r.Scope = "project_developer"
	if RequestVisible(r, i) {
		t.Fatal("combined scope must match developer too")
	}
	r.Scope = "organization"
	r.Organization = "other"
	if RequestVisible(r, i) {
		t.Fatal("other org request must be hidden")
	}
}
