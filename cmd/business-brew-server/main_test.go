package main

import (
	"path/filepath"
	"strings"
	"testing"

	"business-brew/internal/auth"
	"business-brew/internal/store"
)

func TestOperatorBootstrapAndPasswordRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.db")
	args := []string{"bootstrap", "--organization", "acme", "--database", path, "--username", "admin"}
	if err := run(args, strings.NewReader("initial test password\n")); err != nil {
		t.Fatal(err)
	}
	if err := run(args, strings.NewReader("replacement password\n")); err == nil {
		t.Fatal("bootstrap allowed an existing administrator")
	}
	db, err := store.Open(path, "acme")
	if err != nil {
		t.Fatal(err)
	}
	cookie, _, err := db.CreateSession("admin")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	args[0] = "reset-password"
	if err := run(args, strings.NewReader("replacement password\n")); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(path, "acme")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	admin, err := db.Admin("admin")
	if err != nil || !auth.Verify(admin.PasswordHash, "replacement password") {
		t.Fatal("password reset failed")
	}
	if _, err = db.Session(cookie); err == nil {
		t.Fatal("password reset retained previous session")
	}
}

func TestInsecureCookiesRestrictedToLoopback(t *testing.T) {
	if err := run([]string{"serve", "--organization", "acme", "--listen", "0.0.0.0:8080", "--allow-insecure-http"}, strings.NewReader("")); err == nil {
		t.Fatal("insecure service accepted non-loopback listen")
	}
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		if !loopback(address) {
			t.Fatalf("loopback rejected: %s", address)
		}
	}
}

func TestOperatorCanAddSeparateAdministrator(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admins.db")
	if err := run([]string{"bootstrap", "--organization", "acme", "--database", path, "--username", "first"}, strings.NewReader("initial test password\n")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"add-admin", "--organization", "acme", "--database", path, "--username", "second"}, strings.NewReader("second test password\n")); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(path, "acme")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if n, err := db.AdminCount(); err != nil || n != 2 {
		t.Fatal("second local account unavailable")
	}
}
