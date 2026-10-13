package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLandingPageIsPublicAndAdminRemainsProtected(t *testing.T) {
	f := newFixture(t)
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("public page: status %d, type %s", w.Code, w.Header().Get("Content-Type"))
	}
	for _, content := range []string{"Keep your tools.", "For management &amp; IT", `id="terminal-output"`, `src="/static/landing.js"`} {
		if !strings.Contains(w.Body.String(), content) {
			t.Errorf("landing page missing %q", content)
		}
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") || len(w.Result().Cookies()) != 0 {
		t.Fatal("public page must allow local scripts without issuing a session")
	}
	for _, path := range []string{"/admin", "/admin/credentials", "/admin/requests"} {
		w = httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/admin/login" {
			t.Errorf("%s no longer requires login: status %d", path, w.Code)
		}
		if strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src") {
			t.Errorf("landing script policy leaked to %s", path)
		}
	}
}

func TestLandingAssetsAreEmbeddedAndUnknownPathsRemainNotFound(t *testing.T) {
	f := newFixture(t)
	for path, contentType := range map[string]string{
		"/static/landing.css":   "text/css",
		"/static/landing.js":    "javascript",
		"/static/brew-mark.svg": "image/svg+xml",
	} {
		w := httptest.NewRecorder()
		f.s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), contentType) {
			t.Errorf("%s: status %d, type %s", path, w.Code, w.Header().Get("Content-Type"))
		}
	}
	w := httptest.NewRecorder()
	f.s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/not-a-page", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown route served landing page: %d", w.Code)
	}
}
