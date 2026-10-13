package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"business-brew/internal/catalog"
	"business-brew/internal/model"
	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("the draft changed; reload before saving or publishing")

type Store struct {
	db           *sql.DB
	organization string
}
type Admin struct {
	Username     string
	PasswordHash string
}
type Session struct {
	Username  string
	CSRF      string
	ExpiresAt time.Time
}
type CredentialInfo struct {
	ID        string         `json:"id"`
	Identity  model.Identity `json:"identity"`
	CreatedAt time.Time      `json:"created_at"`
}

func Open(path, organization string) (*Store, error) {
	if !subjectPattern.MatchString(organization) {
		return nil, fmt.Errorf("valid organization is required")
	}
	dsn := ":memory:"
	if path != ":memory:" {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(absolute), 0700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(absolute, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		file.Close()
		u := url.URL{Scheme: "file", Path: absolute}
		dsn = u.String()
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, organization: organization}
	if err := s.initialize(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error         { return s.db.Close() }
func (s *Store) Organization() string { return s.organization }

func (s *Store) initialize() error {
	_, err := s.db.Exec(`PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL;
CREATE TABLE IF NOT EXISTS deployment (id INTEGER PRIMARY KEY CHECK(id=1), organization TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS drafts (organization TEXT PRIMARY KEY, version INTEGER NOT NULL, body TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS catalogs (organization TEXT NOT NULL, revision INTEGER NOT NULL, body TEXT NOT NULL, config TEXT NOT NULL, PRIMARY KEY(organization,revision));
CREATE TABLE IF NOT EXISTS admins (organization TEXT NOT NULL, username TEXT NOT NULL, password_hash TEXT NOT NULL, PRIMARY KEY(organization,username));
CREATE TABLE IF NOT EXISTS sessions (organization TEXT NOT NULL, token_hash TEXT PRIMARY KEY, username TEXT NOT NULL, csrf TEXT NOT NULL, expires_at INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS credentials (organization TEXT NOT NULL, id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, identity TEXT NOT NULL, created_at INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(organization,id));
CREATE TABLE IF NOT EXISTS requests (organization TEXT NOT NULL, id TEXT NOT NULL, request_key TEXT NOT NULL, body TEXT NOT NULL, PRIMARY KEY(organization,id), UNIQUE(organization,request_key));
CREATE TABLE IF NOT EXISTS audit (organization TEXT NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, revision INTEGER NOT NULL, created_at INTEGER NOT NULL);`)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO deployment(id,organization) VALUES(1,?) ON CONFLICT(id) DO NOTHING`, s.organization); err != nil {
		return err
	}
	var existing string
	if err := s.db.QueryRow(`SELECT organization FROM deployment WHERE id=1`).Scan(&existing); err != nil {
		return err
	}
	if existing != s.organization {
		return fmt.Errorf("database belongs to a different organization")
	}
	draft := model.Draft{Targets: []model.Target{}, Connectors: []model.Connector{}, RequestScope: "project"}
	body, _ := json.Marshal(draft)
	_, err = s.db.Exec(`INSERT INTO drafts(organization,version,body) VALUES(?,0,?) ON CONFLICT(organization) DO NOTHING`, s.organization, string(body))
	return err
}

func (s *Store) Draft() (model.Draft, error) {
	var draft model.Draft
	var body string
	err := s.db.QueryRow(`SELECT body FROM drafts WHERE organization=?`, s.organization).Scan(&body)
	if err != nil {
		return draft, err
	}
	err = json.Unmarshal([]byte(body), &draft)
	return draft, err
}

func (s *Store) SaveDraft(draft model.Draft, actor string) (model.Draft, error) {
	if err := catalog.ValidateDraft(draft, s.organization); err != nil {
		return model.Draft{}, err
	}
	expected := draft.Version
	draft.Version++
	body, err := json.Marshal(draft)
	if err != nil {
		return model.Draft{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return model.Draft{}, err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE drafts SET version=?,body=? WHERE organization=? AND version=?`, draft.Version, string(body), s.organization, expected)
	if err != nil {
		return model.Draft{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return model.Draft{}, err
	}
	if n != 1 {
		return model.Draft{}, ErrConflict
	}
	if _, err := tx.Exec(`INSERT INTO audit VALUES(?,?,?,?,?)`, s.organization, actor, "save_draft", draft.Version, time.Now().Unix()); err != nil {
		return model.Draft{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Draft{}, err
	}
	return draft, nil
}

func (s *Store) Publish(expectedVersion int64, actor string) (model.Catalog, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return model.Catalog{}, err
	}
	defer tx.Rollback()
	var body string
	var version int64
	if err := tx.QueryRow(`SELECT version,body FROM drafts WHERE organization=?`, s.organization).Scan(&version, &body); err != nil {
		return model.Catalog{}, err
	}
	if version != expectedVersion {
		return model.Catalog{}, ErrConflict
	}
	var draft model.Draft
	if err := json.Unmarshal([]byte(body), &draft); err != nil {
		return model.Catalog{}, err
	}
	if err := catalog.ValidateDraft(draft, s.organization); err != nil {
		return model.Catalog{}, err
	}
	var revision int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(revision),0)+1 FROM catalogs WHERE organization=?`, s.organization).Scan(&revision); err != nil {
		return model.Catalog{}, err
	}
	snapshot := model.Catalog{SchemaVersion: 1, Revision: revision, PublishedAt: time.Now().UTC(), Targets: draft.Targets}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return model.Catalog{}, err
	}
	if _, err := tx.Exec(`INSERT INTO catalogs(organization,revision,body,config) VALUES(?,?,?,?)`, s.organization, revision, string(encoded), body); err != nil {
		return model.Catalog{}, err
	}
	if _, err := tx.Exec(`INSERT INTO audit VALUES(?,?,?,?,?)`, s.organization, actor, "publish", revision, snapshot.PublishedAt.Unix()); err != nil {
		return model.Catalog{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.Catalog{}, err
	}
	return snapshot, nil
}

func (s *Store) Published() (model.Catalog, error) {
	var body string
	result := model.Catalog{SchemaVersion: 1, Targets: []model.Target{}}
	err := s.db.QueryRow(`SELECT body FROM catalogs WHERE organization=? ORDER BY revision DESC LIMIT 1`, s.organization).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(body), &result)
	return result, err
}

func (s *Store) PublishedConfig() (model.Draft, error) {
	var body string
	result := model.Draft{Targets: []model.Target{}, Connectors: []model.Connector{}, RequestScope: "project"}
	err := s.db.QueryRow(`SELECT config FROM catalogs WHERE organization=? ORDER BY revision DESC LIMIT 1`, s.organization).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(body), &result)
	return result, err
}

var subjectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)

func (s *Store) AdminCount() (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM admins WHERE organization=?`, s.organization).Scan(&count)
	return count, err
}
func (s *Store) CreateAdmin(username, hash string) error {
	if !subjectPattern.MatchString(username) || hash == "" {
		return fmt.Errorf("valid username and password hash required")
	}
	_, err := s.db.Exec(`INSERT INTO admins VALUES(?,?,?)`, s.organization, username, hash)
	return err
}
func (s *Store) Admin(username string) (Admin, error) {
	a := Admin{Username: username}
	err := s.db.QueryRow(`SELECT password_hash FROM admins WHERE organization=? AND username=?`, s.organization, username).Scan(&a.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}
func (s *Store) SetAdminPassword(username, hash string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.Exec(`UPDATE admins SET password_hash=? WHERE organization=? AND username=?`, hash, s.organization, username)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE organization=? AND username=?`, s.organization, username); err != nil {
		return err
	}
	return tx.Commit()
}

func secret(bytes int) (string, error) {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateSession(username string) (string, Session, error) {
	if _, err := s.Admin(username); err != nil {
		return "", Session{}, err
	}
	cookie, err := secret(32)
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := secret(32)
	if err != nil {
		return "", Session{}, err
	}
	session := Session{Username: username, CSRF: csrf, ExpiresAt: time.Now().Add(8 * time.Hour)}
	_, err = s.db.Exec(`INSERT INTO sessions VALUES(?,?,?,?,?)`, s.organization, digest(cookie), username, csrf, session.ExpiresAt.Unix())
	return cookie, session, err
}
func (s *Store) Session(cookie string) (Session, error) {
	var session Session
	var expires int64
	err := s.db.QueryRow(`SELECT username,csrf,expires_at FROM sessions WHERE organization=? AND token_hash=?`, s.organization, digest(cookie)).Scan(&session.Username, &session.CSRF, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return session, ErrNotFound
	}
	if err != nil {
		return session, err
	}
	session.ExpiresAt = time.Unix(expires, 0)
	if !time.Now().Before(session.ExpiresAt) {
		return session, ErrNotFound
	}
	return session, nil
}
func (s *Store) DeleteSession(cookie string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE organization=? AND token_hash=?`, s.organization, digest(cookie))
	return err
}

func (s *Store) IssueCredential(identity model.Identity) (string, string, error) {
	if identity.Organization != s.organization || !subjectPattern.MatchString(identity.Developer) {
		return "", "", fmt.Errorf("credential requires this organization and a developer")
	}
	for _, project := range identity.Projects {
		if !subjectPattern.MatchString(project) {
			return "", "", fmt.Errorf("invalid project")
		}
	}
	slices.Sort(identity.Projects)
	identity.Projects = slices.Compact(identity.Projects)
	token, err := secret(32)
	if err != nil {
		return "", "", err
	}
	token = "bb_dev_" + token
	id, err := secret(16)
	if err != nil {
		return "", "", err
	}
	body, err := json.Marshal(identity)
	if err != nil {
		return "", "", err
	}
	_, err = s.db.Exec(`INSERT INTO credentials(organization,id,token_hash,identity,created_at) VALUES(?,?,?,?,?)`, s.organization, id, digest(token), string(body), time.Now().Unix())
	return token, id, err
}
func (s *Store) Identity(token string) (model.Identity, error) {
	var body string
	var identity model.Identity
	err := s.db.QueryRow(`SELECT identity FROM credentials WHERE organization=? AND token_hash=? AND revoked=0`, s.organization, digest(token)).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return identity, ErrNotFound
	}
	if err != nil {
		return identity, err
	}
	err = json.Unmarshal([]byte(body), &identity)
	return identity, err
}
func (s *Store) Credentials() ([]CredentialInfo, error) {
	rows, err := s.db.Query(`SELECT id,identity,created_at FROM credentials WHERE organization=? AND revoked=0 ORDER BY created_at DESC`, s.organization)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CredentialInfo{}
	for rows.Next() {
		var c CredentialInfo
		var body string
		var created int64
		if err := rows.Scan(&c.ID, &body, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &c.Identity); err != nil {
			return nil, err
		}
		c.CreatedAt = time.Unix(created, 0)
		result = append(result, c)
	}
	return result, rows.Err()
}
func (s *Store) RevokeCredential(id string) error {
	r, err := s.db.Exec(`UPDATE credentials SET revoked=1 WHERE organization=? AND id=?`, s.organization, id)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ReserveRequest(request model.SoftwareRequest) (model.SoftwareRequest, bool, error) {
	if request.Organization != s.organization || request.Key == "" {
		return model.SoftwareRequest{}, false, fmt.Errorf("request organization and key required")
	}
	var err error
	request.ID, err = secret(16)
	if err != nil {
		return request, false, err
	}
	request.CreatedAt = time.Now().UTC()
	request.UpdatedAt = request.CreatedAt
	request.Status = "submitting"
	body, err := json.Marshal(request)
	if err != nil {
		return request, false, err
	}
	r, err := s.db.Exec(`INSERT INTO requests(organization,id,request_key,body) VALUES(?,?,?,?) ON CONFLICT(organization,request_key) DO NOTHING`, s.organization, request.ID, request.Key, string(body))
	if err != nil {
		return request, false, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return request, false, err
	}
	if n == 1 {
		return request, true, nil
	}
	var existing string
	if err := s.db.QueryRow(`SELECT body FROM requests WHERE organization=? AND request_key=?`, s.organization, request.Key).Scan(&existing); err != nil {
		return request, false, err
	}
	err = json.Unmarshal([]byte(existing), &request)
	return request, false, err
}
func (s *Store) Request(id string) (model.SoftwareRequest, error) {
	var request model.SoftwareRequest
	var body string
	err := s.db.QueryRow(`SELECT body FROM requests WHERE organization=? AND id=?`, s.organization, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return request, ErrNotFound
	}
	if err != nil {
		return request, err
	}
	err = json.Unmarshal([]byte(body), &request)
	return request, err
}
func (s *Store) UpdateRequest(request model.SoftwareRequest) error {
	if request.Organization != s.organization {
		return fmt.Errorf("request belongs to a different organization")
	}
	request.UpdatedAt = time.Now().UTC()
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	r, err := s.db.Exec(`UPDATE requests SET body=? WHERE organization=? AND id=? AND request_key=?`, string(body), s.organization, request.ID, request.Key)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimRequestStatus gives one caller ownership of a retry known not to have
// submitted a remote ticket. Ambiguous submissions are never eligible.
func (s *Store) ClaimRequestStatus(id, expected, next string) (bool, error) {
	result, err := s.db.Exec(`UPDATE requests SET body=json_set(body,'$.status',?,'$.updated_at',?) WHERE organization=? AND id=? AND json_extract(body,'$.status')=?`, next, time.Now().UTC().Format(time.RFC3339Nano), s.organization, id, expected)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}
func (s *Store) Requests() ([]model.SoftwareRequest, error) {
	rows, err := s.db.Query(`SELECT body FROM requests WHERE organization=? ORDER BY rowid DESC`, s.organization)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.SoftwareRequest{}
	for rows.Next() {
		var body string
		var request model.SoftwareRequest
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			return nil, err
		}
		result = append(result, request)
	}
	return result, rows.Err()
}
