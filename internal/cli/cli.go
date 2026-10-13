package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"slices"
	"time"

	"business-brew/internal/client"
	"business-brew/internal/config"
	"business-brew/internal/installers"
	"business-brew/internal/managed"
	"business-brew/internal/model"
	"business-brew/internal/policy"
)

type App struct {
	Out       io.Writer
	Err       io.Writer
	HTTP      *http.Client
	Installer installers.Adapter
	OS        string
	Arch      string
	Getenv    func(string) string
}

func (a App) Run(ctx context.Context, args []string) int {
	if a.Out == nil {
		a.Out = os.Stdout
	}
	if a.Err == nil {
		a.Err = os.Stderr
	}
	if a.HTTP == nil {
		a.HTTP = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	useNative := a.Installer == nil
	if a.OS == "" {
		a.OS = runtime.GOOS
	}
	if a.Arch == "" {
		a.Arch = runtime.GOARCH
	}
	if a.Getenv == nil {
		a.Getenv = os.Getenv
	}
	if len(args) == 0 {
		fmt.Fprintln(a.Err, "usage: business-brew <plan|setup|status> --profile <managed-profile.json> [--file business-brew.json]")
		return 2
	}
	command := args[0]
	if command != "plan" && command != "setup" && command != "status" {
		fmt.Fprintln(a.Err, "unknown command:", command)
		return 2
	}
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(a.Err)
	profilePath := f.String("profile", "", "company-managed profile path")
	manifestPath := f.String("file", "business-brew.json", "project requirements file")
	if err := f.Parse(args[1:]); err != nil {
		return 2
	}
	if *profilePath == "" || f.NArg() != 0 {
		fmt.Fprintln(a.Err, "--profile is required; positional arguments are not supported")
		return 2
	}
	p, err := config.LoadProfile(*profilePath)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 2
	}
	if useNative {
		if len(p.NativeBinaries) > 0 {
			a.Installer = installers.Native{Runner: managed.Runner{NativeBinaries: p.NativeBinaries}}
		} else {
			a.Installer = installers.Native{}
		}
	}
	m, err := config.LoadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 2
	}
	token, err := managed.Credential(p, a.Getenv)
	if err != nil {
		fmt.Fprintln(a.Err, err)
		return 2
	}
	api := client.New(p.ServiceURL, token, a.HTTP)
	var identity model.Identity
	if err := api.Call(ctx, http.MethodGet, "/api/v1/identity", nil, &identity); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	if p.Organization != identity.Organization || p.Developer != identity.Developer || !sameProjects(p.Projects, identity.Projects) {
		fmt.Fprintln(a.Err, "managed profile identity/projects do not match the service credential")
		return 1
	}
	if !slices.Contains(identity.Projects, m.Project) {
		fmt.Fprintln(a.Err, "project is not permitted by the managed credential")
		return 1
	}
	if command == "status" {
		return a.status(ctx, api, m)
	}
	preferences := p.Installers[a.OS]
	if len(preferences) == 0 {
		fmt.Fprintln(a.Err, "managed profile has no installers for", a.OS)
		return 1
	}
	var catalog model.Catalog
	if err := api.Call(ctx, http.MethodGet, "/api/v1/catalog", nil, &catalog); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	fmt.Fprintf(a.Out, "Project %s · %s/%s · catalog revision %d\n", m.Project, a.OS, a.Arch, catalog.Revision)
	complete := true
	for _, requirement := range m.Tools {
		if command == "setup" {
			// Do not authorize a later install from the snapshot fetched for earlier tools.
			if err := api.Call(ctx, http.MethodGet, "/api/v1/catalog", nil, &catalog); err != nil {
				fmt.Fprintln(a.Err, err)
				complete = false
				continue
			}
		}
		target, found, err := selectTarget(catalog, requirement, a.OS, a.Arch, preferences)
		if err != nil {
			fmt.Fprintf(a.Err, "%s: %v\n", requirement.Tool, err)
			complete = false
			continue
		}
		reason := ""
		if !found {
			reason = "no matching catalog package; provisioning required"
		} else if !policy.Allowed(target, identity, m.Project) {
			reason = "approval required"
		}
		if reason != "" {
			complete = false
			if command == "setup" {
				requestType := "approval"
				if !found {
					requestType = "provisioning"
				}
				a.request(ctx, api, requirement, target, m.Project, reason, requestType)
			} else {
				fmt.Fprintf(a.Out, "%s %s: %s\n", requirement.Tool, requirement.Version, reason)
			}
			continue
		}
		state, err := a.Installer.Check(ctx, target)
		if err != nil {
			fmt.Fprintf(a.Err, "%s %s: native check failed: %v\n", requirement.Tool, requirement.Version, err)
			complete = false
			if errors.Is(err, installers.ErrUnavailable) && command == "setup" {
				a.request(ctx, api, requirement, target, m.Project, "exact native version unavailable; provisioning required", "provisioning")
			}
			continue
		}
		if state.Installed {
			fmt.Fprintf(a.Out, "%s %s: installed and approved\n", requirement.Tool, requirement.Version)
			continue
		}
		if !state.Available {
			complete = false
			if command == "setup" {
				a.request(ctx, api, requirement, target, m.Project, "exact native version unavailable; provisioning required", "provisioning")
			} else {
				fmt.Fprintf(a.Out, "%s %s: exact native version unavailable\n", requirement.Tool, requirement.Version)
			}
			continue
		}
		if command == "plan" {
			fmt.Fprintf(a.Out, "%s %s: approved, ready to install via %s\n", requirement.Tool, requirement.Version, target.Installer)
			continue
		}
		// Check authorization again after native availability discovery, immediately before mutation.
		var latest model.Catalog
		if err := api.Call(ctx, http.MethodGet, "/api/v1/catalog", nil, &latest); err != nil {
			fmt.Fprintln(a.Err, err)
			complete = false
			continue
		}
		fresh, found, err := selectTarget(latest, requirement, a.OS, a.Arch, preferences)
		if err != nil || !found || !policy.Allowed(fresh, identity, m.Project) || !sameTarget(target, fresh) {
			fmt.Fprintf(a.Err, "%s: catalog authorization or package changed; rerun setup\n", requirement.Tool)
			complete = false
			continue
		}
		if err := a.Installer.Install(ctx, fresh); err != nil {
			fmt.Fprintf(a.Err, "%s %s: installation failed: %v\n", requirement.Tool, requirement.Version, err)
			complete = false
			if errors.Is(err, installers.ErrUnavailable) {
				a.request(ctx, api, requirement, fresh, m.Project, "exact native version unavailable; provisioning required", "provisioning")
			}
			continue
		}
		fmt.Fprintf(a.Out, "%s %s: installed and verified\n", requirement.Tool, requirement.Version)
	}
	if !complete {
		return 1
	}
	return 0
}

