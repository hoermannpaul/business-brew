package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"business-brew/internal/auth"
	"business-brew/internal/model"
	"business-brew/internal/store"
)

type fakeConnector struct {
	mu                   sync.Mutex
	creates              int
	remote               *model.RemoteRequest
	createErr, errorFind error
}

func (f *fakeConnector) Find(context.Context, model.SoftwareRequest) (*model.RemoteRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remote == nil {
		return nil, f.errorFind
	}
	copy := *f.remote
	return &copy, f.errorFind
}
func (f *fakeConnector) Create(context.Context, model.SoftwareRequest) (model.RemoteRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.createErr != nil {
		return model.RemoteRequest{}, f.createErr
	}
	f.remote = &model.RemoteRequest{ID: "ticket-1", URL: "https://tickets.example/ticket-1", Status: "pending"}
	return *f.remote, nil
}
func (f *fakeConnector) Read(context.Context, string) (model.RemoteRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.remote == nil {
		return model.RemoteRequest{}, errors.New("not found")
	}
	return *f.remote, nil
}

func TestLookupFailureRetriesOnceAfterRecovery(t *testing.T) {
	f := newFixture(t)
	f.connector.errorFind = errors.New("temporary lookup outage")
	w := f.api(t, "POST", "/api/v1/requests", requestBody)
	item := decodedRequest(t, w)
	if item.Status != "lookup_failed" || f.connector.creates != 0 {
		t.Fatalf("pre-submission failure: %+v creates=%d", item, f.connector.creates)
	}
	f.connector.errorFind = nil
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.api(t, "POST", "/api/v1/requests", requestBody) }()
	}
	wg.Wait()
	close(results)
	for w := range results {
		got := decodedRequest(t, w)
		if got.ID != item.ID {
			t.Fatalf("request identity changed on recovery: %+v", got)
		}
	}
	if f.connector.creates != 1 {
		t.Fatalf("concurrent recovery created %d tickets", f.connector.creates)
	}
	stored, err := f.db.Request(item.ID)
	if err != nil || stored.Status != "pending" {
		t.Fatalf("recovery failed: %+v %v", stored, err)
	}
}

func TestCatalogFormEditsStayDraftUntilPublished(t *testing.T) {
	f := newFixture(t)
	draft, _ := f.db.Draft()
	values := url.Values{"csrf": {f.session.CSRF}, "version": {strconv.FormatInt(draft.Version, 10)}, "tool": {"git"}, "target_version": {"2.49.0"}, "os": {"darwin"}, "arch": {"arm64"}, "installer": {"brew"}, "package": {"git"}, "source": {"homebrew/core"}, "grant_project": {"project-a"}, "grant_developer": {"alice"}, "grant_enabled_0": {"on"}}
	w := f.admin(t, "/admin/target", values)
	if w.Code != 303 {
		t.Fatalf("save target %d: %s", w.Code, w.Body.String())
	}
	updated, _ := f.db.Draft()
	if len(updated.Targets) != 2 || updated.Targets[1].Grants[0].Organization != "acme" {
		t.Fatal("form did not bind grant organization")
	}
	cat, _ := f.db.Published()
	if len(cat.Targets) != 1 {
		t.Fatal("draft edit leaked into published catalog")
	}
	r := httptest.NewRequest("GET", "/admin/target?id="+url.QueryEscape(updated.Targets[1].ID), nil)
	r.AddCookie(&http.Cookie{Name: "brew_session", Value: f.cookie})
	w = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Who may install it?") {
		t.Fatal("target editor did not render")
	}
	values.Set("version", strconv.FormatInt(draft.Version, 10))
	w = f.admin(t, "/admin/target", values)
	if w.Code != 409 {
		t.Fatal("stale form edit accepted")
	}
}

