package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"business-brew/internal/model"
	"business-brew/internal/store"
)

func (s *Server) targetPage(w http.ResponseWriter, r *http.Request, session store.Session) {
	draft, err := s.db.Draft()
	if err != nil {
		http.Error(w, "Unable to read catalog", 500)
		return
	}
	id := r.URL.Query().Get("id")
	target := model.Target{OS: "darwin", Arch: "arm64", Installer: "brew", Source: "homebrew/core"}
	if id != "" {
		found := false
		for _, item := range draft.Targets {
			if item.ID == id {
				target = item
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}
	rows := []grantRow{}
	for i, g := range target.Grants {
		rows = append(rows, grantRow{Index: i, Enabled: true, Project: g.Project, Developer: g.Developer})
	}
	rows = append(rows, grantRow{Index: len(rows)})
	s.targetView(w, session, 200, target, id, draft.Version, rows, "")
}

func (s *Server) targetView(w http.ResponseWriter, session store.Session, status int, target model.Target, original string, version int64, rows []grantRow, message string) {
	data := s.data(session, "Software approval")
	data.Catalog = true
	data.TargetEditor = true
	data.Target = target
	data.OriginalID = original
	data.Version = version
	data.GrantRows = rows
	data.Error = message
	s.render(w, status, data)
}

func (s *Server) saveTarget(w http.ResponseWriter, r *http.Request, session store.Session) {
	version, err := formVersion(r)
	if err != nil {
		http.Error(w, "Invalid draft version", 400)
		return
	}
	original := r.PostForm.Get("original_id")
	target := model.Target{ID: original, Tool: strings.TrimSpace(r.PostForm.Get("tool")), Version: strings.TrimSpace(r.PostForm.Get("target_version")), OS: r.PostForm.Get("os"), Arch: r.PostForm.Get("arch"), Installer: r.PostForm.Get("installer"), Package: strings.TrimSpace(r.PostForm.Get("package")), PackageVersion: strings.TrimSpace(r.PostForm.Get("package_version")), PackageType: r.PostForm.Get("package_type"), Source: strings.TrimSpace(r.PostForm.Get("source")), Grants: []model.Grant{}}
	projects, developers := r.PostForm["grant_project"], r.PostForm["grant_developer"]
	if len(projects) != len(developers) || len(projects) > 1000 {
		http.Error(w, "Invalid grants", 400)
		return
	}
	rows := make([]grantRow, 0, len(projects)+1)
	for i := range projects {
		row := grantRow{Index: i, Enabled: r.PostForm.Get(fmt.Sprintf("grant_enabled_%d", i)) == "on", Project: strings.TrimSpace(projects[i]), Developer: strings.TrimSpace(developers[i])}
		rows = append(rows, row)
		if row.Enabled {
			target.Grants = append(target.Grants, model.Grant{Organization: s.db.Organization(), Project: row.Project, Developer: row.Developer})
		}
	}
	if r.PostForm.Get("action") == "add_grant" {
		rows = append(rows, grantRow{Index: len(rows)})
		s.targetView(w, session, 200, target, original, version, rows, "")
		return
	}
	if target.ID == "" {
		target.ID = "target-" + randomToken()
	}
	draft, err := s.db.Draft()
	if err != nil {
		http.Error(w, "Unable to read draft", 500)
		return
	}
	if draft.Version != version {
		s.targetView(w, session, 409, target, original, version, rows, "The draft changed. Open Catalog and reload this entry before saving.")
		return
	}
	if original == "" {
		draft.Targets = append(draft.Targets, target)
	} else {
		found := false
		for i := range draft.Targets {
			if draft.Targets[i].ID == original {
				draft.Targets[i] = target
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}
	if _, err := s.db.SaveDraft(draft, session.Username); err != nil {
		status := 400
		if errors.Is(err, store.ErrConflict) {
			status = 409
		}
		s.targetView(w, session, status, target, original, version, rows, err.Error())
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (s *Server) deleteTarget(w http.ResponseWriter, r *http.Request, session store.Session) {
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid draft version", 400)
		return
	}
	draft, err := s.db.Draft()
	if err != nil {
		http.Error(w, "Unable to read draft", 500)
		return
	}
	if version != draft.Version {
		s.catalogView(w, session, 409, "", "The draft changed. Review it before removing an entry.")
		return
	}
	id := r.PostForm.Get("id")
	found := false
	remaining := []model.Target{}
	for _, target := range draft.Targets {
		if target.ID == id {
			found = true
			continue
		}
		remaining = append(remaining, target)
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	draft.Targets = remaining
	if _, err := s.db.SaveDraft(draft, session.Username); err != nil {
		s.catalogView(w, session, 400, "", err.Error())
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
