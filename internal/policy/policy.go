package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"business-brew/internal/model"
)

func Allowed(target model.Target, identity model.Identity, project string) bool {
	if identity.Organization == "" || identity.Developer == "" || (project != "" && !slices.Contains(identity.Projects, project)) {
		return false
	}
	for _, grant := range target.Grants {
		if grant.Organization != identity.Organization {
			continue
		}
		if grant.Project != "" && grant.Project != project {
			continue
		}
		if grant.Developer != "" && grant.Developer != identity.Developer {
			continue
		}
		return true
	}
	return false
}

func RequestVisible(request model.SoftwareRequest, identity model.Identity) bool {
	if request.Organization != identity.Organization {
		return false
	}
	switch request.Scope {
	case "organization":
		return true
	case "project":
		return slices.Contains(identity.Projects, request.Project)
	case "developer":
		return request.Developer == identity.Developer
	case "project_developer":
		return request.Developer == identity.Developer && slices.Contains(identity.Projects, request.Project)
	default:
		return false
	}
}

// Only selectors belonging to the configured request scope affect reuse.
func RequestKey(organization, scope string, input model.RequestInput, developer string) string {
	input.Reason = ""
	if input.Version != "" && input.Package != "" && input.PackageVersion != "" && input.Installer != "" && input.Source != "" {
		// Native bindings identify the request even before the catalog assigns
		// canonical display names to the software and upstream version.
		target := model.Target{Installer: input.Installer, PackageVersion: input.PackageVersion, PackageType: input.PackageType}
		input.Tool, input.Version = "", ""
		input.PackageVersion = target.NativeVersionIdentity()
		input.PackageType = target.NativePackageType()
	}
	if scope != "project" && scope != "project_developer" {
		input.Project = ""
	}
	if scope != "developer" && scope != "project_developer" {
		developer = ""
	}
	payload, _ := json.Marshal(struct {
		Organization string
		Scope        string
		Input        model.RequestInput
		Developer    string
	}{organization, scope, input, developer})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}