func TestLoginThrottlingDoesNotLockOtherAccountsBehindProxy(t *testing.T) {
	f := newFixture(t)
	for range 20 {
		r := httptest.NewRequest("POST", "/admin/login", strings.NewReader("username=admin"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("missing csrf accepted")
		}
	}
	if len(f.s.loginAttempts) != 0 {
		t.Fatal("malformed login attempts consumed account throttle")
	}
	for range 11 {
		f.s.limited("127.0.0.1:8080", "alice")
	}
	if f.s.limited("127.0.0.1:8080", "bob") {
		t.Fatal("one account locked out other admins behind same proxy")
	}
}

func TestProvisioningRequestNeedsAvailabilityAfterApproval(t *testing.T) {
	f := newFixture(t)
	draft, _ := f.db.Draft()
	draft.Targets[0].Grants = []model.Grant{{Organization: "acme"}}
	draft, err := f.db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(requestBody, `"reason":`, `"request_type":"provisioning","reason":`, 1)
	w := f.api(t, "POST", "/api/v1/requests", body)
	item := decodedRequest(t, w)
	if item.Status != "pending" || f.connector.creates != 1 {
		t.Fatalf("approved unavailable tool did not request provisioning: %+v", item)
	}
	f.connector.remote.Status = "approved"
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "approved_awaiting_availability" {
		t.Fatalf("service asserted installer availability: %s", got)
	}
	f.connector.remote.Status = "unexpected-provider-state"
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "unknown" {
		t.Fatalf("unmapped state invented decision: %s", got)
	}
}

type fixture struct {
	db            *store.Store
	s             *Server
	token, cookie string
	session       store.Session
	connector     *fakeConnector
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db, err := store.Open(":memory:", "acme")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hash, err := auth.Hash("test password long enough")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CreateAdmin("admin", hash); err != nil {
		t.Fatal(err)
	}
	cookie, session, err := db.CreateSession("admin")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.IssueCredential(model.Identity{Organization: "acme", Developer: "alice", Projects: []string{"project-a"}})
	if err != nil {
		t.Fatal(err)
	}
	connector := &fakeConnector{}
	s, err := New(db, Options{AllowInsecureHTTP: true, ConnectorFactory: func(model.Connector) (model.ConnectorClient, error) { return connector, nil }})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{db: db, s: s, token: token, cookie: cookie, session: session, connector: connector}
	draft, err := db.Draft()
	if err != nil {
		t.Fatal(err)
	}
	draft.Connectors = []model.Connector{{ID: "tickets", Kind: "github", BaseURL: "https://tickets.example", TokenEnv: "TICKET_TOKEN", Settings: map[string]string{"repository": "acme/software"}}}
	draft.DefaultConnector = "tickets"
	draft.RequestScope = "project_developer"
	draft.Targets = []model.Target{{ID: "node-mac", Tool: "node", Version: "22.14.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22", Source: "homebrew/core", Grants: []model.Grant{}}}
	draft, err = db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) api(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	return w
}

func (f fixture) admin(t *testing.T, path string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "brew_session", Value: f.cookie})
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, r)
	return w
}

const requestBody = `{"tool":"node","version":"22.14.0","os":"darwin","arch":"arm64","installer":"brew","source":"homebrew/core","project":"project-a","reason":"Project runtime"}`

func TestProjectlessRequestsUseExplicitOutsideScope(t *testing.T) {
	for _, scope := range []string{"", "developer", "organization"} {
		t.Run(scope, func(t *testing.T) {
			f := newFixture(t)
			draft, _ := f.db.Draft()
			draft.OutsideProjectRequestScope = scope
			draft, err := f.db.SaveDraft(draft, "admin")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
				t.Fatal(err)
			}
			body := strings.Replace(requestBody, `"project-a"`, `""`, 1)
			w := f.api(t, "POST", "/api/v1/requests", body)
			if w.Code != 201 {
				t.Fatalf("projectless request rejected: %d %s", w.Code, w.Body.String())
			}
			first := decodedRequest(t, w)
			want := scope
			if want == "" {
				want = "developer"
			}
			if first.Scope != want {
				t.Fatalf("scope %q want %q", first.Scope, want)
			}
			w = f.api(t, "POST", "/api/v1/requests", body)
			if got := decodedRequest(t, w); got.ID != first.ID || f.connector.creates != 1 {
				t.Fatal("outside request was not reused")
			}
			// Removing project context never broadens the configured in-project
			// request scope or allows reuse of a project-bound request.
			w = f.api(t, "POST", "/api/v1/requests", requestBody)
			inside := decodedRequest(t, w)
			if inside.Scope != "project_developer" || inside.ID == first.ID {
				t.Fatalf("project scope broadened: %+v", inside)
			}
			bob, _, err := f.db.IssueCredential(model.Identity{Organization: "acme", Developer: "bob", Projects: []string{"project-a"}})
			if err != nil {
				t.Fatal(err)
			}
			f.token = bob
			w = f.api(t, "GET", "/api/v1/requests/"+first.ID, "")
			if scope == "organization" && w.Code != 200 {
				t.Fatal("organization request should be shared")
			}
			if scope != "organization" && w.Code != 404 {
				t.Fatal("developer request leaked to another developer")
			}
		})
	}
}

