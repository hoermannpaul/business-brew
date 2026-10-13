package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestServiceRedirectCannotLeakCredentials(t *testing.T) {
	leaked := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer source.Close()
	var result any
	err := New(source.URL, "secret", nil).Call(context.Background(), "GET", "/api/v1/catalog", nil, &result)
	if err == nil || leaked {
		t.Fatal("service redirect followed with credential")
	}
}
func TestResponseMustBeSingleBoundedJSONDocument(t *testing.T) {
	for _, body := range []string{`{} {}`, strings.Repeat(" ", (8<<20)+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		var value any
		err := New(server.URL, "credential", nil).Call(context.Background(), "GET", "/api/v1/catalog", nil, &value)
		server.Close()
		if err == nil {
			t.Fatal("invalid/oversized service response accepted")
		}
	}
}
