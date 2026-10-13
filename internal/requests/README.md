# Request connector profiles

Connectors run in the catalog service. `token_env` names an operator-provided
secret environment variable. `username_env`, when present, selects HTTP Basic
authentication with that username and the token as password. Otherwise GitHub,
Jira, ServiceNow and HTTP use Bearer authentication, Azure DevOps uses Basic
with a PAT and empty username, and Linear uses a personal API key directly in
Authorization. For Jira API tokens, set `username_env` to the account email.

`base_url` is a company-controlled HTTPS API base; loopback HTTP is supported
for local testing. Prefix paths are preserved, so Azure DevOps normally uses
`https://dev.azure.com/organization` and GitHub Enterprise may use
`https://host/api/v3`. Linear uses `https://api.linear.app`. Redirects are
rejected, including same-origin redirects: configure the canonical API URL.
Pagination URLs must stay on that same origin. Responses are limited to 4 MiB,
requests time out after 20 seconds, and searches stop with an error after 100
pages or a repeated cursor. A failed or malformed search is never interpreted
as absence of an existing request.

The company must configure `status_mapping`, mapping exact provider status
strings to `pending`, `approved`, `rejected` or `unknown`. Every unmapped status
is `unknown`. No preset treats closed, done or resolved as approval. Ticket
approval is informational; installation still requires a published catalog
grant.

## Preset settings

| Kind | Required `settings` | Optional `settings` |
| --- | --- | --- |
| `jira` | `serviceDeskId`, `requestTypeId` | `requestFieldValues`: JSON object for additional required request fields |
| `servicenow` | `catalogItemId`, `keyField`, `keyVariable` | `variables`: JSON object for catalog item variables |
| `azure_devops` | `project`, `workItemType` | — |
| `github` | `repository` (`owner/name`) | — |
| `linear` | `teamId` | — |

Jira uses the Service Management customer-request API, including request field
values and current request status. The credential needs visibility into all
requests in the configured service desk/type to recover previous submissions.
A restricted credential can only deduplicate requests it can see.

ServiceNow submits a catalog item through `order_now`, then looks up the
requested item (`sc_req_item`) by the returned request ID and configured catalog
item. **Configure the catalog item workflow to copy the `keyVariable` value into
the `keyField` column on its requested item.** The service identity needs catalog
order permission and read access to that column and table. Creation succeeds
only when one matching requested item exists and its key is verified. If item
creation is asynchronous or the workflow does not copy the key, creation reports
an uncertain outcome; later reconciliation searches the key rather than blindly
placing another order. Configure `variables` for the catalog item's required
fields, such as `{"software":"{{tool}}","version":"{{version}}","description":"{{description}}"}`.
Status mappings use the requested item's `approval` field with display values
disabled (normally provider values such as `requested`, `approved`, `rejected`).

Azure DevOps creates a JSON-patch work item and stores the deduplication marker
in `System.Tags`. Queries are scoped to the configured project. GitHub excludes
pull requests and compares the complete marker line in issue bodies. Linear
queries team issues with bounded cursor pagination and checks GraphQL errors
and mutation success independently of HTTP status.

## Custom HTTP operations

`operations` must define `find`, `create` and `read` with the shared structure:

```json
{
  "find": {
    "method": "GET",
    "path": "/requests?key={{key}}",
    "results_path": "items",
    "key_path": "key",
    "id_path": "id",
    "url_path": "url",
    "status_path": "state",
    "next_path": "next_cursor",
    "cursor_parameter": "cursor"
  },
  "create": {
    "method": "POST",
    "path": "/requests",
    "body": {"key": "{{key}}", "title": "{{title}}", "description": "{{description}}"},
    "id_path": "id",
    "url_path": "url",
    "status_path": "state"
  },
  "read": {
    "method": "GET",
    "path": "/requests/{{id}}",
    "id_path": "id",
    "url_path": "url",
    "status_path": "state"
  }
}
```

Paths are relative to the base URL. Dot-separated response paths access object
keys; an empty results path means the response itself is an array. `find`
compares `key_path` exactly before accepting a candidate; a query filter alone
is insufficient. Cursor pagination uses the configured next-cursor response
path and query parameter. Omit both pagination fields for a single page.

Supported placeholders: `key`, `id`, `tool`, `version`, `organization`,
`developer`, `project`, `scope`, `os`, `arch`, `installer`, `source`, `reason`, `request_type`,
`marker`, `title`, and `description`. JSON placeholders substitute within
string values after parsing, preserving JSON structure. URL path and query
placeholders receive distinct URL escaping. `headers` are literal values;
authorization, cookies, host and proxy authorization overrides are forbidden.
No template evaluation, command execution, or credential interpolation occurs.

Missing remote IDs, invalid JSON, HTTP failures and GraphQL errors return errors.
Remote response bodies and secrets are excluded from errors. Mutating requests
are not automatically retried; the service must reconcile ambiguous outcomes
using `Find` before submitting again.

## Provider references

- [Jira Service Management requests](https://developer.atlassian.com/cloud/jira/service-desk/rest/api-group-request/)
- [ServiceNow Service Catalog API](https://www.servicenow.com/docs/r/api-reference/rest-apis/c_ServiceCatalogAPI.html)
- [Azure DevOps work-item creation](https://learn.microsoft.com/en-us/rest/api/azure/devops/wit/work-items/create?view=azure-devops-rest-7.1)
- [GitHub issues API](https://docs.github.com/en/rest/issues/issues)
- [Linear GraphQL API](https://linear.app/developers/graphql)
- [Linear pagination](https://linear.app/developers/pagination)