func sameProjects(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, p := range a {
		if !slices.Contains(b, p) {
			return false
		}
	}
	return true
}

func sameTarget(a, b model.Target) bool {
	return a.ID == b.ID && a.Tool == b.Tool && a.Version == b.Version && a.OS == b.OS && a.Arch == b.Arch && a.Installer == b.Installer && a.Package == b.Package && a.PackageType == b.PackageType && a.NativeVersion() == b.NativeVersion() && a.Source == b.Source
}

func selectTarget(c model.Catalog, r model.Requirement, platform, arch string, preferences []string) (model.Target, bool, error) {
	if c.SchemaVersion != 1 || c.Revision <= 0 {
		return model.Target{}, false, fmt.Errorf("service has no valid published catalog")
	}
	for _, installer := range preferences {
		var found []model.Target
		for _, t := range c.Targets {
			if t.Tool == r.Tool && t.Version == r.Version && t.OS == platform && t.Arch == arch && t.Installer == installer {
				found = append(found, t)
			}
		}
		if len(found) > 1 {
			return model.Target{}, false, fmt.Errorf("ambiguous catalog targets for %s", installer)
		}
		if len(found) == 1 {
			if err := installers.Validate(found[0]); err != nil {
				return model.Target{}, false, err
			}
			return found[0], true, nil
		}
	}
	return model.Target{}, false, nil
}

func (a App) request(ctx context.Context, api client.Client, r model.Requirement, t model.Target, project, reason, requestType string) {
	input := model.RequestInput{Tool: r.Tool, Version: r.Version, OS: a.OS, Arch: a.Arch, Installer: t.Installer, Source: t.Source, Project: project, Reason: reason}
	input.RequestType = requestType
	if r.Reason != "" {
		input.Reason += "; " + r.Reason
	}
	var request model.SoftwareRequest
	if err := api.Call(ctx, http.MethodPost, "/api/v1/requests", input, &request); err != nil {
		fmt.Fprintf(a.Err, "%s: could not create/reuse request: %v\n", r.Tool, err)
		return
	}
	fmt.Fprintf(a.Out, "%s %s: %s; request %s (%s) %s\n", r.Tool, r.Version, reason, request.ID, request.Status, request.URL)
}

func (a App) status(ctx context.Context, api client.Client, m model.Manifest) int {
	var requests []model.SoftwareRequest
	if err := api.Call(ctx, http.MethodGet, "/api/v1/requests", nil, &requests); err != nil {
		fmt.Fprintln(a.Err, err)
		return 1
	}
	found := false
	for _, request := range requests {
		matches := false
		for _, r := range m.Tools {
			if r.Tool == request.Input.Tool && r.Version == request.Input.Version && request.Input.OS == a.OS && request.Input.Arch == a.Arch {
				matches = true
			}
		}
		if !matches || (request.Scope != "organization" && request.Scope != "developer" && request.Project != m.Project) {
			continue
		}
		var current model.SoftwareRequest
		if err := api.Call(ctx, http.MethodGet, "/api/v1/requests/"+request.ID, nil, &current); err != nil {
			fmt.Fprintln(a.Err, err)
			return 1
		}
		status := current.Status
		if status == "approved" {
			status = "approved, awaiting catalog publication (rerun setup to check current grants)"
		}
		fmt.Fprintf(a.Out, "%s %s: %s · %s %s\n", current.Input.Tool, current.Input.Version, status, current.ID, current.URL)
		found = true
	}
	if !found {
		fmt.Fprintln(a.Out, "No matching software requests.")
	}
	return 0
}
