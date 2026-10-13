package wrapper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"business-brew/internal/model"
)

type transport func(*http.Request) (*http.Response, error)

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

type nativeQuery func(string, ...string) ([]byte, error)

func (q nativeQuery) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	return q(name, args...)
}

type nativeExit int

func (e nativeExit) Error() string { return "native process exited" }
func (e nativeExit) ExitCode() int { return int(e) }

type fakeNative struct {
	calls          int
	installed      bool
	change         bool
	absent, broken bool
}

func (f *fakeNative) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls++
	if f.absent {
		return []byte("No available formula with the name"), errors.New("native lookup absent")
	}
	if f.broken {
		return nil, errors.New("metadata transport failure")
	}
	pkg := strings.TrimPrefix(args[len(args)-1], "homebrew/core/")
	version := "22.14.0"
	if pkg == "git" {
		version = "2.49.0"
	}
	if f.change && f.calls > 1 {
		version = "22.15.0"
	}
	installed := []map[string]string{}
	if f.installed {
		installed = append(installed, map[string]string{"version": version})
	}
	out, _ := json.Marshal(map[string]any{"formulae": []any{map[string]any{"name": pkg, "full_name": pkg, "tap": "homebrew/core", "versions": map[string]string{"stable": version}, "installed": installed}}})
	return out, nil
}

