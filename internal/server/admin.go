package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"business-brew/internal/model"
	"business-brew/internal/store"
)

type pageData struct {
	Title, Username, CSRF, Organization           string
	Login, Catalog, CredentialsPage, RequestsPage bool
	LoginCSRF, Error, Message, DraftJSON, Token   string
	Revision, Version                             int64
	Credentials                                   []store.CredentialInfo
	Requests                                      []model.SoftwareRequest
	Targets                                       []model.Target
	TargetEditor                                  bool
	Target                                        model.Target
	OriginalID                                    string
	GrantRows                                     []grantRow
}

type grantRow struct {
	Index              int
	Enabled            bool
	Project, Developer string
}

func (s *Server) render(w http.ResponseWriter, status int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = s.templates.ExecuteTemplate(w, "page", data)
}

func (s *Server) data(session store.Session, title string) pageData {
	return pageData{Title: title, Username: session.Username, CSRF: session.CSRF, Organization: s.db.Organization()}
}

func (s *Server) catalogPage(w http.ResponseWriter, r *http.Request, session store.Session) {
	s.catalogView(w, session, http.StatusOK, "", "")
}

func (s *Server) catalogView(w http.ResponseWriter, session store.Session, status int, message, errorMessage string, attempted ...string) {
	draft, err := s.db.Draft()
	if err != nil {
		http.Error(w, "Unable to read draft", 500)
		return
	}
	cat, err := s.db.Published()
	if err != nil {
		http.Error(w, "Unable to read published catalog", 500)
		return
	}
	encoded, err := json.MarshalIndent(draft, "", "  ")
	if err != nil {
		http.Error(w, "Unable to display draft", 500)
		return
	}
	data := s.data(session, "Catalog")
	data.Catalog = true
	data.Version = draft.Version
	data.Revision = cat.Revision
	data.DraftJSON = string(encoded)
	data.Targets = draft.Targets
	if len(attempted) > 0 {
		data.DraftJSON = attempted[0]
	}
	if len(attempted) > 1 {
		if version, err := strconv.ParseInt(attempted[1], 10, 64); err == nil {
			data.Version = version
		}
	}
	data.Error = errorMessage
	data.Message = message
	s.render(w, status, data)
}

func formVersion(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
}

func (s *Server) saveDraft(w http.ResponseWriter, r *http.Request, session store.Session) {
	var draft model.Draft
	decoder := json.NewDecoder(strings.NewReader(r.PostForm.Get("draft")))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		s.catalogView(w, session, 400, "", "The draft must be valid JSON with recognized fields.", r.PostForm.Get("draft"), r.PostForm.Get("version"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		s.catalogView(w, session, 400, "", "Provide exactly one draft document.", r.PostForm.Get("draft"), r.PostForm.Get("version"))
		return
	}
	version, err := formVersion(r)
	if err != nil || draft.Version != version {
		s.catalogView(w, session, 400, "", "Draft version must match the version shown on this page.", r.PostForm.Get("draft"), r.PostForm.Get("version"))
		return
	}
	_, err = s.db.SaveDraft(draft, session.Username)
	if err != nil {
		status := 400
		message := err.Error()
		if errors.Is(err, store.ErrConflict) {
			status = 409
			message = "The draft changed in another session. Copy your changes below, open Catalog to reload, then apply them to the latest version."
		}
		s.catalogView(w, session, status, "", message, r.PostForm.Get("draft"), r.PostForm.Get("version"))
		return
	}
	s.catalogView(w, session, 200, "Draft saved. Publish it to make changes available to developers.", "")
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request, session store.Session) {
	version, err := formVersion(r)
	if err != nil {
		s.catalogView(w, session, 400, "", "Invalid draft version.")
		return
	}
	_, err = s.db.Publish(version, session.Username)
	if err != nil {
		status := 400
		message := err.Error()
		if errors.Is(err, store.ErrConflict) {
			status = 409
			message = "The draft changed in another session. Review the latest version before publishing."
		}
		s.catalogView(w, session, status, "", message)
		return
	}
	s.catalogView(w, session, 200, "Catalog published. Developers can now use the new revision.", "")
}

func (s *Server) credentialsPage(w http.ResponseWriter, r *http.Request, session store.Session) {
	s.credentialsView(w, session, 200, "", "")
}

func (s *Server) credentialsView(w http.ResponseWriter, session store.Session, status int, token, errorMessage string) {
	credentials, err := s.db.Credentials()
	if err != nil {
		http.Error(w, "Unable to read credentials", 500)
		return
	}
	data := s.data(session, "Developer access")
	data.CredentialsPage = true
	data.Credentials = credentials
	data.Token = token
	data.Error = errorMessage
	s.render(w, status, data)
}

func (s *Server) issueCredential(w http.ResponseWriter, r *http.Request, session store.Session) {
	developer := strings.TrimSpace(r.PostForm.Get("developer"))
	var projects []string
	seen := map[string]bool{}
	for _, project := range strings.Split(r.PostForm.Get("projects"), ",") {
		project = strings.TrimSpace(project)
		if project != "" && !seen[project] {
			projects = append(projects, project)
			seen[project] = true
		}
	}
	if !validID(developer) {
		s.credentialsView(w, session, 400, "", "Provide a developer ID. Project IDs are optional for organization/developer-only access.")
		return
	}
	for _, project := range projects {
		if !validID(project) {
			s.credentialsView(w, session, 400, "", "Project IDs must be 1–200 characters without control characters.")
			return
		}
	}
	identity := model.Identity{Organization: s.db.Organization(), Developer: developer, Projects: projects}
	token, _, err := s.db.IssueCredential(identity)
	if err != nil {
		s.credentialsView(w, session, 500, "", "Unable to issue developer credential.")
		return
	}
	s.credentialsView(w, session, 200, token, "")
}

func (s *Server) revokeCredential(w http.ResponseWriter, r *http.Request, session store.Session) {
	if err := s.db.RevokeCredential(r.PostForm.Get("id")); err != nil {
		s.credentialsView(w, session, 400, "", "Credential could not be revoked.")
		return
	}
	http.Redirect(w, r, "/admin/credentials", http.StatusSeeOther)
}

func (s *Server) requestsPage(w http.ResponseWriter, r *http.Request, session store.Session) {
	items, err := s.db.Requests()
	if err != nil {
		http.Error(w, "Unable to read requests", 500)
		return
	}
	data := s.data(session, "Requests")
	data.RequestsPage = true
	data.Requests = items
	s.render(w, 200, data)
}

func validID(value string) bool {
	return subjectIdentifier.MatchString(value)
}

var subjectIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`)