func TestProjectlessRequestsCannotUseProjectGrants(t *testing.T) {
	f := newFixture(t)
	draft, _ := f.db.Draft()
	draft.Targets[0].Grants = []model.Grant{{Organization: "acme", Project: "project-a", Developer: "alice"}}
	draft, err := f.db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(requestBody, `"project-a"`, `""`, 1)
	w := f.api(t, "POST", "/api/v1/requests", body)
	if decodedRequest(t, w).Status == "installable" {
		t.Fatal("project-specific grant authorized outside project")
	}
	draft.Targets[0].Grants = []model.Grant{{Organization: "acme", Developer: "alice"}}
	draft, err = f.db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	w = f.api(t, "POST", "/api/v1/requests", body)
	if got := decodedRequest(t, w).Status; got != "installable" {
		t.Fatalf("developer grant failed outside project: %s", got)
	}
}

func TestNativeRequestApprovalBindsPackageTuple(t *testing.T) {
	target := model.Target{Tool: "node", Version: "22.14.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22", PackageVersion: "22.14.0_1", PackageType: "formula", Source: "homebrew/core"}
	input := model.RequestInput{Tool: target.Tool, Version: target.Version, OS: target.OS, Arch: target.Arch, Installer: target.Installer, Package: target.Package, PackageVersion: target.PackageVersion, PackageType: target.PackageType, Source: target.Source}
	if !matchingTarget(target, input) || !validateInput(input) {
		t.Fatal("valid exact native binding rejected")
	}
	for _, mutate := range []func(*model.RequestInput){
		func(i *model.RequestInput) { i.Package = "other-package" },
		func(i *model.RequestInput) { i.PackageVersion = "22.14.0_2" },
		func(i *model.RequestInput) { i.PackageType = "cask" },
		func(i *model.RequestInput) { i.Source = "other/tap" },
	} {
		other := input
		mutate(&other)
		if matchingTarget(target, other) {
			t.Fatal("wrong native binding matched canonical tool approval")
		}
	}
	input.PackageVersion = ""
	if validateInput(input) {
		t.Fatal("partial native tuple accepted")
	}
	input.PackageVersion = "22.14.0_1"
	input.Package = "node;evil"
	if validateInput(input) {
		t.Fatal("unsafe native tuple accepted")
	}
}

