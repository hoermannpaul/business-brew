package model

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type Grant struct {
	Organization string `json:"organization"`
	Project      string `json:"project,omitempty"`
	Developer    string `json:"developer,omitempty"`
}

type Target struct {
	ID             string  `json:"id"`
	Tool           string  `json:"tool"`
	Version        string  `json:"version"`
	OS             string  `json:"os"`
	Arch           string  `json:"arch"`
	Installer      string  `json:"installer"`
	Package        string  `json:"package"`
	PackageVersion string  `json:"package_version,omitempty"`
	PackageType    string  `json:"package_type,omitempty"`
	Source         string  `json:"source"`
	Grants         []Grant `json:"grants"`
}

func (t Target) NativeVersion() string {
	if t.PackageVersion != "" {
		return t.PackageVersion
	}
	return t.Version
}

// NativeVersionIdentity treats an omitted RPM epoch as its explicit zero form.
// NativeVersion retains the original installer representation for execution.
func (t Target) NativeVersionIdentity() string {
	version := t.NativeVersion()
	if t.Installer == "dnf" {
		version = strings.TrimPrefix(strings.TrimPrefix(version, "0:"), "(none):")
	}
	return version
}

func (t Target) NativePackageType() string {
	if t.Installer == "brew" && t.PackageType == "" {
		return "formula"
	}
	return t.PackageType
}

type Catalog struct {
	SchemaVersion int       `json:"schema_version"`
	Revision      int64     `json:"revision"`
	PublishedAt   time.Time `json:"published_at"`
	Targets       []Target  `json:"targets"`
}

type HTTPOperation struct {
	Method          string            `json:"method"`
	Path            string            `json:"path"`
	Body            json.RawMessage   `json:"body,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	IDPath          string            `json:"id_path,omitempty"`
	URLPath         string            `json:"url_path,omitempty"`
	StatusPath      string            `json:"status_path,omitempty"`
	ResultsPath     string            `json:"results_path,omitempty"`
	KeyPath         string            `json:"key_path,omitempty"`
	NextPath        string            `json:"next_path,omitempty"`
	CursorParameter string            `json:"cursor_parameter,omitempty"`
}

type Connector struct {
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	BaseURL       string                   `json:"base_url"`
	TokenEnv      string                   `json:"token_env"`
	UsernameEnv   string                   `json:"username_env,omitempty"`
	Settings      map[string]string        `json:"settings,omitempty"`
	Operations    map[string]HTTPOperation `json:"operations,omitempty"`
	StatusMapping map[string]string        `json:"status_mapping,omitempty"`
}

type Draft struct {
	Version                    int64       `json:"version"`
	Targets                    []Target    `json:"targets"`
	Connectors                 []Connector `json:"connectors"`
	DefaultConnector           string      `json:"default_connector,omitempty"`
	RequestScope               string      `json:"request_scope"`
	OutsideProjectRequestScope string      `json:"outside_project_request_scope,omitempty"`
}

type Identity struct {
	Organization string   `json:"organization"`
	Developer    string   `json:"developer"`
	Projects     []string `json:"projects"`
}

type Profile struct {
	SchemaVersion  int                 `json:"schema_version"`
	ServiceURL     string              `json:"service_url"`
	Organization   string              `json:"organization"`
	Developer      string              `json:"developer"`
	Projects       []string            `json:"projects"`
	CredentialEnv  string              `json:"credential_env"`
	Installers     map[string][]string `json:"installers"`
	CredentialFile string              `json:"credential_file,omitempty"`
	NativeBinaries map[string]string   `json:"native_binaries,omitempty"`
	Repositories   []RepositoryContext `json:"repositories,omitempty"`
}

type RepositoryContext struct {
	Root    string `json:"root"`
	Project string `json:"project"`
}

type Requirement struct {
	Tool    string `json:"tool"`
	Version string `json:"version"`
	Reason  string `json:"reason,omitempty"`
}

type Manifest struct {
	SchemaVersion int           `json:"schema_version"`
	Project       string        `json:"project"`
	Tools         []Requirement `json:"tools"`
}

type RequestInput struct {
	RequestType    string `json:"request_type,omitempty"`
	Tool           string `json:"tool"`
	Version        string `json:"version"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	Installer      string `json:"installer,omitempty"`
	Source         string `json:"source,omitempty"`
	Package        string `json:"package,omitempty"`
	PackageVersion string `json:"package_version,omitempty"`
	PackageType    string `json:"package_type,omitempty"`
	Project        string `json:"project"`
	Reason         string `json:"reason,omitempty"`
}

type SoftwareRequest struct {
	ID           string       `json:"id"`
	Key          string       `json:"key"`
	Organization string       `json:"organization"`
	Developer    string       `json:"developer"`
	Project      string       `json:"project"`
	Scope        string       `json:"scope"`
	Input        RequestInput `json:"input"`
	ConnectorID  string       `json:"connector_id"`
	ExternalID   string       `json:"external_id,omitempty"`
	URL          string       `json:"url,omitempty"`
	Status       string       `json:"status"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

type RemoteRequest struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Status string `json:"status"`
}

// ConnectorClient creates and reads tickets; it never grants installation rights.
type ConnectorClient interface {
	Find(context.Context, SoftwareRequest) (*RemoteRequest, error)
	Create(context.Context, SoftwareRequest) (RemoteRequest, error)
	Read(context.Context, string) (RemoteRequest, error)
}

type ConnectorFactory func(Connector) (ConnectorClient, error)
