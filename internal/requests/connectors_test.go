package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"business-brew/internal/model"
)

func requestFixture() model.SoftwareRequest {
	return model.SoftwareRequest{Key: "012345abcdef", Organization: "acme", Developer: "alice", Project: "app", Scope: "project", Input: model.RequestInput{Tool: "node", Version: "22.0.0", OS: "linux", Arch: "amd64", Installer: "apt", Source: "company", Reason: "Needed for project", RequestType: "approval"}}
}
func configFixture(kind, base string) model.Connector {
	c := model.Connector{ID: kind, Kind: kind, BaseURL: base, TokenEnv: "BREW_TEST_TOKEN", StatusMapping: map[string]string{"Waiting": "pending", "Authorized": "approved", "Denied": "rejected"}, Settings: map[string]string{}}
	switch kind {
	case "github":
		c.Settings["repository"] = "acme/requests"
	case "jira":
		c.Settings["serviceDeskId"] = "12"
		c.Settings["requestTypeId"] = "34"
	case "servicenow":
		c.Settings = map[string]string{"catalogItemId": "catalog-id", "keyField": "u_brew_key", "keyVariable": "brew_key", "variables": `{"description":"{{description}}","software":"{{tool}}"}`}
	case "azure_devops":
		c.Settings["project"] = "Dev Tools"
		c.Settings["workItemType"] = "Task"
	case "linear":
		c.Settings["teamId"] = "team-id"
	}
	return c
}
func decodeBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}
func fixtureConnector(t *testing.T, kind string, h http.HandlerFunc) model.ConnectorClient {
	t.Helper()
	t.Setenv("BREW_TEST_TOKEN", "fixture-secret")
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	client, err := NewConnector(configFixture(kind, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	return client
}
func respond(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func TestGitHubFindUsesExactMarkerAndPagination(t *testing.T) {
	r := requestFixture()
	posts := 0
	client := fixtureConnector(t, "github", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer fixture-secret" {
			t.Error("missing auth")
		}
		switch {
		case req.Method == "POST":
			posts++
			body := decodeBody(t, req)
			if body["body"] != description(r) {
				t.Errorf("unexpected request body: %v", body)
			}
			respond(w, map[string]any{"number": 3, "state": "Waiting"})
		case req.URL.Query().Get("page") == "2":
			respond(w, []any{map[string]any{"number": 2, "body": description(r), "state": "closed"}})
		case strings.HasSuffix(req.URL.Path, "/issues/2"):
			respond(w, map[string]any{"number": 2, "state": "Authorized"})
		default:
			w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?page=2>; rel="next"`, req.Host, req.URL.Path))
			respond(w, []any{map[string]any{"number": 1, "body": marker(r.Key) + "-other", "state": "Authorized"}, map[string]any{"number": 4, "body": description(r), "pull_request": map[string]any{"url": "x"}, "state": "Authorized"}})
		}
	})
	found, err := client.Find(context.Background(), r)
	if err != nil || found == nil || found.ID != "2" || found.Status != "unknown" {
		t.Fatalf("find %v %v", found, err)
	}
	created, err := client.Create(context.Background(), r)
	if err != nil || created.ID != "3" || created.Status != "pending" || posts != 1 {
		t.Fatalf("create %v %v", created, err)
	}
	read, err := client.Read(context.Background(), "2")
	if err != nil || read.Status != "approved" {
		t.Fatalf("read %v %v", read, err)
	}
}
func TestJiraUsesServiceDeskRequestAPI(t *testing.T) {
	r := requestFixture()
	client := fixtureConnector(t, "jira", func(w http.ResponseWriter, req *http.Request) {
		issue := map[string]any{"issueKey": "IT-1", "currentStatus": map[string]any{"status": "Authorized"}, "requestFieldValues": []any{map[string]any{"fieldId": "description", "value": description(r)}}}
		switch req.Method {
		case "POST":
			body := decodeBody(t, req)
			if body["serviceDeskId"] != "12" || body["requestTypeId"] != "34" || pathValue(body, "requestFieldValues.description") != description(r) {
				t.Errorf("bad request: %v", body)
			}
			respond(w, issue)
		default:
			if strings.HasSuffix(req.URL.Path, "/IT-1") {
				respond(w, issue)
			} else {
				if req.URL.Query().Get("serviceDeskId") != "12" || req.URL.Query().Get("requestTypeId") != "34" {
					t.Error("missing service scope")
				}
				respond(w, map[string]any{"values": []any{issue}})
			}
		}
	})
	assertProviderLifecycle(t, client, r, "IT-1")
}
func TestServiceNowResolvesRequestedItemAfterOrder(t *testing.T) {
	r := requestFixture()
	orders := 0
	client := fixtureConnector(t, "servicenow", func(w http.ResponseWriter, req *http.Request) {
		item := map[string]any{"sys_id": "ritm-id", "u_brew_key": r.Key, "approval": "Authorized"}
		switch {
		case strings.Contains(req.URL.Path, "/order_now"):
			orders++
			body := decodeBody(t, req)
			if pathValue(body, "variables.brew_key") != r.Key || pathValue(body, "variables.software") != "node" || body["sysparm_quantity"] != "1" {
				t.Errorf("bad catalog order: %v", body)
			}
			respond(w, map[string]any{"result": map[string]any{"request_id": "req-id"}})
		case strings.HasSuffix(req.URL.Path, "/ritm-id"):
			respond(w, map[string]any{"result": item})
		default:
			query := req.URL.Query().Get("sysparm_query")
			if query != "request=req-id^cat_item=catalog-id" && query != "u_brew_key="+r.Key+"^cat_item=catalog-id" {
				t.Errorf("bad query %q", query)
			}
			respond(w, map[string]any{"result": []any{item}})
		}
	})
	assertProviderLifecycle(t, client, r, "ritm-id")
	if orders != 1 {
		t.Fatal("catalog order was retried")
	}
}
func TestAzureUsesPatchAndVerifiesTags(t *testing.T) {
	r := requestFixture()
	client := fixtureConnector(t, "azure_devops", func(w http.ResponseWriter, req *http.Request) {
		issue := map[string]any{"id": 27, "fields": map[string]any{"System.State": "Authorized", "System.Tags": marker(r.Key)}}
		switch {
		case strings.HasSuffix(req.URL.Path, "/wiql"):
			body := decodeBody(t, req)
			if !strings.Contains(str(body["query"]), marker(r.Key)) {
				t.Error("missing query marker")
			}
			respond(w, map[string]any{"workItems": []any{map[string]any{"id": 27}}})
		case req.Method == "POST":
			if req.Header.Get("Content-Type") != "application/json-patch+json" {
				t.Error("missing JSON patch media type")
			}
			var patch []map[string]any
			if json.NewDecoder(req.Body).Decode(&patch) != nil || len(patch) != 3 {
				t.Error("invalid patch")
			}
			if !strings.Contains(req.URL.Path, "Dev Tools") {
				t.Error("wrong project path")
			}
			respond(w, issue)
		default:
			respond(w, issue)
		}
	})
	assertProviderLifecycle(t, client, r, "27")
}
func TestLinearUsesGraphQLVariablesAndPagination(t *testing.T) {
	r := requestFixture()
	client := fixtureConnector(t, "linear", func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "fixture-secret" {
			t.Error("wrong Linear authentication")
		}
		body := decodeBody(t, req)
		query := str(body["query"])
		issue := map[string]any{"id": "linear-id", "state": map[string]any{"name": "Authorized"}, "description": description(r)}
		switch {
		case strings.Contains(query, "issueCreate"):
			if pathValue(body, "variables.input.teamId") != "team-id" || pathValue(body, "variables.input.description") != description(r) {
				t.Error("incorrect create variables")
			}
			respond(w, map[string]any{"data": map[string]any{"issueCreate": map[string]any{"success": true, "issue": issue}}})
		case strings.Contains(query, "query Read"):
			if pathValue(body, "variables.id") != "linear-id" {
				t.Error("incorrect read variable")
			}
			respond(w, map[string]any{"data": map[string]any{"issue": issue}})
		default:
			if pathValue(body, "variables.after") == nil {
				respond(w, map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []any{}, "pageInfo": map[string]any{"hasNextPage": true, "endCursor": "next"}}}})
			} else {
				respond(w, map[string]any{"data": map[string]any{"issues": map[string]any{"nodes": []any{issue}, "pageInfo": map[string]any{"hasNextPage": false}}}})
			}
		}
	})
	assertProviderLifecycle(t, client, r, "linear-id")
}
func assertProviderLifecycle(t *testing.T, client model.ConnectorClient, r model.SoftwareRequest, id string) {
	t.Helper()
	found, err := client.Find(context.Background(), r)
	if err != nil || found == nil || found.ID != id || found.Status != "approved" {
		t.Fatalf("find %v %v", found, err)
	}
	created, err := client.Create(context.Background(), r)
	if err != nil || created.ID != id || created.Status != "approved" {
		t.Fatalf("create %v %v", created, err)
	}
	read, err := client.Read(context.Background(), id)
	if err != nil || read.ID != id || read.Status != "approved" {
		t.Fatalf("read %v %v", read, err)
	}
}

func httpConfig(base string) model.Connector {
	return model.Connector{ID: "custom", Kind: "http", BaseURL: base, TokenEnv: "BREW_TEST_TOKEN", StatusMapping: map[string]string{"Accepted": "approved"}, Operations: map[string]model.HTTPOperation{
		"find":   {Method: "GET", Path: "/requests?key={{key}}", IDPath: "id", KeyPath: "key", StatusPath: "status", ResultsPath: "items", NextPath: "next", CursorParameter: "cursor"},
		"create": {Method: "POST", Path: "/requests", Body: json.RawMessage(`{"key":"{{key}}","name":"{{tool}}","reason":"{{reason}}","request_type":"{{request_type}}"}`), IDPath: "id", StatusPath: "status"},
		"read":   {Method: "GET", Path: "/requests/{{id}}", IDPath: "id", StatusPath: "status"},
	}}
}
func TestHTTPStructuredSubstitutionAndExactKeys(t *testing.T) {
	r := requestFixture()
	r.Input.Tool = `tool"},"injected":true`
	r.Input.Reason = "Line1\nLine2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == "POST" {
			body := decodeBody(t, req)
			if len(body) != 4 || body["name"] != r.Input.Tool || body["reason"] != r.Input.Reason || body["request_type"] != r.Input.RequestType {
				t.Error("template altered structure")
			}
			respond(w, map[string]any{"id": "id", "status": "Accepted"})
			return
		}
		if req.URL.Query().Get("cursor") == "second" {
			respond(w, map[string]any{"items": []any{map[string]any{"id": "id", "key": r.Key, "status": "Accepted"}}})
			return
		}
		if strings.HasPrefix(req.URL.Path, "/requests/") {
			if req.URL.EscapedPath() != "/requests/part%2Fnext%3Fsecret=1" || req.URL.RawQuery != "" {
				t.Errorf("unsafe path: %s", req.URL.EscapedPath())
			}
			respond(w, map[string]any{"id": "id", "status": "Accepted"})
			return
		}
		respond(w, map[string]any{"items": []any{map[string]any{"id": "wrong", "key": r.Key + "extra", "status": "Accepted"}}, "next": "second"})
	}))
	defer server.Close()
	t.Setenv("BREW_TEST_TOKEN", "fixture-secret")
	client, err := NewConnector(httpConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	found, err := client.Find(context.Background(), r)
	if err != nil || found.ID != "id" {
		t.Fatalf("find %v %v", found, err)
	}
	if _, err := client.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Read(context.Background(), "part/next?secret=1"); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsUnsafeConfigurations(t *testing.T) {
	for _, base := range []string{"http://example.com", "https://u:p@example.com", "https://example.com?x=1", "file:///tmp/test", "//example.com", "https://example.com#fragment"} {
		if ValidateConnector(configFixture("github", base)) == nil {
			t.Errorf("accepted %s", base)
		}
	}
	c := httpConfig("https://example.com")
	op := c.Operations["find"]
	op.KeyPath = ""
	c.Operations["find"] = op
	if ValidateConnector(c) == nil {
		t.Error("accepted search without key validation")
	}
	c = configFixture("github", "https://example.com")
	c.StatusMapping["Closed"] = "installed"
	if ValidateConnector(c) == nil {
		t.Error("accepted invalid mapping")
	}
	c = httpConfig("https://example.com")
	op = c.Operations["read"]
	op.Path = "//evil.com/requests"
	c.Operations["read"] = op
	if ValidateConnector(c) == nil {
		t.Error("accepted external path")
	}
}

func TestMalformedAndFailedProviderResponses(t *testing.T) {
	cases := []struct{ name, kind, body string }{
		{"github missing ID", "github", `{"state":"Waiting"}`}, {"jira missing ID", "jira", `{"currentStatus":{"status":"Waiting"}}`}, {"azure missing ID", "azure_devops", `{"fields":{"System.State":"Waiting"}}`},
		{"linear errors with data", "linear", `{"errors":[{"message":"fixture-secret"}],"data":{"issueCreate":{"success":true,"issue":{"id":"id"}}}}`},
		{"linear unsuccessful", "linear", `{"data":{"issueCreate":{"success":false,"issue":{"id":"id"}}}}`},
		{"linear missing ID", "linear", `{"data":{"issueCreate":{"success":true,"issue":{}}}}`},
		{"servicenow missing request", "servicenow", `{"result":{}}`},
		{"invalid JSON", "github", `{"number":`}, {"trailing JSON", "github", `{"number":1} {"number":2}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client := fixtureConnector(t, tc.kind, func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(tc.body)) })
			_, err := client.Create(context.Background(), requestFixture())
			if err == nil {
				t.Fatal("accepted failed response")
			}
			if strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("secret leaked")
			}
			if calls != 1 {
				t.Fatalf("POST retried %d times", calls)
			}
		})
	}
}
func TestHTTPFailuresNeverLeakResponseBodyOrRetry(t *testing.T) {
	for _, status := range []int{400, 401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			client := fixtureConnector(t, "github", func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				_, _ = w.Write([]byte("fixture-secret"))
			})
			_, err := client.Create(context.Background(), requestFixture())
			if err == nil || strings.Contains(err.Error(), "fixture-secret") || calls != 1 {
				t.Fatalf("calls %d, error %v", calls, err)
			}
		})
	}
}
func TestRedirectAndPaginationCannotLeakCredentials(t *testing.T) {
	destinationCalls := 0
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls++; respond(w, []any{}) }))
	defer destination.Close()
	for _, redirect := range []bool{false, true} {
		t.Run(fmt.Sprint(redirect), func(t *testing.T) {
			client := fixtureConnector(t, "github", func(w http.ResponseWriter, r *http.Request) {
				if redirect {
					http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
				} else {
					w.Header().Set("Link", "<"+destination.URL+`>; rel="next"`)
					respond(w, []any{})
				}
			})
			if _, err := client.Find(context.Background(), requestFixture()); err == nil {
				t.Fatal("followed unsafe destination")
			}
		})
	}
	if destinationCalls != 0 {
		t.Fatal("sent credentials to other origin")
	}
}
func TestSearchPaginationLoopFailsClosed(t *testing.T) {
	calls := 0
	client := fixtureConnector(t, "github", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Link", fmt.Sprintf(`<http://%s%s?state=all&per_page=100>; rel="next"`, r.Host, r.URL.Path))
		respond(w, []any{})
	})
	if _, err := client.Find(context.Background(), requestFixture()); err == nil {
		t.Fatal("search loop treated as not found")
	}
	if calls > 2 {
		t.Fatal("search loop not bounded")
	}
}
func TestResponseBodyBound(t *testing.T) {
	client := fixtureConnector(t, "github", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat(" ", maxResponse+1)))
	})
	if _, err := client.Read(context.Background(), "1"); err == nil {
		t.Fatal("accepted oversized body")
	}
}
func TestMissingCredentialDoesNotReachProvider(t *testing.T) {
	calls := 0
	client := fixtureConnector(t, "github", func(w http.ResponseWriter, r *http.Request) { calls++ })
	t.Setenv("BREW_TEST_TOKEN", "")
	if _, err := client.Read(context.Background(), "1"); err == nil || calls != 0 {
		t.Fatal("request sent without credential")
	}
}
