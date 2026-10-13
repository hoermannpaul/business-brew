// Package wrapper gates native installer commands without requiring a project
// manifest or replacing the native installer's interactive output.
package wrapper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"business-brew/internal/client"
	"business-brew/internal/config"
	"business-brew/internal/installers"
	"business-brew/internal/managed"
	"business-brew/internal/model"
	"business-brew/internal/nativecmd"
	"business-brew/internal/policy"
)

const ExitBlocked = 77
const ExitUnsupported = 64
const ExitConfiguration = 78
const ExitChanged = 75

type App struct {
	In       io.Reader
	Out, Err io.Writer
	HTTP     *http.Client
	// These seams support simulated native installers and protected test profiles.
	ProfilePath, OS, Arch, CWD string
	Getenv                     func(string) string
	Runner                     installers.Runner
	Execute                    func(context.Context, string, []string) (int, error)
	Verify                     func(context.Context, []model.Target) error
}

func IsInstaller(name string) bool {
	return slices.Contains([]string{"brew", "winget", "apt", "apt-get", "dnf"}, strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe"))
}

func (a App) Run(ctx context.Context, name string, args []string) int {
	if a.In == nil {
		a.In = os.Stdin
	}
	if a.Out == nil {
		a.Out = os.Stdout
	}
	if a.Err == nil {
		a.Err = os.Stderr
	}
	if a.Getenv == nil {
		a.Getenv = os.Getenv
	}
	if a.OS == "" {
		a.OS = runtime.GOOS
	}
	if a.Arch == "" {
		a.Arch = runtime.GOARCH
	}
	fail := func(code int, err error) int { fmt.Fprintln(a.Err, "Business Brew:", err); return code }
	if a.Getenv(managed.ActiveEnvironment) != "" {
		return fail(ExitConfiguration, errors.New("recursive wrapper invocation; configure trusted real installer paths"))
	}
	nativeName := strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe")
	inv, err := nativecmd.Parse(nativeName, args)
	if err != nil {
		return fail(ExitUnsupported, err)
	}
	path := a.ProfilePath
	if path == "" {
		path, err = managed.DefaultProfilePath(a.OS, a.Getenv)
		if err != nil {
			return fail(ExitConfiguration, err)
		}
	}
	profile, err := config.LoadProfile(path)
	if err != nil {
		return fail(ExitConfiguration, fmt.Errorf("load company-managed profile: %w", err))
	}
	trusted := managed.Runner{NativeBinaries: profile.NativeBinaries}
	if a.Runner == nil {
		a.Runner = trusted
	}
	if a.Execute == nil {
		a.Execute = func(ctx context.Context, name string, args []string) (int, error) {
			binary, err := trusted.Binary(name)
			if err != nil {
				return 1, err
			}
			command := exec.CommandContext(ctx, binary, args...)
			command.Env = trusted.Environment()
			command.Stdin = a.In
			command.Stdout = a.Out
			command.Stderr = a.Err
			err = command.Run()
			if err == nil {
				return 0, nil
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code := exit.ExitCode()
				if code < 0 {
					code = 130
				}
				return code, nil
			}
			return 1, fmt.Errorf("could not start native %s: %w", name, err)
		}
	}
	// Inventory and help remain usable while the approval service is unavailable.
	if inv.ReadOnly {
		code, err := a.Execute(ctx, nativeName, inv.Args)
		if err != nil {
			return fail(ExitConfiguration, err)
		}
		return code
	}
	credential, err := managed.Credential(profile, a.Getenv)
	if err != nil {
		return fail(ExitConfiguration, err)
	}
	api := client.New(profile.ServiceURL, credential, a.HTTP)
	var identity model.Identity
	if err := api.Call(ctx, http.MethodGet, "/api/v1/identity", nil, &identity); err != nil {
		return fail(ExitBlocked, err)
	}
	if !sameIdentity(profile, identity) {
		return fail(ExitConfiguration, errors.New("managed profile identity/projects do not match its developer credential"))
	}
	cwd := a.CWD
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return fail(ExitConfiguration, err)
		}
	}
	project, err := managed.Project(profile, cwd)
	if err != nil {
		return fail(ExitConfiguration, err)
	}
	var catalog model.Catalog
	if err := api.Call(ctx, http.MethodGet, "/api/v1/catalog", nil, &catalog); err != nil {
		return fail(ExitBlocked, err)
	}
	if catalog.SchemaVersion != 1 {
		return fail(ExitConfiguration, errors.New("unsupported published catalog schema"))
	}
	var nativeTargets []model.Target
	blocked := false
	for _, spec := range inv.Packages {
		one := inv
		one.Packages = []nativecmd.Package{spec}
		resolved, err := nativecmd.Resolve(ctx, one, a.Runner, a.OS, a.Arch)
		if err != nil {
			blocked = true
			fmt.Fprintf(a.Err, "Business Brew: %s: %v\n", spec.Name, err)
			if errors.Is(err, nativecmd.ErrUnavailable) {
				pkg := spec.Name
				source := spec.Source
				if inv.Installer == "brew" {
					parts := strings.Split(pkg, "/")
					if len(parts) == 3 {
						source = strings.Join(parts[:2], "/")
						pkg = parts[2]
					}
				}
				// No resolver result means no version/source may be assumed.
				input := model.RequestInput{RequestType: "provisioning", Tool: pkg, Version: spec.Version, OS: a.OS, Arch: a.Arch, Installer: inv.Installer, Source: source, Package: pkg, PackageVersion: spec.Version, PackageType: spec.PackageType, Project: project, Reason: "Requested native package/version is unavailable"}
				a.request(ctx, api, input)
			}
			continue
		}
		if len(resolved) != 1 {
			blocked = true
			fmt.Fprintln(a.Err, "Business Brew: native resolver returned ambiguous package targets")
			continue
		}
		actual := resolved[0]
		nativeTargets = append(nativeTargets, actual)
		approved, found, err := catalogTarget(catalog, actual)
		if err != nil {
			blocked = true
			fmt.Fprintln(a.Err, "Business Brew:", err)
			continue
		}
		if found && policy.Allowed(approved, identity, project) {
			continue
		}
		blocked = true
		if !found {
			approved = actual
		}
		a.request(ctx, api, model.RequestInput{RequestType: "approval", Tool: approved.Tool, Version: approved.Version, OS: a.OS, Arch: a.Arch, Installer: actual.Installer, Source: actual.Source, Package: actual.Package, PackageVersion: actual.NativeVersion(), PackageType: actual.PackageType, Project: project, Reason: "Requested by native " + nativeName + " " + inv.Operation})
	}
	if blocked {
		fmt.Fprintln(a.Err, "Business Brew: installation stopped; no packages in this command were installed. Rerun the same command after approval or provisioning.")
		return ExitBlocked
	}
	pinned, err := nativecmd.PinnedArgs(inv, nativeTargets)
	if err != nil {
		return fail(ExitUnsupported, err)
	}
	// A second native lookup detects candidate/source changes during preflight.
	latestNative, err := nativecmd.Resolve(ctx, inv, a.Runner, a.OS, a.Arch)
	if err != nil {
		return fail(ExitChanged, err)
	}
	if len(latestNative) != len(nativeTargets) {
		return fail(ExitChanged, errors.New("native targets changed; rerun the command"))
	}
	for i, target := range latestNative {
		if !sameNative(target, nativeTargets[i]) {
			return fail(ExitChanged, errors.New("native candidate changed; rerun the command"))
		}
	}
	var latest model.Catalog
	if err := api.Call(ctx, http.MethodGet, "/api/v1/catalog", nil, &latest); err != nil {
		return fail(ExitBlocked, err)
	}
	for _, actual := range nativeTargets {
		target, found, err := catalogTarget(latest, actual)
		if err != nil || !found || !policy.Allowed(target, identity, project) {
			return fail(ExitBlocked, errors.New("catalog authorization changed; rerun the command"))
		}
	}
	code, err := a.Execute(ctx, nativeName, pinned)
	if err != nil {
		return fail(ExitConfiguration, err)
	}
	if code != 0 {
		return code
	}
	if a.Verify == nil {
		a.Verify = func(ctx context.Context, targets []model.Target) error {
			for _, target := range targets {
				state, err := (installers.Native{Runner: a.Runner}).Check(ctx, target)
				if err != nil {
					return err
				}
				if !state.Installed {
					return fmt.Errorf("native %s finished without verifying exact %s %s from %s", target.Installer, target.Package, target.NativeVersion(), target.Source)
				}
			}
			return nil
		}
	}
	if err := a.Verify(ctx, nativeTargets); err != nil {
		return fail(1, err)
	}
	return 0
}

