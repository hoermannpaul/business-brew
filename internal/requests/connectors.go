package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"

	"business-brew/internal/model"
)

func (c *connector) Find(ctx context.Context, r model.SoftwareRequest) (*model.RemoteRequest, error) {
	if len(r.Key) > 256 || !safeID.MatchString(r.Key) {
		return nil, errors.New("invalid request key")
	}
	switch c.config.Kind {
	case "http":
		return c.findHTTP(ctx, r)
	case "linear":
		return c.findLinear(ctx, r)
	case "azure_devops":
		return c.findAzure(ctx, r)
	case "github":
		path := "/repos/" + repositoryPath(c.config.Settings["repository"]) + "/issues?state=all&per_page=100"
		return c.paginate(ctx, path, func(x any) ([]any, error) {
			a, ok := x.([]any)
			if !ok {
				return nil, errors.New("invalid GitHub issues response")
			}
			return a, nil
		}, func(x any) bool {
			return pathValue(x, "pull_request") == nil && exactMarker(str(pathValue(x, "body")), r.Key)
		}, func(x any) (model.RemoteRequest, error) { return remote(c, x, "number", "html_url", "state") }, func(_ any, h mapHeader) string { return linkNext(h.Get("Link")) })
	case "jira":
		q := url.Values{"serviceDeskId": {c.config.Settings["serviceDeskId"]}, "requestTypeId": {c.config.Settings["requestTypeId"]}, "requestOwnership": {"ALL_REQUESTS"}, "limit": {"100"}}
		return c.paginate(ctx, "/rest/servicedeskapi/request?"+q.Encode(), arrayAt("values"), func(x any) bool {
			fields, _ := pathValue(x, "requestFieldValues").([]any)
			for _, f := range fields {
				if str(pathValue(f, "fieldId")) == "description" && exactMarker(str(pathValue(f, "value")), r.Key) {
					return true
				}
			}
			return false
		}, func(x any) (model.RemoteRequest, error) {
			return remote(c, x, "issueKey", "_links.web", "currentStatus.status")
		}, func(x any, _ mapHeader) string { return str(pathValue(x, "_links.next")) })
	case "servicenow":
		query := c.config.Settings["keyField"] + "=" + r.Key + "^cat_item=" + c.config.Settings["catalogItemId"]
		path := "/api/now/table/sc_req_item?" + url.Values{"sysparm_query": {query}, "sysparm_limit": {"100"}, "sysparm_display_value": {"false"}}.Encode()
		return c.paginate(ctx, path, arrayAt("result"), func(x any) bool { return str(pathValue(x, c.config.Settings["keyField"])) == r.Key }, func(x any) (model.RemoteRequest, error) { return c.serviceNowRemote(x) }, func(_ any, h mapHeader) string { return linkNext(h.Get("Link")) })
	}
	return nil, errors.New("unsupported connector")
}

// mapHeader keeps pagination callbacks independent of transport implementation.
type mapHeader interface{ Get(string) string }

func arrayAt(path string) func(any) ([]any, error) {
	return func(x any) ([]any, error) {
		items, ok := pathValue(x, path).([]any)
		if !ok {
			return nil, errors.New("connector search response missing result list")
		}
		return items, nil
	}
}
func linkNext(h string) string {
	for _, part := range strings.Split(h, ",") {
		sections := strings.Split(part, ";")
		if len(sections) < 2 {
			continue
		}
		for _, s := range sections[1:] {
			if strings.TrimSpace(s) == `rel="next"` {
				return strings.Trim(strings.TrimSpace(sections[0]), "<>")
			}
		}
	}
	return ""
}
func (c *connector) paginate(ctx context.Context, path string, items func(any) ([]any, error), match func(any) bool, decode func(any) (model.RemoteRequest, error), next func(any, mapHeader) string) (*model.RemoteRequest, error) {
	seen := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		u, err := c.endpoint(path)
		if err != nil {
			return nil, err
		}
		if seen[u.String()] {
			return nil, errors.New("connector pagination loop")
		}
		seen[u.String()] = true
		result, headers, err := c.call(ctx, "GET", path, nil, nil)
		if err != nil {
			return nil, err
		}
		rows, err := items(result)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if match(row) {
				found, err := decode(row)
				if err != nil {
					return nil, err
				}
				return &found, nil
			}
		}
		path = next(result, headers)
		if path == "" {
			return nil, nil
		}
	}
	return nil, errors.New("connector pagination limit reached; search incomplete")
}
func repositoryPath(repository string) string {
	parts := strings.Split(repository, "/")
	return url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
}

