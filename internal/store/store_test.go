package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"business-brew/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), "acme")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func testTarget() model.Target {
	return model.Target{ID: "node", Tool: "node", Version: "22.1.0", OS: "linux", Arch: "amd64", Installer: "brew", Package: "node@22", Source: "homebrew/core", Grants: []model.Grant{{Organization: "acme"}}}
}

func TestPublicationAndConflictingEdits(t *testing.T) {
	s := testStore(t)
	draft, _ := s.Draft()
	draft.Targets = []model.Target{testTarget()}
	saved, err := s.SaveDraft(draft, "admin")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.Published()
	if before.Revision != 0 || len(before.Targets) != 0 {
		t.Fatal("draft leaked into published snapshot")
	}
	if _, err := s.SaveDraft(draft, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	if _, err := s.Publish(draft.Version, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publish: %v", err)
	}
	first, err := s.Publish(saved.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || len(first.Targets) != 1 {
		t.Fatal("snapshot incomplete")
	}
	saved.Targets[0].Grants = nil
	saved, err = s.SaveDraft(saved, "admin")
	if err != nil {
		t.Fatal(err)
	}
	unchanged, _ := s.Published()
	if len(unchanged.Targets[0].Grants) != 1 {
		t.Fatal("published snapshot mutated by draft")
	}
	second, err := s.Publish(saved.Version, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 || len(second.Targets[0].Grants) != 0 {
		t.Fatal("grant revocation not published")
	}
}

func TestCredentialBindingRevocationAndSessionInvalidation(t *testing.T) {
	s := testStore(t)
	identity := model.Identity{Organization: "acme", Developer: "alice", Projects: []string{"billing"}}
	token, id, err := s.IssueCredential(identity)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := s.Identity(token)
	if err != nil || actual.Developer != "alice" {
		t.Fatal("identity binding lost")
	}
	identity.Organization = "other"
	if _, _, err := s.IssueCredential(identity); err == nil {
		t.Fatal("cross-org credential accepted")
	}
	if err := s.RevokeCredential(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Identity(token); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked credential still active")
	}
	if err := s.CreateAdmin("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	cookie, _, err := s.CreateSession("admin")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAdminPassword("admin", "different hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(cookie); !errors.Is(err, ErrNotFound) {
		t.Fatal("password reset did not invalidate session")
	}
}

func TestCredentialWithoutProjectsPreservesDeveloperIdentity(t *testing.T) {
	s := testStore(t)
	token, _, err := s.IssueCredential(model.Identity{Organization: "acme", Developer: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.Identity(token)
	if err != nil || identity.Organization != "acme" || identity.Developer != "alice" || len(identity.Projects) != 0 {
		t.Fatalf("organization/developer-only credential lost identity: %+v %v", identity, err)
	}
}

func TestConcurrentRequestsReserveExactlyOneRecord(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	ids := map[string]bool{}
	for range 8 {
		wg.Go(func() {
			r, new, err := s.ReserveRequest(model.SoftwareRequest{Organization: "acme", Key: "same", Scope: "project"})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if new {
				created++
			}
			ids[r.ID] = true
		})
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("created=%d unique ids=%d", created, len(ids))
	}
}

func TestDatabasePersistsAndRejectsDifferentOrganization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	s, err := Open(path, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAdmin("admin", "hash"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if other, err := Open(path, "other"); err == nil {
		other.Close()
		t.Fatal("database reassigned to another organization")
	}
	s, err = Open(path, "acme")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if count, err := s.AdminCount(); err != nil || count != 1 {
		t.Fatal("admin did not persist")
	}
}
