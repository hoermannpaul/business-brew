package requests

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"business-brew/internal/model"
)

const maxPages = 100
const maxResponse = 4 << 20

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var placeholder = regexp.MustCompile(`\{\{([a-z_]+)\}\}`)

type connector struct {
	config model.Connector
	base   *url.URL
	client *http.Client
}

// NewConnector builds a company-configured connector. Secrets are resolved only at request time.
func NewConnector(config model.Connector) (model.ConnectorClient, error) {
	if err := ValidateConnector(config); err != nil {
		return nil, err
	}
	base, _ := url.Parse(strings.TrimRight(config.BaseURL, "/"))
	return &connector{config: config, base: base, client: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("connector redirects are disabled") }}}, nil
}

func ValidateConnector(config model.Connector) error {
	if strings.TrimSpace(config.ID) == "" {
		return errors.New("connector ID is required")
	}
	base, err := url.Parse(config.BaseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("connector base_url must be a plain HTTPS URL")
	}
	local := base.Hostname() == "localhost"
	if ip := net.ParseIP(base.Hostname()); ip != nil {
		local = ip.IsLoopback()
	}
	if base.Scheme != "https" && !(base.Scheme == "http" && local) {
		return errors.New("connector requires HTTPS except loopback development URLs")
	}
	if !envName.MatchString(config.TokenEnv) {
		return errors.New("connector token_env must name an environment variable")
	}
	if config.UsernameEnv != "" && !envName.MatchString(config.UsernameEnv) {
		return errors.New("invalid username_env")
	}
	for _, decision := range config.StatusMapping {
		if decision != "pending" && decision != "approved" && decision != "rejected" && decision != "unknown" {
			return errors.New("status mappings must be pending, approved, rejected, or unknown")
		}
	}
	required := map[string][]string{"jira": {"serviceDeskId", "requestTypeId"}, "servicenow": {"catalogItemId", "keyField", "keyVariable"}, "azure_devops": {"project", "workItemType"}, "github": {"repository"}, "linear": {"teamId"}, "http": {}}
	keys, ok := required[config.Kind]
	if !ok {
		return errors.New("unsupported connector kind")
	}
	for _, key := range keys {
		if strings.TrimSpace(config.Settings[key]) == "" {
			return fmt.Errorf("connector setting %s is required", key)
		}
	}
	if config.Kind == "github" {
		parts := strings.Split(config.Settings["repository"], "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return errors.New("repository must be owner/name")
		}
	}
	if config.Kind == "servicenow" {
		if !safeID.MatchString(config.Settings["catalogItemId"]) {
			return errors.New("invalid catalogItemId")
		}
		for _, key := range []string{"keyField", "keyVariable"} {
			if !fieldName.MatchString(config.Settings[key]) {
				return fmt.Errorf("invalid %s", key)
			}
		}
		if v := config.Settings["variables"]; v != "" {
			var values map[string]any
			if json.Unmarshal([]byte(v), &values) != nil || values == nil {
				return errors.New("variables setting must be a JSON object")
			}
			if err := validateTemplate(v); err != nil {
				return err
			}
		}
	}
	if config.Kind == "jira" {
		if v := config.Settings["requestFieldValues"]; v != "" {
			var fields map[string]any
			if json.Unmarshal([]byte(v), &fields) != nil || fields == nil {
				return errors.New("requestFieldValues setting must be a JSON object")
			}
			if err := validateTemplate(v); err != nil {
				return err
			}
		}
	}
	if config.Kind == "http" {
		for _, name := range []string{"find", "create", "read"} {
			op, ok := config.Operations[name]
			if !ok {
				return fmt.Errorf("HTTP %s operation is required", name)
			}
			if op.Method != "GET" && op.Method != "POST" && op.Method != "PUT" {
				return fmt.Errorf("invalid %s method", name)
			}
			if name == "read" && op.Method != "GET" {
				return errors.New("read must use GET")
			}
			if name == "create" && op.Method == "GET" {
				return errors.New("create must use POST or PUT")
			}
			if !strings.HasPrefix(op.Path, "/") || strings.HasPrefix(op.Path, "//") {
				return errors.New("HTTP paths must be relative to base_url")
			}
			if op.IDPath == "" {
				return errors.New("HTTP operations require id_path")
			}
			if name == "find" && op.KeyPath == "" {
				return errors.New("HTTP find requires key_path to verify exact request keys")
			}
			if op.NextPath != "" && op.CursorParameter == "" {
				return errors.New("HTTP next_path requires cursor_parameter")
			}
			if len(op.Body) > 0 && !json.Valid(op.Body) {
				return errors.New("HTTP operation body must be valid JSON")
			}
			if err := validateTemplate(op.Path); err != nil {
				return err
			}
			if err := validateTemplate(string(op.Body)); err != nil {
				return err
			}
			for key, value := range op.Headers {
				switch strings.ToLower(key) {
				case "authorization", "cookie", "host", "proxy-authorization":
					return errors.New("reserved HTTP operation header")
				}
				if strings.ContainsAny(key+value, "\r\n") {
					return errors.New("invalid header")
				}
			}
		}
	}
	return nil
}

