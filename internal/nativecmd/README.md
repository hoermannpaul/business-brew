# Native command boundary

`Parse` accepts only explicit top-level native package operations or allowlisted
read-only commands. `Resolve` performs metadata queries through a caller-supplied
trusted runner. `PinnedArgs` binds execution to the resolved package, version, and
source while retaining supported interaction flags. None of these functions
executes a package installation.

| Installer | Supported mutation grammar | Native candidate query |
| --- | --- | --- |
| Homebrew | `install`, `upgrade`, `reinstall` plus one or more names, `name@major` selectors, or `owner/tap/name`; optional `--formula`/`--cask` | `info --json=v2`, with formula/cask autodetection and canonical tap/name metadata |
| WinGet | `install` or `upgrade --id ID`, optional `--version`, `--source`, `--architecture`; one ID per command | Exact `search` verifies configured source identity; exact `show` verifies ID/version and selects the host architecture |
| apt/apt-get | `install` or `reinstall` plus names or `name[:arch]=version`; `install --only-upgrade` for explicit upgrades | `apt-cache policy` selects the configured candidate; `madison` verifies exact version and repository URI |
| DNF | `install`, `upgrade`, `reinstall` plus names or full `name-[epoch:]version-release.arch` NEVRA | `repoquery --latest-limit=1` uses native RPM ordering and returns exact EVR/architecture/repository |

Ordinary commands preserve presentation/interaction flags from a small allowlist.
Homebrew allows quiet/verbose/debug; WinGet allows silent/interactive, agreement
acceptance and disabling interaction; apt allows yes/quiet, reinstall,
only-upgrade, and no-install-recommends; DNF allows yes/quiet. Unsupported flags
are rejected rather than forwarded. Architecture overrides must match the host
(DNF `noarch` is also compatible).

Homebrew operands become canonical `tap/name`; brew cannot pin an arbitrary
historical version on its command line, so the caller must recheck metadata and
disable auto-updates before execution. Native Homebrew revisions are retained in
`PackageVersion`; casks with `latest` instead of an exact version are blocked.
WinGet execution receives explicit exact ID/version/source/architecture, including
when the original command omitted them. The text metadata parser requires English
`Found`, `Version`, and optional `Id`/`Source` labels; unrecognized/localized output
fails closed. Sources without exact ID/version metadata are unsupported.

Apt execution receives `package:hostarch=version`. An identical version from more
than one repository URI is ambiguous and blocked. Apt/apt-get `upgrade` and
`full-upgrade` are bulk operations and blocked even if operands were supplied;
use `install --only-upgrade package` instead. Package suffixes `+` and `-`, release
selectors, and local `.deb` files are unsupported.

DNF execution receives exact name-EVR operands, host `--forcearch`, and only the
resolved repositories. The architecture suffix is omitted so a unique `noarch`
candidate remains installable. Multiple candidate repositories or host/noarch
variants are blocked. Native repoquery support must already exist; the wrapper
does not install plugins or use an approximate version-sort fallback.

Read-only command allowlists include help, search, info/show, list, and basic
package-manager diagnostics. The native caller must still use trusted executable
paths and environment. Unknown commands, configuration/source overrides, custom
manifests/install arguments, local package paths, plugins, module/group installs,
and implicit bulk upgrades fail closed. Dependencies are managed by the native
installer; they are not separately evaluated as top-level approval requests.

`ErrUnavailable` means a definitive absent exact candidate. Network failures,
unrecognized metadata, repository ambiguity and missing executables remain
ordinary errors: callers must not treat them as permission to install or as a
definitive packaging request. Queries use structured arguments, never a shell.

Native query references:

- [Homebrew JSON querying](https://docs.brew.sh/Querying-Brew)
- [Homebrew command reference](https://docs.brew.sh/Manpage)
- [WinGet show](https://learn.microsoft.com/en-us/windows/package-manager/winget/show)
- [WinGet HRESULT meanings](https://github.com/microsoft/winget-cli/blob/master/doc/windows/package-manager/winget/returnCodes.md)
- [apt-cache policy and madison](https://manpages.debian.org/trixie/apt/apt-cache.8.en.html)
- [apt-get exact installation grammar](https://manpages.debian.org/bookworm/apt/apt-get.8)
- [DNF repoquery and repository options](https://dnf.readthedocs.io/en/stable/command_ref.html)

Tests use simulated query output and never execute native installations. Native
platform verification is still required before rollout, especially WinGet output
formats, apt cache freshness, and DNF versions/plugin availability.
