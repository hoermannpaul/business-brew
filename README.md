# Business Brew

[Explore the landing page](https://hoermannpaul.github.io/business-brew/).

Self-hosted enterprise developer-tool management: a small CLI wraps existing
installers, checks a company catalog, and requests missing software or approvals
through the company's chosen request system.

## Developer workflow

After IT provisions the wrapper, developers use their normal installer commands:

```sh
brew install node@22 git
brew install --cask visual-studio-code
winget install --id OpenJS.NodeJS.LTS --version 22.14.0
sudo apt install nodejs
sudo dnf install git
```

Business Brew resolves the exact package, native version, source and architecture,
checks the published company catalog, and invokes the real installer when all
requested packages are authorized. Commands without a version use the native
installer's current candidate. Blocked commands create or reuse requests and print
their tracking links; developers rerun the same command after IT publishes approval.
No package in a multi-package command is installed if any requested package is blocked.
Read-only commands such as `brew list` pass through without contacting the service.

Neither a project file nor a profile flag is required for native commands. IT maps
repository roots to permitted project IDs in the managed profile. Outside those
roots, only organization or developer grants apply. Dependencies remain managed
by the native installer.

Native installer failures retain their exit codes and output. Wrapper exits are
`77` for blocked authorization/availability, `64` for unsupported commands,
`78` for invalid managed configuration, and `75` for a changed native candidate.
A successful native command that fails exact installed-version verification returns `1`.
Bulk upgrades, local package files, source/configuration overrides, and unknown
mutation flags are blocked. For apt upgrades, use `apt install --only-upgrade package`.
See [supported commands and resolver limits](internal/nativecmd/README.md).

### Optional project setup

A `business-brew.json` file can still provide explicit batch setup:

```json
{
  "schema_version": 1,
  "project": "billing",
  "tools": [{"tool": "node", "version": "22.14.0", "reason": "Project runtime"}]
}
```

IT provisions a managed profile and a developer credential. The profile supplies
the service URL, developer identity, permitted project IDs, installer preferences,
and either an environment variable or protected file holding the credential. See
[the example profile](examples/managed-profile.json).

```sh
business-brew plan --profile /path/to/company-profile.json
business-brew setup --profile /path/to/company-profile.json
business-brew status --profile /path/to/company-profile.json
```

Use `--file /path/to/business-brew.json` for another project file location. `plan`
does not install software or create tickets. `setup` processes every requirement,
installs approved available packages, and creates or reuses approval/provisioning
requests for blocked requirements. It checks the current published catalog again
before every installation. `status` reads request progress without installing.

Exit codes: `0` means the command completed; for `plan`/`setup`, all requirements
are ready/already installed or setup completed. `1` means incomplete setup or a
service/native-installer failure. `2` means invalid invocation/configuration.

Project files cannot supply credentials, approval rules, trusted identities, or
installer commands. The service binds each credential to its organization,
developer, and permitted projects; profile assertions must match that identity.
Device management owns distribution and protection of local profiles.

## Managed wrapper setup

IT deploys the executable and creates shims in a protected directory:

```sh
business-brew shims --directory /absolute/company/shims
```

This creates `brew`, `winget`, `apt`, `apt-get`, and `dnf` entry points (executable
copies on Windows, symlinks on macOS/Linux). Existing destinations are never
overwritten. Company provisioning places that directory before real installers
in PATH; Linux provisioning must also configure sudo's `secure_path` so elevated
commands reach the shim. The CLI does not edit shell profiles, PATH, sudo settings,
or replace installer binaries.

Profiles are discovered automatically:

| OS | Managed profile path |
| --- | --- |
| macOS | `/Library/Application Support/BusinessBrew/profiles/<login>.json` |
| Linux | `/etc/business-brew/profiles/<login>.json` |
| Windows | `%ProgramData%\BusinessBrew\profiles\<login>.json` |

An absolute `BUSINESS_BREW_PROFILE` override supports managed alternative layouts.
POSIX sudo uses the original `SUDO_USER` login. `native_binaries` pins absolute
paths to the real installers and their metadata helpers, preventing recursion.
Provision only the tools supported on that device: Homebrew; WinGet; apt/apt-get
plus apt-cache/dpkg-query; or dnf plus rpm. Native DNF repoquery support must exist.
See [macOS](examples/managed-profile.json), [Linux](examples/managed-profile-linux.json)
and [Windows](examples/managed-profile-windows.json) profile examples; replace all
paths and identities with device-specific values. The `installers` preferences
apply to optional project setup; native commands use the installer invoked.

For elevated Linux commands, a `credential_file` avoids relying on environment
variables retained by sudo. Use a regular file at an absolute path, readable by
the invoking account, with POSIX mode `0600` or stricter. Configure exactly one
of `credential_file` or `credential_env`. Device controls protect profiles,
credentials, shims and trusted installers. Direct installer paths remain available;
enforcement outside this wrapper belongs to existing device controls.

## Build and start locally

Requires Go 1.26 or later. The service uses `modernc.org/sqlite`; local-account
password hashing uses `golang.org/x/crypto/argon2`. The CLI has no runtime package
dependencies and does not bootstrap native installers.

```sh
mkdir -p bin
go build -o bin/business-brew ./cmd/business-brew
go build -o bin/business-brew-server ./cmd/business-brew-server
```

Create the first administrator explicitly. Supply a password of at least 12 bytes
through stdin, for example from a protected secret file. No default password or
public registration exists.

```sh
bin/business-brew-server bootstrap --organization acme --database data/catalog.db --username admin < /path/to/admin-password.txt
bin/business-brew-server serve --organization acme --database data/catalog.db --listen 127.0.0.1:8080 --allow-insecure-http
```

Open `http://127.0.0.1:8080/admin` for this loopback development setup. The insecure
mode is rejected for non-loopback listeners. For production, omit it and expose
the service through an HTTPS reverse proxy. Administrator cookies are secure,
HTTP-only, same-site cookies backed by expiring server sessions and CSRF tokens.
Local password reset invalidates existing sessions.

The public landing page is available at `http://127.0.0.1:8080/`, with developer
and management/IT perspectives and an illustrative terminal demo.

```sh
bin/business-brew-server add-admin --organization acme --database data/catalog.db --username another-admin < /path/to/new-password.txt
bin/business-brew-server reset-password --organization acme --database data/catalog.db --username admin < /path/to/new-password.txt
```

## Admin interface

1. Add/edit software using the catalog form: exact tool version, OS, architecture,
   installer, native package, native version when different, and approved source.
2. Enable grants and optionally restrict them to a project, developer, or both.
3. Configure a request connector and request scope in the advanced configuration
   editor. This editor also supports full catalog import/export; the draft version
   must match the version shown on the page.
4. Save the draft and explicitly publish it. Publication validates all targets and
   connectors and creates a complete immutable revision. Conflicting edits are
   rejected rather than overwriting another administrator's work.
5. Issue a credential in Developer access, copy it once, and distribute it through
   company provisioning. Credentials can be revoked independently. Keep the
   profile's project list in sync with its credential.

[The example catalog](examples/catalog-draft.json) is illustrative: update the
organization, request repository, draft version, and package mappings for your
environment. Example Homebrew historical versions may no longer be available;
the CLI will request provisioning rather than install a newer release.

Grants explicitly name the organization. Every restriction in one grant must
match; any matching grant allows installation. No grants means deny. A project
context must be permitted by both the managed profile and server-bound credential even
when the software grant is organization-wide. Removing grants or targets takes
effect for future wrapper installations after publication; it does not uninstall
software.

## Installers and exact versions

| Platform | Adapter | Approved source | Version behavior |
| --- | --- | --- | --- |
| macOS/Linux | `brew` | Existing tap such as `homebrew/core` | Verify formula stable version/revision and installed versions; casks supported on macOS |
| Windows | `winget` | Configured source name | Exact package ID, version, architecture; native scope selection |
| Debian/Ubuntu | `apt` | Configured repository HTTP(S) URL from `apt-cache madison` | Exact `package:architecture=version`; reject that version if another repository also provides it |
| Fedora/RHEL | `dnf` | Configured repository ID | Restrict enabled repository, match architecture and epoch/version/release |

Use `package_version` to map the canonical tool version to a native build,
distribution revision, RPM epoch, or composite Homebrew cask version. Requirements
are exact versions: ranges, `latest`, `stable`, and `head` are rejected. Sources
must already be configured. Adapter calls use structured arguments without a
shell, never add repositories, and verify the exact native version after install.

Homebrew does not preserve every historical version; an organization may maintain
its own tap. Apt/dnf operations require existing native privileges. The wrapper
does not invoke `sudo`, `runas`, or disable native integrity checks. WinGet respects
native package scope/privilege selection; noninteractive execution can fail when
agreements or privileges are missing. Installer failures remain visible, and
partial successful installations are reported. This wrapper does not isolate
different project runtimes or prevent native installers/applications from being
used or updated independently.

## Request integrations

Presets: Jira Service Management, ServiceNow Service Catalog, Azure DevOps,
GitHub, Linear. Custom HTTP profiles configure `find`, `create`, and `read`
operations with safe URL/JSON substitution, exact-key verification, and bounded
pagination. See [connector configuration and API references](internal/requests/README.md).

Secrets stay in server environment variables referenced by connector profiles.
The published developer catalog excludes connector configuration and credentials.
The configured default connector handles requests; the service selects one request
scope: `organization`, `project`, `developer`, or `project_developer`.

Outside mapped repositories, `outside_project_request_scope` selects `organization`
or `developer` (default). Project-scoped requests never silently broaden to another
scope; this fallback applies only when no managed project context exists.

Request keys include organization, exact software/platform/source, request type,
and the configured project/developer scope. Native requests bind the full installer
tuple, so changing a catalog display name does not create a second request.
Persistent reservations avoid duplicate
submission from concurrent CLI calls to this service. A lookup outage before any
submission is retryable. After an ambiguous submission, the service only searches
for the original key and does not blindly create another ticket. If no ticket can
be found, an operator must reconcile the outcome in the request system. Separate
service databases cannot guarantee atomic deduplication against an external API.

Status mapping is explicit. Unmapped provider statuses are `unknown`. No preset
assumes closed/done means approved. An approved ticket displays
`approved_awaiting_catalog` until IT publishes a grant. An approved provisioning
request with a catalog grant displays `approved_awaiting_availability`; rerunning
setup checks the actual installer. Ticket transitions never publish grants or
trigger remote installation.

ServiceNow requires the catalog workflow to propagate the request key variable to
the requested-item field configured in the connector. Provider credentials need
visibility into the relevant request collection for deduplication.

## Self-hosting

The supplied Dockerfile builds the service; Compose runs one instance with a
persistent local SQLite volume. Configure the organization and server-side
connector secrets in an untracked `.env` file based on `examples/env.example`.

```sh
cp examples/env.example .env
docker compose build
docker compose run --rm -T catalog bootstrap --organization acme --database /data/business-brew.db --username admin < /path/to/admin-password.txt
docker compose up -d
```

Set the bootstrap organization to the same value as `.env`. Route your HTTPS
reverse proxy to `127.0.0.1:8080`; Compose publishes only that host loopback port.
Use a persistent local filesystem volume, one service instance, and ingress login
rate limits. The service does not trust arbitrary forwarded client IP headers.
Back up the database consistently with SQLite's backup tooling or while the
service is stopped; copying only a live database file can omit its WAL.

The database includes developer/project identifiers, account hashes, token hashes,
catalog revisions, request records, and publication audit events. Company operators
own backup protection, retention, removal, and their existing request system's
privacy requirements. A database is bound to one organization and refuses startup
with another organization ID.

Organization ownership is explicit across persistence, credentials, request keys,
and authorization. This provides a foundation for a future hosted SaaS service;
multi-tenant onboarding, SaaS datastore migration, billing, and paid administrator
SSO are later product phases. The current release implements local accounts.

## API and verification

### Landing page publication

GitHub Pages hosts the public landing page at
[hoermannpaul.github.io/business-brew](https://hoermannpaul.github.io/business-brew/).
The publication workflow exports the same embedded template and landing assets
used by the Go service. Public management links point to this repository's
self-hosting instructions; administrator pages and APIs run only on your service.

To export the page locally:

```sh
go run ./cmd/business-brew-site --output dist --base-path /business-brew/ --repository-url https://github.com/hoermannpaul/business-brew
```

The `Publish landing page` workflow publishes changes to the landing source on
`main`, or can be run manually from GitHub Actions. It uploads only `dist` from a
fresh checkout. Use a dedicated, empty output directory for manual exports.

### Docker integration tests

With Docker running, execute the opt-in suite:

```sh
bash scripts/test-integration.sh
```

The first run downloads the service build images and a pinned Playwright browser
image/test package. The suite builds the production service image, bootstraps an
isolated test database, then checks the public landing page, assets, administrator
authentication, perspective switching and keyboard controls, animation controls,
reduced motion, mobile layout, and the GitHub Pages export under `/business-brew/`.
The browser shares the service's test network namespace so the loopback-only HTTP
development setting remains intact. No host ports, production `.env`, live ticket
providers, or native package installations are used.

Each run owns its Compose project and database volume and removes its containers,
volume, network, and per-run image tags on exit. Failure screenshots, traces,
JUnit results, and container logs are retained under the ignored
`test-results/integration/` directory. The manually triggered `Docker integration
tests` GitHub Actions workflow runs the same suite and uploads those artifacts.

### Service API verification

Developer bearer endpoints: `GET /api/v1/identity`, `GET /api/v1/catalog`,
`POST /api/v1/requests`, `GET /api/v1/requests`, and
`GET /api/v1/requests/{id}`. Creation accepts only software/platform/project/reason
and request type; organization/developer identity comes from the credential.
Listing does not refresh providers unless `?refresh=true`; reading an individual
request refreshes its provider status. Admin mutations require an admin session
and CSRF token, never a developer credential.

Focused tests use temporary databases, mock HTTP providers, and simulated native
commands. They cover policy combinations, credential boundaries, publication and
conflicting edits, request failure recovery, native source/version validation,
and CLI authorization refresh. Live provider and native OS checks are required
before an enterprise rollout. Container deployment requires its own verification.