func validateTemplate(s string) error {
	allowed := map[string]bool{"key": true, "id": true, "tool": true, "version": true, "organization": true, "developer": true, "project": true, "scope": true, "os": true, "arch": true, "installer": true, "source": true, "reason": true, "request_type": true, "marker": true, "title": true, "description": true}
	for _, m := range placeholder.FindAllStringSubmatch(s, -1) {
		if !allowed[m[1]] {
			return fmt.Errorf("unknown placeholder %s", m[1])
		}
	}
	if strings.Contains(placeholder.ReplaceAllString(s, ""), "{{") {
		return errors.New("malformed placeholder")
	}
	return nil
}
func values(r model.SoftwareRequest, id string) map[string]string {
	return map[string]string{"key": r.Key, "id": id, "tool": r.Input.Tool, "version": r.Input.Version, "organization": r.Organization, "developer": r.Developer, "project": r.Project, "scope": r.Scope, "os": r.Input.OS, "arch": r.Input.Arch, "installer": r.Input.Installer, "source": r.Input.Source, "reason": r.Input.Reason, "request_type": r.Input.RequestType, "marker": marker(r.Key), "title": title(r), "description": description(r)}
}
func marker(key string) string { return "business-brew-key:" + key }
func title(r model.SoftwareRequest) string {
	return "Software request: " + r.Input.Tool + " " + r.Input.Version
}
func description(r model.SoftwareRequest) string {
	payload, _ := json.MarshalIndent(r.Input, "", "  ")
	return marker(r.Key) + "\nOrganization: " + r.Organization + "\nDeveloper: " + r.Developer + "\nProject: " + r.Project + "\nScope: " + r.Scope + "\n" + string(payload)
}
func exactMarker(text, key string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == marker(key) {
			return true
		}
	}
	return false
}
func replace(s string, v map[string]string, escape func(string) string) string {
	return placeholder.ReplaceAllStringFunc(s, func(p string) string { return escape(v[placeholder.FindStringSubmatch(p)[1]]) })
}
func renderJSON(raw json.RawMessage, v map[string]string) (any, error) {
	var result any
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, errors.New("invalid configured JSON")
	}
	var walk func(any) any
	walk = func(x any) any {
		switch n := x.(type) {
		case string:
			return replace(n, v, func(s string) string { return s })
		case []any:
			for i := range n {
				n[i] = walk(n[i])
			}
		case map[string]any:
			for k, e := range n {
				n[k] = walk(e)
			}
		}
		return x
	}
	return walk(result), nil
}
func pathValue(x any, path string) any {
	if path == "" {
		return x
	}
	parts := strings.Split(path, ".")
	for i, part := range parts {
		m, ok := x.(map[string]any)
		if !ok {
			return nil
		}
		// Azure work-item field names contain literal dots.
		if value, exists := m[strings.Join(parts[i:], ".")]; exists {
			return value
		}
		x = m[part]
	}
	return x
}
func str(x any) string {
	switch v := x.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return fmt.Sprintf("%.0f", v)
	}
	return ""
}
func (c *connector) status(raw string) string {
	if s, ok := c.config.StatusMapping[raw]; ok {
		return s
	}
	return "unknown"
}

func (c *connector) endpoint(path string) (*url.URL, error) {
	u, err := url.Parse(path)
	if err != nil {
		return nil, errors.New("invalid connector URL")
	}
	if u.IsAbs() {
		if u.Scheme != c.base.Scheme || u.Host != c.base.Host || u.User != nil {
			return nil, errors.New("connector URL changed origin")
		}
		return u, nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, errors.New("invalid connector path")
	}
	u, err = url.Parse(c.base.String() + path)
	if err != nil || u.Host != c.base.Host || u.User != nil {
		return nil, errors.New("invalid connector URL")
	}
	return u, nil
}
func (c *connector) call(ctx context.Context, method, path string, body any, headers map[string]string) (any, http.Header, error) {
	u, err := c.endpoint(path)
	if err != nil {
		return nil, nil, err
	}
	var encoded []byte
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return nil, nil, errors.New("cannot encode connector request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, nil, errors.New("cannot build connector request")
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	token, ok := os.LookupEnv(c.config.TokenEnv)
	if !ok || strings.TrimSpace(token) == "" {
		return nil, nil, errors.New("connector credential is unavailable")
	}
	if c.config.UsernameEnv != "" {
		username, ok := os.LookupEnv(c.config.UsernameEnv)
		if !ok || username == "" {
			return nil, nil, errors.New("connector username is unavailable")
		}
		req.SetBasicAuth(username, token)
	} else {
		switch c.config.Kind {
		case "linear":
			req.Header.Set("Authorization", token)
		case "azure_devops":
			req.SetBasicAuth("", token)
		default:
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, nil, errors.New("connector transport failed; submission outcome may be unknown")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("connector returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil || len(data) > maxResponse {
		return nil, nil, errors.New("connector response exceeds limits or could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var result any
	if decoder.Decode(&result) != nil {
		return nil, nil, errors.New("invalid connector JSON response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, nil, errors.New("invalid trailing connector response")
	}
	if problems := pathValue(result, "errors"); problems != nil {
		list, ok := problems.([]any)
		if !ok || len(list) > 0 {
			return nil, nil, errors.New("connector GraphQL returned errors")
		}
	}
	return result, response.Header, nil
}
func remote(c *connector, x any, idPath, urlPath, statusPath string) (model.RemoteRequest, error) {
	id := str(pathValue(x, idPath))
	if id == "" {
		return model.RemoteRequest{}, errors.New("connector response missing request ID")
	}
	u := str(pathValue(x, urlPath))
	if u != "" {
		parsed, err := url.Parse(u)
		if err != nil || parsed.User != nil || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && parsed.Host == c.base.Host)) {
			return model.RemoteRequest{}, errors.New("invalid connector request link")
		}
	}
	return model.RemoteRequest{ID: id, URL: u, Status: c.status(str(pathValue(x, statusPath)))}, nil
}