func (c *connector) Create(ctx context.Context, r model.SoftwareRequest) (model.RemoteRequest, error) {
	if len(r.Key) > 256 || !safeID.MatchString(r.Key) {
		return model.RemoteRequest{}, errors.New("invalid request key")
	}
	switch c.config.Kind {
	case "http":
		return c.httpOperation(ctx, "create", r, "")
	case "github":
		x, _, err := c.call(ctx, "POST", "/repos/"+repositoryPath(c.config.Settings["repository"])+"/issues", map[string]any{"title": title(r), "body": description(r)}, nil)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		return remote(c, x, "number", "html_url", "state")
	case "jira":
		fields := map[string]any{"summary": title(r), "description": description(r)}
		if raw := c.config.Settings["requestFieldValues"]; raw != "" {
			extra, err := renderJSON(json.RawMessage(raw), values(r, ""))
			if err != nil {
				return model.RemoteRequest{}, err
			}
			m, ok := extra.(map[string]any)
			if !ok {
				return model.RemoteRequest{}, errors.New("requestFieldValues must be an object")
			}
			for key, value := range m {
				fields[key] = value
			}
			fields["description"] = description(r)
		}
		x, _, err := c.call(ctx, "POST", "/rest/servicedeskapi/request", map[string]any{"serviceDeskId": c.config.Settings["serviceDeskId"], "requestTypeId": c.config.Settings["requestTypeId"], "requestFieldValues": fields}, nil)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		return remote(c, x, "issueKey", "_links.web", "currentStatus.status")
	case "azure_devops":
		patch := []map[string]any{{"op": "add", "path": "/fields/System.Title", "value": title(r)}, {"op": "add", "path": "/fields/System.Description", "value": "<pre>" + html.EscapeString(description(r)) + "</pre>"}, {"op": "add", "path": "/fields/System.Tags", "value": marker(r.Key)}}
		path := "/" + url.PathEscape(c.config.Settings["project"]) + "/_apis/wit/workitems/$" + url.PathEscape(c.config.Settings["workItemType"]) + "?api-version=7.1"
		x, _, err := c.call(ctx, "POST", path, patch, map[string]string{"Content-Type": "application/json-patch+json"})
		if err != nil {
			return model.RemoteRequest{}, err
		}
		return remote(c, x, "id", "_links.html.href", "fields.System.State")
	case "linear":
		x, _, err := c.call(ctx, "POST", "/graphql", map[string]any{"query": `mutation Create($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id url state { name } } } }`, "variables": map[string]any{"input": map[string]any{"teamId": c.config.Settings["teamId"], "title": title(r), "description": description(r)}}}, nil)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		if pathValue(x, "data.issueCreate.success") != true {
			return model.RemoteRequest{}, errors.New("Linear did not confirm issue creation")
		}
		return remote(c, pathValue(x, "data.issueCreate.issue"), "id", "url", "state.name")
	case "servicenow":
		variables := map[string]any{}
		if raw := c.config.Settings["variables"]; raw != "" {
			rendered, err := renderJSON(json.RawMessage(raw), values(r, ""))
			if err != nil {
				return model.RemoteRequest{}, err
			}
			variables = rendered.(map[string]any)
		}
		variables[c.config.Settings["keyVariable"]] = r.Key
		x, _, err := c.call(ctx, "POST", "/api/sn_sc/servicecatalog/items/"+url.PathEscape(c.config.Settings["catalogItemId"])+"/order_now", map[string]any{"sysparm_quantity": "1", "variables": variables}, nil)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		requestID := str(pathValue(x, "result.request_id"))
		if !safeID.MatchString(requestID) {
			return model.RemoteRequest{}, errors.New("ServiceNow order response missing request_id; outcome may be unknown")
		}
		query := "request=" + requestID + "^cat_item=" + c.config.Settings["catalogItemId"]
		result, _, err := c.call(ctx, "GET", "/api/now/table/sc_req_item?"+url.Values{"sysparm_query": {query}, "sysparm_limit": {"2"}, "sysparm_display_value": {"false"}}.Encode(), nil, nil)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		rows, err := arrayAt("result")(result)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		if len(rows) != 1 {
			return model.RemoteRequest{}, errors.New("ServiceNow order requested item not uniquely resolved; outcome may be unknown")
		}
		if str(pathValue(rows[0], c.config.Settings["keyField"])) != r.Key {
			return model.RemoteRequest{}, errors.New("ServiceNow catalog key was not copied to requested item; outcome may be unknown")
		}
		return c.serviceNowRemote(rows[0])
	}
	return model.RemoteRequest{}, errors.New("unsupported connector")
}
func (c *connector) Read(ctx context.Context, id string) (model.RemoteRequest, error) {
	if id == "" {
		return model.RemoteRequest{}, errors.New("request ID is required")
	}
	if c.config.Kind == "http" {
		return c.httpOperation(ctx, "read", model.SoftwareRequest{}, id)
	}
	var path, idPath, urlPath, statusPath string
	switch c.config.Kind {
	case "github":
		path = "/repos/" + repositoryPath(c.config.Settings["repository"]) + "/issues/" + url.PathEscape(id)
		idPath = "number"
		urlPath = "html_url"
		statusPath = "state"
	case "jira":
		path = "/rest/servicedeskapi/request/" + url.PathEscape(id)
		idPath = "issueKey"
		urlPath = "_links.web"
		statusPath = "currentStatus.status"
	case "azure_devops":
		path = "/" + url.PathEscape(c.config.Settings["project"]) + "/_apis/wit/workitems/" + url.PathEscape(id) + "?api-version=7.1"
		idPath = "id"
		urlPath = "_links.html.href"
		statusPath = "fields.System.State"
	case "servicenow":
		x, _, err := c.call(ctx, "GET", "/api/now/table/sc_req_item/"+url.PathEscape(id)+"?sysparm_display_value=false", nil, nil)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		return c.serviceNowRemote(pathValue(x, "result"))
	case "linear":
		x, _, err := c.call(ctx, "POST", "/graphql", map[string]any{"query": `query Read($id: String!) { issue(id: $id) { id url state { name } } }`, "variables": map[string]any{"id": id}}, nil)
		if err != nil {
			return model.RemoteRequest{}, err
		}
		return remote(c, pathValue(x, "data.issue"), "id", "url", "state.name")
	}
	x, _, err := c.call(ctx, "GET", path, nil, nil)
	if err != nil {
		return model.RemoteRequest{}, err
	}
	return remote(c, x, idPath, urlPath, statusPath)
}
func (c *connector) serviceNowRemote(x any) (model.RemoteRequest, error) {
	result, err := remote(c, x, "sys_id", "", "approval")
	if err == nil {
		result.URL = c.base.String() + "/nav_to.do?uri=" + url.QueryEscape("sc_req_item.do?sys_id="+result.ID)
	}
	return result, err
}
func (c *connector) findAzure(ctx context.Context, r model.SoftwareRequest) (*model.RemoteRequest, error) {
	project := strings.ReplaceAll(c.config.Settings["project"], "'", "''")
	query := "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = '" + project + "' AND [System.Tags] CONTAINS '" + marker(r.Key) + "' ORDER BY [System.Id]"
	x, _, err := c.call(ctx, "POST", "/"+url.PathEscape(c.config.Settings["project"])+"/_apis/wit/wiql?$top=1000&api-version=7.1", map[string]any{"query": query}, nil)
	if err != nil {
		return nil, err
	}
	rows, err := arrayAt("workItems")(x)
	if err != nil {
		return nil, err
	}
	if len(rows) >= 1000 {
		return nil, errors.New("Azure search limit reached; search incomplete")
	}
	for _, row := range rows {
		id := str(pathValue(row, "id"))
		if id == "" {
			return nil, errors.New("Azure search result missing ID")
		}
		x, _, err := c.call(ctx, "GET", "/"+url.PathEscape(c.config.Settings["project"])+"/_apis/wit/workitems/"+url.PathEscape(id)+"?api-version=7.1", nil, nil)
		if err != nil {
			return nil, err
		}
		tags := str(pathValue(x, "fields.System.Tags"))
		for _, tag := range strings.Split(tags, ";") {
			if strings.TrimSpace(tag) == marker(r.Key) {
				result, err := remote(c, x, "id", "_links.html.href", "fields.System.State")
				return &result, err
			}
		}
	}
	return nil, nil
}
func (c *connector) findLinear(ctx context.Context, r model.SoftwareRequest) (*model.RemoteRequest, error) {
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		if seen[cursor] {
			return nil, errors.New("Linear pagination loop")
		}
		seen[cursor] = true
		x, _, err := c.call(ctx, "POST", "/graphql", map[string]any{"query": `query Find($team: ID!, $after: String) { issues(first: 100, after: $after, filter: {team: {id: {eq: $team}}}) { nodes { id url description state { name } } pageInfo { hasNextPage endCursor } } }`, "variables": map[string]any{"team": c.config.Settings["teamId"], "after": nullable(cursor)}}, nil)
		if err != nil {
			return nil, err
		}
		rows, err := arrayAt("data.issues.nodes")(x)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if exactMarker(str(pathValue(row, "description")), r.Key) {
				result, err := remote(c, row, "id", "url", "state.name")
				return &result, err
			}
		}
		more, ok := pathValue(x, "data.issues.pageInfo.hasNextPage").(bool)
		if !ok {
			return nil, errors.New("Linear pagination metadata missing")
		}
		if !more {
			return nil, nil
		}
		cursor = str(pathValue(x, "data.issues.pageInfo.endCursor"))
		if cursor == "" {
			return nil, errors.New("Linear next cursor missing")
		}
	}
	return nil, errors.New("Linear pagination limit reached; search incomplete")
}
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (c *connector) httpOperation(ctx context.Context, name string, r model.SoftwareRequest, id string) (model.RemoteRequest, error) {
	op := c.config.Operations[name]
	x, _, err := c.executeHTTP(ctx, op, r, id, "")
	if err != nil {
		return model.RemoteRequest{}, err
	}
	return remote(c, x, op.IDPath, op.URLPath, op.StatusPath)
}
func (c *connector) executeHTTP(ctx context.Context, op model.HTTPOperation, r model.SoftwareRequest, id, cursor string) (any, mapHeader, error) {
	v := values(r, id)
	path := replace(op.Path, v, url.PathEscape)
	// Query placeholders use query escaping rather than path escaping.
	if i := strings.Index(op.Path, "?"); i >= 0 {
		path = replace(op.Path[:i], v, url.PathEscape) + "?" + replace(op.Path[i+1:], v, url.QueryEscape)
	}
	if cursor != "" {
		u, err := url.Parse(path)
		if err != nil {
			return nil, nil, errors.New("invalid HTTP operation path")
		}
		q := u.Query()
		q.Set(op.CursorParameter, cursor)
		u.RawQuery = q.Encode()
		path = u.String()
	}
	body, err := renderJSON(op.Body, v)
	if err != nil {
		return nil, nil, err
	}
	return c.call(ctx, op.Method, path, body, op.Headers)
}
func (c *connector) findHTTP(ctx context.Context, r model.SoftwareRequest) (*model.RemoteRequest, error) {
	op := c.config.Operations["find"]
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < maxPages; page++ {
		if seen[cursor] {
			return nil, errors.New("HTTP pagination loop")
		}
		seen[cursor] = true
		x, _, err := c.executeHTTP(ctx, op, r, "", cursor)
		if err != nil {
			return nil, err
		}
		rows, err := arrayAt(op.ResultsPath)(x)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if str(pathValue(row, op.KeyPath)) == r.Key {
				result, err := remote(c, row, op.IDPath, op.URLPath, op.StatusPath)
				return &result, err
			}
		}
		if op.NextPath == "" {
			return nil, nil
		}
		cursor = str(pathValue(x, op.NextPath))
		if cursor == "" {
			return nil, nil
		}
		if op.CursorParameter == "" {
			return nil, fmt.Errorf("HTTP next_path requires cursor_parameter")
		}
	}
	return nil, errors.New("HTTP pagination limit reached; search incomplete")
}