type fixture struct {
	app                                App
	profile                            model.Profile
	path                               string
	out, err                           bytes.Buffer
	runner                             fakeNative
	catalog                            model.Catalog
	apiCalls, catalogCalls, executions int
	requests                           []model.RequestInput
	arguments                          []string
	revoke, mismatch                   bool
	nativeExit                         int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	root := t.TempDir()
	f.path = filepath.Join(root, "profile.json")
	f.profile = model.Profile{SchemaVersion: 1, ServiceURL: "https://catalog.example", Organization: "acme", Developer: "alice", Projects: []string{"billing"}, CredentialEnv: "TOKEN", NativeBinaries: map[string]string{"brew": filepath.Join(root, "real-brew")}}
	f.saveProfile(t)
	f.catalog = model.Catalog{SchemaVersion: 1, Revision: 1, Targets: []model.Target{{ID: "node", Tool: "node", Version: "22.14.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22", Source: "homebrew/core", Grants: []model.Grant{{Organization: "acme"}}}}}
	f.app = App{ProfilePath: f.path, OS: "darwin", Arch: "arm64", CWD: t.TempDir(), Out: &f.out, Err: &f.err, Runner: &f.runner, Getenv: func(key string) string {
		if key == "TOKEN" {
			return "credential"
		}
		return ""
	}}
	f.app.HTTP = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		f.apiCalls++
		if r.Header.Get("Authorization") != "Bearer credential" {
			t.Fatal("missing credential")
		}
		var output any
		switch r.URL.Path {
		case "/api/v1/identity":
			dev := "alice"
			if f.mismatch {
				dev = "other"
			}
			output = model.Identity{Organization: "acme", Developer: dev, Projects: f.profile.Projects}
		case "/api/v1/catalog":
			f.catalogCalls++
			cat := f.catalog
			if f.revoke && f.catalogCalls > 1 {
				cat.Targets = append([]model.Target(nil), cat.Targets...)
				for i := range cat.Targets {
					cat.Targets[i].Grants = nil
				}
			}
			output = cat
		case "/api/v1/requests":
			if r.Method != http.MethodPost {
				t.Fatal("unexpected method")
			}
			var input model.RequestInput
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			f.requests = append(f.requests, input)
			output = model.SoftwareRequest{ID: "request-1", Status: "pending", URL: "https://requests.example/request-1"}
		default:
			t.Fatalf("unexpected service path %s", r.URL.Path)
		}
		encoded, _ := json.Marshal(output)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(encoded))}, nil
	})}
	f.app.Execute = func(_ context.Context, name string, args []string) (int, error) {
		f.executions++
		f.arguments = slices.Clone(args)
		f.runner.installed = true
		fmtOut := []byte("native progress\n")
		_, _ = f.out.Write(fmtOut)
		return f.nativeExit, nil
	}
	return f
}
func (f *fixture) saveProfile(t *testing.T) {
	t.Helper()
	data, _ := json.Marshal(f.profile)
	if err := os.WriteFile(f.path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) run(args ...string) int { return f.app.Run(context.Background(), "brew", args) }

func TestReadOnlyPassthroughPreservesArgumentsAndNativeExit(t *testing.T) {
	f := newFixture(t)
	f.nativeExit = 9
	f.app.Getenv = func(string) string { return "" }
	if code := f.run("list", "--versions"); code != 9 || f.apiCalls != 0 || f.runner.calls != 0 || !slices.Equal(f.arguments, []string{"list", "--versions"}) {
		t.Fatalf("read passthrough changed: %d %s", code, f.err.String())
	}
}
func TestApprovedNativeCommandNeedsNoManifestAndStreamsOutput(t *testing.T) {
	f := newFixture(t)
	if code := f.run("install", "node@22"); code != 0 {
		t.Fatalf("exit %d: %s", code, f.err.String())
	}
	if f.executions != 1 || f.catalogCalls != 2 || len(f.requests) != 0 || f.out.String() != "native progress\n" || !slices.Equal(f.arguments, []string{"install", "homebrew/core/node@22"}) {
		t.Fatal("native workflow/output changed")
	}
}
func TestEntireMultiPackageCommandStopsWhenOneTargetIsBlocked(t *testing.T) {
	f := newFixture(t)
	f.catalog.Targets = append(f.catalog.Targets, model.Target{ID: "git", Tool: "git", Version: "2.49.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "git", Source: "homebrew/core", Grants: []model.Grant{{Organization: "acme", Project: "billing"}}})
	if code := f.run("install", "node@22", "git"); code != ExitBlocked || f.executions != 0 || len(f.requests) != 1 {
		t.Fatalf("partial native mutation: %d %s", code, f.err.String())
	}
	if f.requests[0].Package != "git" || f.requests[0].Project != "" || !strings.Contains(f.err.String(), "https://requests.example/request-1") {
		t.Fatal("request lost exact package/scope/link")
	}
}
func TestManagedRepositoryEnablesProjectGrant(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	f.profile.Repositories = []model.RepositoryContext{{Root: root, Project: "billing"}}
	f.saveProfile(t)
	f.app.CWD = root
	f.catalog.Targets[0].Grants = []model.Grant{{Organization: "acme", Project: "billing", Developer: "alice"}}
	if code := f.run("install", "node@22"); code != 0 || f.executions != 1 {
		t.Fatalf("managed project blocked: %d %s", code, f.err.String())
	}
}
func TestApprovalRevocationImmediatelyBeforeNativeInvocation(t *testing.T) {
	f := newFixture(t)
	f.revoke = true
	if code := f.run("install", "node@22"); code != ExitBlocked || f.executions != 0 {
		t.Fatalf("revocation ignored: %d", code)
	}
}
func TestChangedNativeCandidateStopsWithoutFallback(t *testing.T) {
	f := newFixture(t)
	f.runner.change = true
	if code := f.run("install", "node@22"); code != ExitChanged || f.executions != 0 {
		t.Fatalf("candidate drift ignored: %d", code)
	}
}
func TestNativeFailureExitIsPreserved(t *testing.T) {
	f := newFixture(t)
	f.nativeExit = 42
	if code := f.run("install", "node@22"); code != 42 || f.executions != 1 || f.runner.calls != 2 {
		t.Fatalf("native failure exit/postchecks changed: %d", code)
	}
}
func TestMissingPackageCreatesUnresolvedProvisioningRequest(t *testing.T) {
	f := newFixture(t)
	f.runner.absent = true
	if code := f.run("install", "missing"); code != ExitBlocked || f.executions != 0 || len(f.requests) != 1 {
		t.Fatal("missing package did not request provisioning")
	}
	input := f.requests[0]
	if input.RequestType != "provisioning" || input.Version != "" || input.Package != "missing" || input.Source != "" {
		t.Fatalf("unknown metadata invented: %+v", input)
	}
}
func TestMetadataFailureDoesNotCreateFalsePackagingRequest(t *testing.T) {
	f := newFixture(t)
	f.runner.broken = true
	if code := f.run("install", "node@22"); code != ExitBlocked || len(f.requests) != 0 || f.executions != 0 {
		t.Fatal("metadata outage treated as package absence")
	}
}
func TestProfileCannotImpersonateAnotherDeveloper(t *testing.T) {
	f := newFixture(t)
	f.mismatch = true
	if code := f.run("install", "node@22"); code != ExitConfiguration || f.runner.calls != 0 || f.executions != 0 {
		t.Fatal("profile identity spoof accepted")
	}
}
func TestBulkAndOverrideFlagsNeverReachNativeExecutable(t *testing.T) {
	for _, args := range [][]string{{"upgrade"}, {"install", "--HEAD", "node"}, {"bundle"}} {
		f := newFixture(t)
		if code := f.run(args...); code != ExitUnsupported || f.executions != 0 || f.apiCalls != 0 {
			t.Fatalf("unsafe native command allowed: %v", args)
		}
	}
}
func TestSuccessfulNativeCommandMustVerifyInstalledVersion(t *testing.T) {
	f := newFixture(t)
	f.app.Verify = func(context.Context, []model.Target) error {
		return errors.New("native installed version mismatches approval")
	}
	if code := f.run("install", "node@22"); code != 1 || f.executions != 1 {
		t.Fatal("unverified install reported success")
	}
}
func TestOrganizationOnlyCredentialNeedsNoProjectAssignment(t *testing.T) {
	f := newFixture(t)
	f.profile.Projects = nil
	f.saveProfile(t)
	if code := f.run("install", "node@22"); code != 0 {
		t.Fatalf("org grant required phantom project: %d %s", code, f.err.String())
	}
}

func TestUnavailableExplicitVersionSurvivesProvisioningRequest(t *testing.T) {
	f := newFixture(t)
	f.app.OS = "windows"
	f.app.Runner = nativeQuery(func(_ string, args ...string) ([]byte, error) {
		if args[0] == "search" {
			return []byte("NodeJS OpenJS.NodeJS.LTS 22.14.0 winget\n"), nil
		}
		return nil, nativeExit(-1978335209)
	})
	code := f.app.Run(context.Background(), "winget", []string{"install", "--id", "OpenJS.NodeJS.LTS", "--version", "22.14.0", "--source", "winget"})
	if code != ExitBlocked || f.executions != 0 || len(f.requests) != 1 {
		t.Fatalf("unavailable native version was not requested: %d %s", code, f.err.String())
	}
	input := f.requests[0]
	if input.RequestType != "provisioning" || input.Version != "22.14.0" || input.PackageVersion != "22.14.0" || input.Source != "winget" {
		t.Fatalf("explicit request lost version/source: %+v", input)
	}
}