func TestNativeRequestStatusRecognizesPublishedCanonicalName(t *testing.T) {
	f := newFixture(t)
	input := model.RequestInput{RequestType: "approval", Tool: "node@22", Version: "22.14.0_1", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22", PackageVersion: "22.14.0_1", PackageType: "formula", Source: "homebrew/core"}
	body, _ := json.Marshal(input)
	w := f.api(t, "POST", "/api/v1/requests", string(body))
	if w.Code != 201 {
		t.Fatalf("request failed: %d %s", w.Code, w.Body.String())
	}
	item := decodedRequest(t, w)
	f.connector.remote.Status = "approved"
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "approved_awaiting_catalog" {
		t.Fatalf("ticket granted install before catalog: %s", got)
	}
	draft, _ := f.db.Draft()
	draft.Targets[0].PackageVersion = input.PackageVersion
	draft.Targets[0].Grants = []model.Grant{{Organization: "acme", Developer: "alice"}}
	draft, err := f.db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "installable" {
		t.Fatalf("canonical tool/upstream version broke exact native approval: %s", got)
	}
	w = f.api(t, "POST", "/api/v1/requests", string(body))
	if got := decodedRequest(t, w).Status; got != "installable" || f.connector.creates != 1 {
		t.Fatal("canonical rename created another native request")
	}
	input.PackageVersion = "22.14.0_2"
	if f.s.allowed(input, model.Identity{Organization: "acme", Developer: "alice"}) {
		t.Fatal("different native revision authorized")
	}
	legacy := input
	legacy.Package, legacy.PackageVersion, legacy.PackageType = "", "", ""
	if matchingTarget(draft.Targets[0], legacy) {
		t.Fatal("legacy canonical matching broadened to aliases")
	}
}

func TestNativeRequestsReuseAcrossCanonicalNames(t *testing.T) {
	f := newFixture(t)
	input := model.RequestInput{RequestType: "approval", Tool: "node@22", Version: "22.14.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22", PackageVersion: "22.14.0", Source: "homebrew/core"}
	body, _ := json.Marshal(input)
	first := decodedRequest(t, f.api(t, "POST", "/api/v1/requests", string(body)))
	input.Tool, input.PackageType = "node", "formula"
	body, _ = json.Marshal(input)
	second := decodedRequest(t, f.api(t, "POST", "/api/v1/requests", string(body)))
	if first.ID == "" || first.ID != second.ID || f.connector.creates != 1 {
		t.Fatal("catalog canonical names duplicated native request")
	}
}

func TestNativeDNFApprovalMatchesZeroEpochIdentity(t *testing.T) {
	target := model.Target{Tool: "node", Version: "22.14.0", OS: "linux", Arch: "amd64", Installer: "dnf", Package: "nodejs", PackageVersion: "0:22.14.0-1.fc43", Source: "fedora"}
	input := model.RequestInput{Tool: "nodejs", Version: "22.14.0-1.fc43", OS: "linux", Arch: "amd64", Installer: "dnf", Package: "nodejs", PackageVersion: "22.14.0-1.fc43", Source: "fedora"}
	if !matchingTarget(target, input) {
		t.Fatal("DNF zero epoch identities failed to match")
	}
	input.PackageVersion = "1:22.14.0-1.fc43"
	if matchingTarget(target, input) {
		t.Fatal("DNF nonzero epoch matched zero epoch approval")
	}
}

func TestNativeRequestVersionsAcceptInstallerVersionFormats(t *testing.T) {
	for _, input := range []model.RequestInput{
		{Tool: "node", Version: "22.14.0", OS: "linux", Arch: "amd64", Installer: "apt", Package: "nodejs", PackageVersion: "22.14.0-1~22.04", Source: "https://packages.example"},
		{Tool: "docker", Version: "4.62.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "docker", PackageType: "cask", PackageVersion: "4.62.0,225307", Source: "homebrew/cask"},
	} {
		if !validateInput(input) {
			t.Fatalf("native version rejected: %+v", input)
		}
		input.Version = input.PackageVersion
		if !validateInput(input) {
			t.Fatalf("resolved native version without canonical catalog mapping rejected: %+v", input)
		}
	}
}

func TestUnresolvedProvisioningNeverAuthorizesInstallation(t *testing.T) {
	f := newFixture(t)
	draft, _ := f.db.Draft()
	draft.Targets[0].Grants = []model.Grant{{Organization: "acme"}}
	draft, err := f.db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	input := model.RequestInput{RequestType: "provisioning", Tool: "node", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22"}
	if !validateInput(input) {
		t.Fatal("unresolved packaging request rejected")
	}
	if matchingTarget(draft.Targets[0], input) {
		t.Fatal("unresolved package authorized")
	}
	body, _ := json.Marshal(input)
	w := f.api(t, "POST", "/api/v1/requests", string(body))
	if w.Code != 201 {
		t.Fatalf("provisioning failed: %d %s", w.Code, w.Body.String())
	}
	item := decodedRequest(t, w)
	if item.Status != "pending" || f.connector.creates != 1 {
		t.Fatal("unresolved package did not create provisioning request")
	}
	f.connector.remote.Status = "approved"
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "approved_awaiting_catalog" {
		t.Fatalf("unresolved package incorrectly installable: %s", got)
	}
	input.RequestType = "approval"
	if validateInput(input) {
		t.Fatal("unknown version accepted for approval")
	}
	input.RequestType = "provisioning"
	input.Package = "--evil"
	if validateInput(input) {
		t.Fatal("unsafe unresolved package accepted")
	}
}

func TestExplicitUnavailableVersionPreservesRequestWithoutSource(t *testing.T) {
	f := newFixture(t)
	draft, _ := f.db.Draft()
	draft.Targets[0].Grants = []model.Grant{{Organization: "acme"}}
	draft, err := f.db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	input := model.RequestInput{RequestType: "provisioning", Tool: "node", Version: "22.14.0", PackageVersion: "22.14.0", OS: "darwin", Arch: "arm64", Installer: "brew", Package: "node@22"}
	if !validateInput(input) {
		t.Fatal("explicit unavailable version without known source rejected")
	}
	if matchingTarget(draft.Targets[0], input) {
		t.Fatal("unknown source matched grant")
	}
	body, _ := json.Marshal(input)
	w := f.api(t, "POST", "/api/v1/requests", string(body))
	if w.Code != 201 {
		t.Fatalf("provisioning failed: %d %s", w.Code, w.Body.String())
	}
	item := decodedRequest(t, w)
	if item.Input.Version != input.Version || item.Input.PackageVersion != input.PackageVersion {
		t.Fatal("requested version not preserved")
	}
	f.connector.remote.Status = "approved"
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "approved_awaiting_catalog" {
		t.Fatalf("unknown source authorized: %s", got)
	}
	input.RequestType = "approval"
	if validateInput(input) {
		t.Fatal("partial native approval accepted")
	}
	input.RequestType = "provisioning"
	input.OS, input.Installer, input.Package = "linux", "apt", "nodejs"
	input.Version, input.PackageVersion = "1:22.14.0-1~22.04", "1:22.14.0-1~22.04"
	if !validateInput(input) {
		t.Fatal("explicit native epoch/tilde version rejected")
	}
	input.Source = "https://packages.example;evil"
	if validateInput(input) {
		t.Fatal("unsafe known source accepted for partial provisioning")
	}
}

func decodedRequest(t *testing.T, w *httptest.ResponseRecorder) model.SoftwareRequest {
	t.Helper()
	var item model.SoftwareRequest
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	return item
}

func TestCredentialsBindIdentityAndProject(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/catalog", nil))
	if w.Code != 401 {
		t.Fatalf("unauthenticated catalog status: %d", w.Code)
	}
	w = f.api(t, "POST", "/api/v1/requests", strings.Replace(requestBody, "project-a", "project-other", 1))
	if w.Code != 403 || f.connector.creates != 0 {
		t.Fatalf("unauthorized project: %d creates=%d", w.Code, f.connector.creates)
	}
	w = f.api(t, "POST", "/api/v1/requests", strings.Replace(requestBody, `"reason":`, `"developer":"admin","reason":`, 1))
	if w.Code != 400 {
		t.Fatalf("spoofed identity accepted: %d", w.Code)
	}
	for _, version := range []string{"latest", "stable", "HEAD"} {
		w = f.api(t, "POST", "/api/v1/requests", strings.Replace(requestBody, "22.14.0", version, 1))
		if w.Code != 400 {
			t.Fatalf("non-exact version %s accepted: %d", version, w.Code)
		}
	}
	w = f.api(t, "GET", "/api/v1/identity", "")
	var identity model.Identity
	_ = json.Unmarshal(w.Body.Bytes(), &identity)
	if identity.Developer != "alice" || identity.Organization != "acme" {
		t.Fatalf("wrong identity: %+v", identity)
	}
	credentials, _ := f.db.Credentials()
	if err := f.db.RevokeCredential(credentials[0].ID); err != nil {
		t.Fatal(err)
	}
	if w = f.api(t, "GET", "/api/v1/catalog", ""); w.Code != 401 {
		t.Fatalf("revoked credential accepted: %d", w.Code)
	}
}

func TestTicketApprovalDoesNotAuthorizeUntilPublication(t *testing.T) {
	f := newFixture(t)
	w := f.api(t, "POST", "/api/v1/requests", requestBody)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	item := decodedRequest(t, w)
	f.connector.remote.Status = "approved"
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "approved_awaiting_catalog" {
		t.Fatalf("ticket alone authorizes: %s", got)
	}
	draft, _ := f.db.Draft()
	draft.Targets[0].Grants = []model.Grant{{Organization: "acme", Project: "project-a", Developer: "alice"}}
	draft, err := f.db.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "approved_awaiting_catalog" {
		t.Fatalf("draft alone authorizes: %s", got)
	}
	if _, err = f.db.Publish(draft.Version, "admin"); err != nil {
		t.Fatal(err)
	}
	w = f.api(t, "GET", "/api/v1/requests/"+item.ID, "")
	if got := decodedRequest(t, w).Status; got != "installable" {
		t.Fatalf("publication did not authorize: %s", got)
	}
	stored, err := f.db.Request(item.ID)
	if err != nil || stored.Status != "approved" {
		t.Fatalf("decorated decision persisted: %+v %v", stored, err)
	}
	w = f.api(t, "POST", "/api/v1/requests", requestBody)
	if got := decodedRequest(t, w).Status; got != "installable" || f.connector.creates != 1 {
		t.Fatalf("approved tool submitted another ticket: %s %d", got, f.connector.creates)
	}
}

func TestRequestDeduplicationAndAmbiguousSubmission(t *testing.T) {
	f := newFixture(t)
	f.connector.createErr = errors.New("connection lost after submission")
	w := f.api(t, "POST", "/api/v1/requests", requestBody)
	first := decodedRequest(t, w)
	if first.Status != "unknown_submission" || f.connector.creates != 1 {
		t.Fatalf("unexpected uncertain result: %+v", first)
	}
	w = f.api(t, "POST", "/api/v1/requests", requestBody)
	second := decodedRequest(t, w)
	if second.ID != first.ID || f.connector.creates != 1 {
		t.Fatalf("uncertain ticket duplicated: %+v creates=%d", second, f.connector.creates)
	}
	f.connector.remote = &model.RemoteRequest{ID: "reconciled", Status: "pending", URL: "https://tickets.example/reconciled"}
	w = f.api(t, "POST", "/api/v1/requests", requestBody)
	third := decodedRequest(t, w)
	if third.ID != first.ID || third.ExternalID != "reconciled" || f.connector.creates != 1 {
		t.Fatalf("reconciliation failed: %+v", third)
	}
	other, _, err := f.db.IssueCredential(model.Identity{Organization: "acme", Developer: "bob", Projects: []string{"project-a"}})
	if err != nil {
		t.Fatal(err)
	}
	f.token = other
	w = f.api(t, "GET", "/api/v1/requests/"+first.ID, "")
	if w.Code != 404 {
		t.Fatalf("another developer can read scoped request: %d", w.Code)
	}
}

func TestAdminCSRFAndOptimisticDraftEditing(t *testing.T) {
	f := newFixture(t)
	draft, _ := f.db.Draft()
	encoded, _ := json.Marshal(draft)
	values := url.Values{"version": {"1"}, "draft": {string(encoded)}}
	w := f.admin(t, "/admin/draft", values)
	if w.Code != 403 {
		t.Fatalf("missing CSRF accepted: %d", w.Code)
	}
	values.Set("csrf", f.session.CSRF)
	w = f.admin(t, "/admin/draft", values)
	if w.Code != 200 {
		t.Fatalf("valid save: %d %s", w.Code, w.Body.String())
	}
	w = f.admin(t, "/admin/draft", values)
	if w.Code != 409 {
		t.Fatalf("stale draft accepted: %d", w.Code)
	}
	w = f.admin(t, "/admin/publish", url.Values{"version": {"1"}, "csrf": {f.session.CSRF}})
	if w.Code != 409 {
		t.Fatalf("stale publish accepted: %d", w.Code)
	}
	w = f.admin(t, "/admin/logout", url.Values{"csrf": {f.session.CSRF}})
	if w.Code != 303 {
		t.Fatalf("logout: %d", w.Code)
	}
	if _, err := f.db.Session(f.cookie); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("logout retained session: %v", err)
	}
}

func TestLoginCSRFAndRateLimit(t *testing.T) {
	f := newFixture(t)
	post := func(values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, r)
		return w
	}
	w := post(url.Values{"username": {"admin"}, "password": {"test password long enough"}}, nil)
	if w.Code != 403 {
		t.Fatalf("login CSRF bypass: %d", w.Code)
	}
	w = httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/admin/login", nil))
	cookie := w.Result().Cookies()[0]
	values := url.Values{"username": {"admin"}, "password": {"test password long enough"}, "login_csrf": {cookie.Value}}
	w = post(values, cookie)
	if w.Code != 303 {
		t.Fatalf("valid login failed: %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == "brew_session" && (!c.HttpOnly || c.SameSite != http.SameSiteStrictMode) {
			t.Fatal("session missing cookie protections")
		}
	}
	values.Set("password", "bad password")
	for i := 0; i < 10; i++ {
		w = post(values, cookie)
	}
	if w.Code != 429 {
		t.Fatalf("missing login rate limit: %d", w.Code)
	}
}
