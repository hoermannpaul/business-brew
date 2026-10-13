package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"business-brew/internal/model"
)

func fixture(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManagedProfileReferencesAndMappings(t *testing.T) {
	root := t.TempDir()
	base := model.Profile{SchemaVersion: 1, ServiceURL: "https://company.example", Organization: "acme", Developer: "dev", Projects: []string{"app"}, CredentialEnv: "BREW_TOKEN", Installers: map[string][]string{"linux": {"apt"}}}
	load := func(p model.Profile) error {
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		_, err = LoadProfile(fixture(t, string(data)))
		return err
	}
	if err := load(base); err != nil {
		t.Fatalf("legacy profile rejected: %v", err)
	}
	valid := base
	valid.CredentialEnv = ""
	valid.CredentialFile = filepath.Join(root, "credential")
	valid.NativeBinaries = map[string]string{"apt": filepath.Join(root, "apt"), "apt-get": filepath.Join(root, "apt-get"), "apt-cache": filepath.Join(root, "apt-cache")}
	valid.Repositories = []model.RepositoryContext{{Root: root, Project: "app"}}
	if err := load(valid); err != nil {
		t.Fatalf("managed profile rejected: %v", err)
	}
	for _, change := range []func(*model.Profile){
		func(p *model.Profile) { p.CredentialFile = "relative" },
		func(p *model.Profile) { p.CredentialEnv = "TOKEN" },
		func(p *model.Profile) { p.CredentialFile = "" },
		func(p *model.Profile) { p.NativeBinaries = map[string]string{"sudo": filepath.Join(root, "sudo")} },
		func(p *model.Profile) { p.NativeBinaries = map[string]string{"apt": "relative"} },
		func(p *model.Profile) { p.Repositories = []model.RepositoryContext{{Root: root, Project: "other"}} },
		func(p *model.Profile) { p.Repositories = []model.RepositoryContext{{Root: "relative", Project: "app"}} },
		func(p *model.Profile) {
			p.Repositories = []model.RepositoryContext{{Root: root, Project: "app"}, {Root: filepath.Join(root, "."), Project: "app"}}
		},
	} {
		p := valid
		change(&p)
		if err := load(p); err == nil {
			t.Fatal("invalid managed profile accepted")
		}
	}
}

func TestManifestRejectsProjectIdentityOverride(t *testing.T) {
	_, err := LoadManifest(fixture(t, `{"schema_version":1,"project":"app","organization":"other","tools":[{"tool":"go","version":"1.26.0"}]}`))
	if err == nil {
		t.Fatal("unknown organization override accepted")
	}
}

func TestProfileRequiresSecureServiceAndPlatformInstaller(t *testing.T) {
	for _, content := range []string{
		`{"schema_version":1,"service_url":"http://company.example","organization":"acme","developer":"dev","projects":["app"],"credential_env":"BREW_TOKEN","installers":{"linux":["apt"]}}`,
		`{"schema_version":1,"service_url":"https://company.example","organization":"acme","developer":"dev","projects":["app"],"credential_env":"BREW_TOKEN","installers":{"windows":["apt"]}}`,
	} {
		if _, err := LoadProfile(fixture(t, content)); err == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
}

func TestManifestRejectsDuplicateToolsAndTrailingDocument(t *testing.T) {
	for _, content := range []string{
		`{"schema_version":1,"project":"app","tools":[{"tool":"go","version":"1.26.0"},{"tool":"go","version":"1.25.0"}]}`,
		`{"schema_version":1,"project":"app","tools":[{"tool":"go","version":"1.26.0"}]} {}`,
	} {
		if _, err := LoadManifest(fixture(t, content)); err == nil {
			t.Fatal("invalid manifest accepted")
		}
	}
}
