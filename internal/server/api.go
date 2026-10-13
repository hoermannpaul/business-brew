package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"business-brew/internal/installers"
	"business-brew/internal/model"
	"business-brew/internal/policy"
	"business-brew/internal/store"
)

func (s *Server) identity(w http.ResponseWriter, r *http.Request, identity model.Identity) {
	jsonResponse(w, 200, identity)
}

func (s *Server) catalog(w http.ResponseWriter, r *http.Request, _ model.Identity) {
	cat, err := s.db.Published()
	if err != nil {
		jsonError(w, 500, "Unable to read catalog")
		return
	}
	jsonResponse(w, 200, cat)
}

func permittedProject(identity model.Identity, project string) bool {
	for _, allowed := range identity.Projects {
		if allowed == project {
			return true
		}
	}
	return false
}

func matchingTarget(target model.Target, input model.RequestInput) bool {
	if input.Version == "" {
		return false
	}
	if input.Package != "" || input.PackageVersion != "" || input.PackageType != "" {
		if input.Package == "" || input.PackageVersion == "" || input.Installer == "" || input.Source == "" {
			return false
		}
		native := model.Target{Installer: input.Installer, PackageVersion: input.PackageVersion, PackageType: input.PackageType}
		// The exact native binding is authoritative. Requests initially made
		// without a catalog may use a native package name/version for display;
		// later publication can assign canonical tool/version names to it.
		return input.Package == target.Package && native.NativeVersionIdentity() == target.NativeVersionIdentity() && native.NativePackageType() == target.NativePackageType() && input.OS == target.OS && input.Arch == target.Arch && input.Installer == target.Installer && input.Source == target.Source
	}
	return target.Tool == input.Tool && target.Version == input.Version && target.OS == input.OS && target.Arch == input.Arch && (input.Installer == "" || input.Installer == target.Installer) && (input.Source == "" || input.Source == target.Source)
}

func (s *Server) allowed(input model.RequestInput, identity model.Identity) bool {
	if input.Project != "" && !permittedProject(identity, input.Project) {
		return false
	}
	cat, err := s.db.Published()
	if err != nil {
		return false
	}
	allowed, matches := false, 0
	for _, target := range cat.Targets {
		if matchingTarget(target, input) {
			matches++
			allowed = allowed || policy.Allowed(target, identity, input.Project)
		}
	}
	// Treat ambiguous native bindings as blocked even if an older published
	// catalog predates validation of duplicate native mappings.
	return allowed && (input.Package == "" || matches == 1)
}

func validateInput(input model.RequestInput) bool {
	if input.RequestType != "" && input.RequestType != "approval" && input.RequestType != "provisioning" {
		return false
	}
	if !requestIdentifier.MatchString(input.Tool) || (input.Project != "" && !validID(input.Project)) || len(input.Reason) > 4000 {
		return false
	}
	unresolved := input.RequestType == "provisioning" && input.Version == ""
	nativeVersion := input.PackageVersion != "" && input.PackageVersion == input.Version
	if !unresolved && !nativeVersion && (!requestIdentifier.MatchString(input.Version) || strings.ContainsAny(input.Version, "*@><=~^")) {
		return false
	}
	switch strings.ToLower(input.Version) {
	case "latest", "stable", "head":
		return false
	}
	switch input.OS {
	case "darwin", "windows", "linux":
	default:
		return false
	}
	switch input.Arch {
	case "amd64", "arm64":
	default:
		return false
	}
	if input.Installer != "" {
		switch input.Installer {
		case "brew", "winget", "apt", "dnf":
		default:
			return false
		}
	}
	if input.Installer != "" && !((input.OS == "darwin" && input.Installer == "brew") || (input.OS == "windows" && input.Installer == "winget") || (input.OS == "linux" && (input.Installer == "brew" || input.Installer == "apt" || input.Installer == "dnf"))) {
		return false
	}
	if input.PackageType != "" && (input.Installer != "brew" || (input.PackageType != "formula" && input.PackageType != "cask")) {
		return false
	}
	if len(input.Source) > 500 || strings.ContainsAny(input.Source, "\r\n\x00") {
		return false
	}
	if input.RequestType == "provisioning" && (input.Package != "" || input.PackageVersion != "" || input.PackageType != "" || unresolved) {
		// A packaging request may preserve an explicitly requested version
		// even when metadata is unavailable. Incomplete tuples never match grants.
		if input.Package == "" || input.Installer == "" || len(input.PackageVersion) > 200 {
			return false
		}
		target := model.Target{OS: input.OS, Arch: input.Arch, Installer: input.Installer, Package: input.Package, PackageVersion: input.PackageVersion, PackageType: input.PackageType, Source: input.Source}
		if target.PackageVersion == "" {
			target.PackageVersion = "0"
		}
		if target.Source == "" {
			switch target.Installer {
			case "brew":
				target.Source = "homebrew/core"
			case "apt":
				target.Source = "https://unresolved.example"
			default:
				target.Source = "unresolved"
			}
		}
		return installers.Validate(target) == nil
	}
	if input.Package != "" || input.PackageVersion != "" || input.PackageType != "" {
		// Native requests bind the entire installer tuple; a partial tuple must
		// not accidentally match an approval for a different package or source.
		if input.Package == "" || input.PackageVersion == "" || input.Installer == "" || input.Source == "" || len(input.PackageVersion) > 200 {
			return false
		}
		if err := installers.Validate(model.Target{OS: input.OS, Arch: input.Arch, Installer: input.Installer, Package: input.Package, PackageVersion: input.PackageVersion, PackageType: input.PackageType, Source: input.Source}); err != nil {
			return false
		}
	}
	return true
}

var requestIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/+@-]{0,199}$`)

func connectorConfig(draft model.Draft, id string) (model.Connector, bool) {
	for _, connector := range draft.Connectors {
		if connector.ID == id {
			return connector, true
		}
	}
	return model.Connector{}, false
}

func (s *Server) createRequest(w http.ResponseWriter, r *http.Request, identity model.Identity) {
	var input model.RequestInput
	if err := readJSON(w, r, &input); err != nil || !validateInput(input) {
		jsonError(w, 400, "Provide a valid tool, exact version, operating system, architecture, and permitted project when applicable")
		return
	}
	if input.RequestType == "" {
		input.RequestType = "approval"
	}
	if input.Project != "" && !permittedProject(identity, input.Project) {
		jsonError(w, 403, "Project is not permitted by this credential")
		return
	}
	if input.RequestType != "provisioning" && s.allowed(input, identity) {
		jsonResponse(w, 200, model.SoftwareRequest{Organization: identity.Organization, Developer: identity.Developer, Project: input.Project, Input: input, Status: "installable"})
		return
	}
	draft, err := s.db.PublishedConfig()
	if err != nil {
		jsonError(w, 500, "Unable to read request configuration")
		return
	}
	connector, ok := connectorConfig(draft, draft.DefaultConnector)
	if !ok {
		jsonError(w, 503, "No request connector is configured in the published catalog")
		return
	}
	client, err := s.options.ConnectorFactory(connector)
	if err != nil {
		jsonError(w, 503, "Request connector is unavailable; contact your administrator")
		return
	}
	scope := draft.RequestScope
	if input.Project == "" {
		scope = draft.OutsideProjectRequestScope
		if scope == "" {
			scope = "developer"
		}
	}
	key := policy.RequestKey(identity.Organization, scope, input, identity.Developer)
	item, created, err := s.db.ReserveRequest(model.SoftwareRequest{Key: key, Organization: identity.Organization, Developer: identity.Developer, Project: input.Project, Scope: scope, Input: input, ConnectorID: connector.ID, Status: "submitting"})
	if err != nil {
		jsonError(w, 500, "Unable to reserve software request")
		return
	}
	if !created {
		if item.Status == "lookup_failed" {
			// Only one process can resume a reservation that is known never to
			// have attempted ticket creation. Crashed submitting reservations
			// remain reconciliation-only because submission may have happened.
			oldConnector, ok := connectorConfig(draft, item.ConnectorID)
			if !ok {
				jsonError(w, 503, "The request connector is no longer configured")
				return
			}
			client, err = s.options.ConnectorFactory(oldConnector)
			if err != nil {
				jsonError(w, 503, "Request connector is unavailable; contact your administrator")
				return
			}
			claimed, claimErr := s.db.ClaimRequestStatus(item.ID, "lookup_failed", "submitting")
			if claimErr != nil {
				jsonError(w, 500, "Unable to resume software request")
				return
			}
			if claimed {
				item.Status = "submitting"
				s.submit(w, r, identity, item, client)
				return
			}
			item, err = s.db.Request(item.ID)
			if err != nil {
				jsonError(w, 500, "Unable to read software request")
				return
			}
		}
		// The persisted reservation owns submission, even if a previous process
		// crashed. Read/find can reconcile it; never blindly resubmit a ticket.
		item = s.refresh(r.Context(), item)
		jsonResponse(w, 200, s.decorate(item, identity))
		return
	}
	s.submit(w, r, identity, item, client)
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request, identity model.Identity, item model.SoftwareRequest, client model.ConnectorClient) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	remote, err := client.Find(ctx, item)
	if err != nil {
		item.Status = "lookup_failed"
		if s.db.UpdateRequest(item) != nil {
			jsonError(w, 500, "Request reservation could not be updated; retry status")
			return
		}
		jsonResponse(w, 202, s.decorate(item, identity))
		return
	}
	if remote == nil {
		result, createErr := client.Create(ctx, item)
		if createErr != nil {
			item.Status = "unknown_submission"
		} else {
			remote = &result
		}
	}
	if remote != nil {
		applyRemote(&item, *remote)
	}
	if err := s.db.UpdateRequest(item); err != nil {
		jsonError(w, 500, "Request result could not be saved; retry status to reconcile the reserved request")
		return
	}
	jsonResponse(w, http.StatusCreated, s.decorate(item, identity))
}

func applyRemote(item *model.SoftwareRequest, remote model.RemoteRequest) {
	item.ExternalID = remote.ID
	item.URL = remote.URL
	switch remote.Status {
	case "approved", "rejected", "pending":
		item.Status = remote.Status
	default:
		item.Status = "unknown"
	}
	item.UpdatedAt = time.Now().UTC()
}

func (s *Server) refresh(ctx context.Context, item model.SoftwareRequest) model.SoftwareRequest {
	draft, err := s.db.PublishedConfig()
	if err != nil {
		return item
	}
	connector, ok := connectorConfig(draft, item.ConnectorID)
	if !ok {
		return item
	}
	client, err := s.options.ConnectorFactory(connector)
	if err != nil {
		return item
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var remote *model.RemoteRequest
	if item.ExternalID != "" {
		result, readErr := client.Read(ctx, item.ExternalID)
		if readErr != nil {
			return item
		}
		remote = &result
	} else {
		remote, err = client.Find(ctx, item)
		if err != nil || remote == nil {
			return item
		}
	}
	applyRemote(&item, *remote)
	if s.db.UpdateRequest(item) != nil {
		return item
	}
	return item
}

func (s *Server) decorate(item model.SoftwareRequest, identity model.Identity) model.SoftwareRequest {
	input := item.Input
	// Organization/developer-scoped tickets may be shared across projects;
	// only the request's originating project is evaluated here. A new setup
	// invocation checks the developer's actual project independently.
	if input.RequestType == "provisioning" {
		if item.Status == "approved" {
			if s.allowed(input, identity) {
				item.Status = "approved_awaiting_availability"
			} else {
				item.Status = "approved_awaiting_catalog"
			}
		}
	} else if s.allowed(input, identity) {
		item.Status = "installable"
	} else if item.Status == "approved" {
		item.Status = "approved_awaiting_catalog"
	}
	return item
}

func (s *Server) listRequests(w http.ResponseWriter, r *http.Request, identity model.Identity) {
	items, err := s.db.Requests()
	if err != nil {
		jsonError(w, 500, "Unable to read requests")
		return
	}
	visible := []model.SoftwareRequest{}
	for _, item := range items {
		if policy.RequestVisible(item, identity) {
			if r.URL.Query().Get("refresh") == "true" {
				item = s.refresh(r.Context(), item)
			}
			visible = append(visible, s.decorate(item, identity))
		}
	}
	jsonResponse(w, 200, visible)
}

func (s *Server) getRequest(w http.ResponseWriter, r *http.Request, identity model.Identity) {
	id := strings.TrimSpace(r.PathValue("id"))
	item, err := s.db.Request(id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !policy.RequestVisible(item, identity)) {
		jsonError(w, 404, "Request not found")
		return
	}
	if err != nil {
		jsonError(w, 500, "Unable to read request")
		return
	}
	item = s.refresh(r.Context(), item)
	jsonResponse(w, 200, s.decorate(item, identity))
}
