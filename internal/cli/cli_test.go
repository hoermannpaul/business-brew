package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"business-brew/internal/installers"
	"business-brew/internal/model"
)

type fakeInstaller struct {
	checks, installs int
	state            installers.State
}

func (f *fakeInstaller) Check(context.Context, model.Target) (installers.State, error) {
	f.checks++
	return f.state, nil
}
func (f *fakeInstaller) Install(context.Context, model.Target) error { f.installs++; return nil }

type transport func(*http.Request) (*http.Response, error)

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

type harness struct {
	app                App
	args               []string
	out, err           bytes.Buffer
	catalogs, requests int
	revoke             bool
	mismatch           bool
	lastRequest        model.RequestInput
	catalog            model.Catalog
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{}
	dir := t.TempDir()
	profile := filepath.Join(dir, "profile.json")
	manifest := filepath.Join(dir, "business-brew.json")
	if err := os.WriteFile(profile, []byte(`{"schema_version":1,"service_url":"https://company.example","organization":"acme","developer":"dev","projects":["app"],"credential_env":"BREW_TOKEN","installers":{"darwin":["brew"]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte(`{"schema_version":1,"project":"app","tools":[{"tool":"go","version":"1.26.0"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	h.args = []string{"--profile", profile, "--file", manifest}
	h.catalog = model.Catalog{SchemaVersion: 1, Revision: 1, Targets: []model.Target{{ID: "go-mac", Tool: "go", Version: "1.26.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "go", Source: "homebrew/core", Grants: []model.Grant{{Organization: "acme"}}}}}
	h.app = App{Out: &h.out, Err: &h.err, OS: "darwin", Arch: "arm64", Getenv: func(string) string { return "secret" }, Installer: &fakeInstaller{state: installers.State{Available: true}}}
	h.app.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatal("credential missing")
		}
		var output any
		switch r.URL.Path {
		case "/api/v1/identity":
			developer := "dev"
			if h.mismatch {
				developer = "other"
			}
			output = model.Identity{Organization: "acme", Developer: developer, Projects: []string{"app"}}
		case "/api/v1/catalog":
			h.catalogs++
			c := h.catalog
			if h.revoke && h.catalogs >= 3 {
				c.Targets = append([]model.Target(nil), c.Targets...)
				c.Targets[0].Grants = nil
			}
			output = c
		case "/api/v1/requests":
			if r.Method == http.MethodPost {
				h.requests++
				if err := json.NewDecoder(r.Body).Decode(&h.lastRequest); err != nil {
					t.Fatal(err)
				}
				output = model.SoftwareRequest{ID: "request-1", Status: "pending"}
			} else {
				output = []model.SoftwareRequest{}
			}
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
		encoded, err := json.Marshal(output)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded))}, nil
	})}
	return h
}
func (h *harness) run(command string) int {
	return h.app.Run(context.Background(), append([]string{command}, h.args...))
}

func TestPlanNoMutation(t *testing.T) {
	h := newHarness(t)
	if code := h.run("plan"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err.String())
	}
	f := h.app.Installer.(*fakeInstaller)
	if f.installs != 0 || h.requests != 0 || !strings.Contains(h.out.String(), "ready to install") {
		t.Fatal("plan mutated state or omitted readiness")
	}
}
func TestDeniedSetupCreatesRequestWithoutNativeCalls(t *testing.T) {
	h := newHarness(t)
	h.catalog.Targets[0].Grants = nil
	if h.run("setup") != 1 {
		t.Fatal("blocked setup succeeded")
	}
	f := h.app.Installer.(*fakeInstaller)
	if f.checks != 0 || f.installs != 0 || h.requests != 1 || h.lastRequest.RequestType != "approval" {
		t.Fatal("denied requirement not gated/requested")
	}
}
func TestApprovalRevocationBetweenCheckAndInstall(t *testing.T) {
	h := newHarness(t)
	h.revoke = true
	if h.run("setup") != 1 || h.app.Installer.(*fakeInstaller).installs != 0 || h.catalogs != 3 {
		t.Fatal("revoked approval permitted installation")
	}
}
func TestUnavailableVersionRequestsProvisioning(t *testing.T) {
	h := newHarness(t)
	h.app.Installer.(*fakeInstaller).state.Available = false
	if h.run("setup") != 1 || h.requests != 1 || h.lastRequest.RequestType != "provisioning" || h.app.Installer.(*fakeInstaller).installs != 0 {
		t.Fatal("unavailable version installed or incorrectly requested")
	}
}

func TestMissingCatalogPackageRequestsProvisioning(t *testing.T) {
	h := newHarness(t)
	h.catalog.Targets = nil
	if h.run("setup") != 1 || h.requests != 1 || h.lastRequest.RequestType != "provisioning" {
		t.Fatal("missing mapping did not request provisioning")
	}
}
func TestProfileCannotOverrideCredentialIdentity(t *testing.T) {
	h := newHarness(t)
	h.mismatch = true
	if h.run("setup") != 1 || h.catalogs != 0 || h.requests != 0 || h.app.Installer.(*fakeInstaller).checks != 0 {
		t.Fatal("mismatched identity accepted")
	}
}
func TestStatusDoesNotRunInstaller(t *testing.T) {
	h := newHarness(t)
	if h.run("status") != 0 || h.catalogs != 0 || h.app.Installer.(*fakeInstaller).checks != 0 {
		t.Fatal("status mutated or queried installer")
	}
}
