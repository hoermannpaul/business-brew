package managed

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"business-brew/internal/model"
)

func TestDefaultProfileUsesOriginalSudoLoginOnlyWhenRoot(t *testing.T) {
	env := func(name string) string {
		if name == "SUDO_USER" {
			return "alice"
		}
		return ""
	}
	current := func() (string, error) { return "bob", nil }
	for _, test := range []struct {
		uid   int
		login string
	}{{0, "alice"}, {1000, "bob"}} {
		got, err := defaultProfilePath("linux", env, test.uid, current)
		if err != nil || got != filepath.Join("/etc/business-brew/profiles", test.login+".json") {
			t.Fatalf("%q %v", got, err)
		}
	}
	if _, err := defaultProfilePath("linux", func(string) string { return "../../bad" }, 0, current); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err := defaultProfilePath("linux", func(name string) string {
		if name == "BUSINESS_BREW_PROFILE" {
			return "relative"
		}
		return ""
	}, 0, current); err == nil {
		t.Fatal("relative override accepted")
	}
}

func TestProjectLongestCanonicalMappingAndOutsideContext(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	other := t.TempDir()
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	p := model.Profile{Projects: []string{"outer", "inner"}, Repositories: []model.RepositoryContext{{Root: root, Project: "outer"}, {Root: nested, Project: "inner"}}}
	got, err := Project(p, nested)
	if err != nil || got != "inner" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = Project(p, other)
	if err != nil || got != "" {
		t.Fatalf("outside mapping: %q %v", got, err)
	}
	p.Repositories[1].Project = "forged"
	if _, err := Project(p, nested); err == nil {
		t.Fatal("unauthorized mapping accepted")
	}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(root, alias); err != nil {
			t.Fatal(err)
		}
		p.Repositories = []model.RepositoryContext{{Root: alias, Project: "outer"}}
		got, err := Project(p, nested)
		if err != nil || got != "outer" {
			t.Fatalf("symlink mapping: %q %v", got, err)
		}
	}
}

func TestCredentialBoundedAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("secret-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := model.Profile{CredentialFile: path}
	got, err := Credential(p, func(string) string { return "" })
	if err != nil || got != "secret-token" {
		t.Fatalf("credential failed: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := Credential(p, os.Getenv); err == nil {
			t.Fatal("public secret accepted")
		}
		if err := os.Chmod(path, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, make([]byte, 4097), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Credential(p, os.Getenv); err == nil {
		t.Fatal("oversized secret accepted")
	}
	p.CredentialEnv = "TOKEN"
	if _, err := Credential(p, os.Getenv); err == nil {
		t.Fatal("ambiguous secret accepted")
	}
	p.CredentialFile = ""
	if _, err := Credential(p, func(string) string { return "first\nsecond" }); err == nil {
		t.Fatal("multiline secret accepted")
	}
}

func TestUnclonedRepositoryDoesNotBlockOutsideProjectContext(t *testing.T) {
	root := t.TempDir()
	profile := model.Profile{
		Projects:     []string{"future"},
		Repositories: []model.RepositoryContext{{Root: filepath.Join(root, "not-cloned"), Project: "future"}},
	}
	project, err := Project(profile, root)
	if err != nil || project != "" {
		t.Fatalf("outside organization/developer context blocked by uncloned repository: project=%q err=%v", project, err)
	}
}

func TestCredentialRejectsNonRegularReferenceBeforeReading(t *testing.T) {
	profile := model.Profile{CredentialFile: t.TempDir()}
	if _, err := Credential(profile, os.Getenv); err == nil {
		t.Fatal("directory credential reference accepted")
	}
}
