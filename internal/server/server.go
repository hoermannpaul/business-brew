package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"business-brew/internal/auth"
	"business-brew/internal/model"
	"business-brew/internal/requests"
	"business-brew/internal/store"
	"business-brew/web"
)

type Options struct {
	// AllowInsecureHTTP is for explicitly enabled loopback development only.
	AllowInsecureHTTP bool
	ConnectorFactory  model.ConnectorFactory
}

type Server struct {
	db            *store.Store
	options       Options
	templates     *template.Template
	handler       http.Handler
	loginMu       sync.Mutex
	loginAttempts map[string]attempt
	dummyHash     string
}

type attempt struct {
	Count int
	Start time.Time
}

func New(db *store.Store, options Options) (*Server, error) {
	t, err := template.ParseFS(web.Assets, "templates/*.html")
	if err != nil {
		return nil, err
	}
	if options.ConnectorFactory == nil {
		options.ConnectorFactory = requests.NewConnector
	}
	dummyHash, err := auth.Hash(randomToken())
	if err != nil {
		return nil, err
	}
	s := &Server{db: db, options: options, templates: t, loginAttempts: make(map[string]attempt)}
	s.dummyHash = dummyHash
	mux := http.NewServeMux()
	assets, err := fs.Sub(web.Assets, "static")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))
	mux.HandleFunc("GET /{$}", s.landingPage)
	mux.HandleFunc("GET /admin/login", s.loginPage)
	mux.HandleFunc("POST /admin/login", s.login)
	mux.HandleFunc("GET /admin", s.admin(s.catalogPage))
	mux.HandleFunc("POST /admin/draft", s.admin(s.saveDraft))
	mux.HandleFunc("GET /admin/target", s.admin(s.targetPage))
	mux.HandleFunc("POST /admin/target", s.admin(s.saveTarget))
	mux.HandleFunc("POST /admin/target/delete", s.admin(s.deleteTarget))
	mux.HandleFunc("POST /admin/publish", s.admin(s.publish))
	mux.HandleFunc("POST /admin/logout", s.admin(s.logout))
	mux.HandleFunc("GET /admin/credentials", s.admin(s.credentialsPage))
	mux.HandleFunc("POST /admin/credentials", s.admin(s.issueCredential))
	mux.HandleFunc("POST /admin/credentials/revoke", s.admin(s.revokeCredential))
	mux.HandleFunc("GET /admin/requests", s.admin(s.requestsPage))
	mux.HandleFunc("GET /api/v1/identity", s.api(s.identity))
	mux.HandleFunc("GET /api/v1/catalog", s.api(s.catalog))
	mux.HandleFunc("POST /api/v1/requests", s.api(s.createRequest))
	mux.HandleFunc("GET /api/v1/requests", s.api(s.listRequests))
	mux.HandleFunc("GET /api/v1/requests/{id}", s.api(s.getRequest))
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) secureCookie() bool { return !s.options.AllowInsecureHTTP }

func (s *Server) cookie(w http.ResponseWriter, name, value string, expiry time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.secureCookie(), SameSite: http.SameSiteStrictMode, Expires: expiry})
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("operating system randomness unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sameToken(a, b string) bool {
	return a != "" && b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func (s *Server) admin(next func(http.ResponseWriter, *http.Request, store.Session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("brew_session")
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		session, err := s.db.Session(cookie.Value)
		if err != nil || time.Now().After(session.ExpiresAt) {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if err := r.ParseForm(); err != nil || !sameToken(r.PostForm.Get("csrf"), session.CSRF) {
				http.Error(w, "Invalid form token. Reload the page and retry.", http.StatusForbidden)
				return
			}
		}
		next(w, r, session)
	}
}

func (s *Server) api(next func(http.ResponseWriter, *http.Request, model.Identity)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			jsonError(w, http.StatusUnauthorized, "Developer credential required")
			return
		}
		identity, err := s.db.Identity(strings.TrimPrefix(header, "Bearer "))
		if err != nil || identity.Organization != s.db.Organization() {
			jsonError(w, http.StatusUnauthorized, "Invalid developer credential")
			return
		}
		next(w, r, identity)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

func jsonResponse(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func jsonError(w http.ResponseWriter, status int, message string) {
	jsonResponse(w, status, map[string]string{"error": message})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	token := randomToken()
	s.cookie(w, "brew_login", token, time.Now().Add(15*time.Minute))
	s.render(w, http.StatusOK, pageData{Title: "Sign in", Login: true, LoginCSRF: token})
}

func (s *Server) limited(remoteAddr, username string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	now := time.Now()
	key := host + "\x00" + strings.ToLower(username)
	a := s.loginAttempts[key]
	if now.Sub(a.Start) > 15*time.Minute {
		a = attempt{Start: now}
	}
	a.Count++
	s.loginAttempts[key] = a
	// Bound memory without trusting proxy-provided client addresses.
	if len(s.loginAttempts) > 10000 {
		for key, item := range s.loginAttempts {
			if now.Sub(item.Start) > 15*time.Minute {
				delete(s.loginAttempts, key)
			}
		}
	}
	return a.Count > 10 || len(s.loginAttempts) > 10000
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid sign-in form", http.StatusBadRequest)
		return
	}
	csrf, err := r.Cookie("brew_login")
	if err != nil || !sameToken(csrf.Value, r.PostForm.Get("login_csrf")) {
		http.Error(w, "Invalid sign-in token. Reload the sign-in page.", http.StatusForbidden)
		return
	}
	username := strings.TrimSpace(r.PostForm.Get("username"))
	if s.limited(r.RemoteAddr, username) {
		http.Error(w, "Too many sign-in attempts. Try again in 15 minutes.", http.StatusTooManyRequests)
		return
	}
	password := r.PostForm.Get("password")
	admin, err := s.db.Admin(username)
	hash := admin.PasswordHash
	if err != nil {
		hash = s.dummyHash
	}
	validPassword := auth.Verify(hash, password)
	if err != nil || !validPassword {
		token := randomToken()
		s.cookie(w, "brew_login", token, time.Now().Add(15*time.Minute))
		s.render(w, http.StatusUnauthorized, pageData{Title: "Sign in", Login: true, LoginCSRF: token, Error: "Username or password is incorrect."})
		return
	}
	value, session, err := s.db.CreateSession(username)
	if err != nil {
		http.Error(w, "Unable to sign in", http.StatusInternalServerError)
		return
	}
	s.cookie(w, "brew_session", value, session.ExpiresAt)
	s.cookie(w, "brew_login", "", time.Unix(1, 0))
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request, _ store.Session) {
	cookie, _ := r.Cookie("brew_session")
	if cookie != nil {
		if err := s.db.DeleteSession(cookie.Value); err != nil {
			http.Error(w, "Unable to sign out", 500)
			return
		}
	}
	s.cookie(w, "brew_session", "", time.Unix(1, 0))
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}