func (a App) request(ctx context.Context, api client.Client, input model.RequestInput) {
	var request model.SoftwareRequest
	if err := api.Call(ctx, http.MethodPost, "/api/v1/requests", input, &request); err != nil {
		fmt.Fprintf(a.Err, "Business Brew: could not create/reuse request for %s: %v\n", input.Tool, err)
		return
	}
	if request.Status == "installable" {
		fmt.Fprintf(a.Err, "Business Brew: approval for %s is now available; rerun the same command.\n", input.Tool)
		return
	}
	fmt.Fprintf(a.Err, "Business Brew: %s %s needs %s; request %s [%s] %s\n", input.Tool, input.Version, input.RequestType, request.ID, request.Status, request.URL)
}

func sameIdentity(profile model.Profile, identity model.Identity) bool {
	if profile.Organization != identity.Organization || profile.Developer != identity.Developer || len(profile.Projects) != len(identity.Projects) {
		return false
	}
	for _, project := range profile.Projects {
		if !slices.Contains(identity.Projects, project) {
			return false
		}
	}
	return true
}

func sameNative(a, b model.Target) bool {
	return a.OS == b.OS && a.Arch == b.Arch && a.Installer == b.Installer && a.Package == b.Package && a.NativeVersionIdentity() == b.NativeVersionIdentity() && a.Source == b.Source && a.NativePackageType() == b.NativePackageType()
}
func catalogTarget(catalog model.Catalog, actual model.Target) (model.Target, bool, error) {
	if catalog.SchemaVersion != 1 {
		return model.Target{}, false, errors.New("unsupported catalog schema")
	}
	var matches []model.Target
	for _, target := range catalog.Targets {
		if sameNative(target, actual) {
			matches = append(matches, target)
		}
	}
	if len(matches) > 1 {
		return model.Target{}, false, fmt.Errorf("multiple catalog tools map to native %s %s; ask IT to correct the mapping", actual.Package, actual.NativeVersion())
	}
	if len(matches) == 1 {
		return matches[0], true, nil
	}
	return model.Target{}, false, nil
}
