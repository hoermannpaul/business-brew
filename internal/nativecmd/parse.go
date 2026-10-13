// Package nativecmd validates native installer invocations and resolves their
// top-level packages using read-only queries. It never executes an installation.
package nativecmd

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var ErrUnsupported = errors.New("unsupported native installer invocation")
var ErrUnavailable = errors.New("requested exact native package version unavailable")

type Package struct {
	Name, Version, Source, PackageType, Arch string
}
type Invocation struct {
	Installer string
	Operation string
	Args      []string
	Packages  []Package
	ReadOnly  bool
}

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+@-]*$`)
var versionToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~:@,%-]*$`)
var nevra = regexp.MustCompile(`^(.+)-([0-9]+:[A-Za-z0-9._+~]+-[A-Za-z0-9._+~]+|[A-Za-z0-9._+~]+-[A-Za-z0-9._+~]+)\.(x86_64|aarch64|i686|noarch)$`)

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, args...))
}

// Parse preserves all original arguments. Unknown commands/flags fail closed,
// including package-manager plugins and commands with implicit bulk targets.
func Parse(name string, args []string) (Invocation, error) {
	name = strings.TrimSuffix(strings.ToLower(filepath.Base(name)), ".exe")
	if name == "apt-get" {
		name = "apt"
	}
	inv := Invocation{Installer: name, Args: append([]string(nil), args...)}
	if name != "brew" && name != "winget" && name != "apt" && name != "dnf" {
		return inv, unsupported("installer %q", name)
	}
	if len(args) == 0 {
		inv.ReadOnly = true
		return inv, nil
	}
	if len(args) == 1 && (args[0] == "--version" || args[0] == "--help" || args[0] == "-h") {
		inv.ReadOnly = true
		inv.Operation = args[0]
		return inv, nil
	}
	// Native global presentation flags may precede a command. Configuration and
	// plugin flags cannot: they would change metadata lookup or execute code.
	i := 0
	for i < len(args) && globalFlag(name, args[i]) {
		i++
	}
	if i == len(args) || (strings.HasPrefix(args[i], "-") && !readCommand(name, args[i])) {
		return inv, unsupported("missing command or unsupported global option")
	}
	inv.Operation = args[i]
	rest := args[i+1:]
	if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") && (readCommand(name, inv.Operation) || inv.Operation == "install" || inv.Operation == "upgrade" || inv.Operation == "reinstall") {
		inv.ReadOnly = true
		return inv, nil
	}
	if readCommand(name, inv.Operation) {
		if err := parseReadOnly(name, inv.Operation, rest); err != nil {
			return inv, err
		}
		inv.ReadOnly = true
		return inv, nil
	}
	if inv.Operation != "install" && inv.Operation != "upgrade" && inv.Operation != "reinstall" {
		return inv, unsupported("command %q", inv.Operation)
	}
	if name == "apt" && inv.Operation == "upgrade" {
		return inv, unsupported("apt upgrade can upgrade all packages; use install --only-upgrade with explicit packages")
	}
	if name == "winget" && inv.Operation == "reinstall" {
		return inv, unsupported("winget has no native reinstall command")
	}
	defaults := Package{}
	var names []string
	seen := map[string]bool{}
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if a == "--" {
			return inv, unsupported("explicit argument separators are not supported")
		}
		if !strings.HasPrefix(a, "-") {
			names = append(names, a)
			continue
		}
		flag, value, has := strings.Cut(a, "=")
		if harmlessFlag(name, flag) {
			if has {
				return inv, unsupported("unexpected value for %s", flag)
			}
			continue
		}
		if name == "brew" && (flag == "--formula" || flag == "--cask") {
			kind := strings.TrimPrefix(flag, "--")
			if has || defaults.PackageType != "" {
				return inv, unsupported("conflicting package type flags")
			}
			defaults.PackageType = kind
			continue
		}
		if name == "winget" && (flag == "--exact" || flag == "-e") {
			if has {
				return inv, unsupported("unexpected exact value")
			}
			seen["exact"] = true
			continue
		}
		if name == "winget" && (flag == "--id" || flag == "--version" || flag == "-v" || flag == "--source" || flag == "-s" || flag == "--architecture" || flag == "-a") {
			if !has {
				j++
				if j >= len(rest) {
					return inv, unsupported("missing %s value", flag)
				}
				value = rest[j]
			}
			key := flag
			switch flag {
			case "-v":
				key = "--version"
			case "-s":
				key = "--source"
			case "-a":
				key = "--architecture"
			}
			if seen[key] || !versionToken.MatchString(value) {
				return inv, unsupported("invalid or repeated %s", flag)
			}
			seen[key] = true
			switch key {
			case "--id":
				defaults.Name = value
			case "--version":
				defaults.Version = value
			case "--source":
				defaults.Source = value
			case "--architecture":
				defaults.Arch = value
			}
			continue
		}
		return inv, unsupported("option %q", a)
	}
	if name == "winget" {
		if defaults.Name == "" || len(names) != 0 {
			return inv, unsupported("winget requires --id ID and no positional query")
		}
		if !identifier.MatchString(defaults.Name) || (defaults.Source != "" && !identifier.MatchString(defaults.Source)) {
			return inv, unsupported("invalid winget ID/source")
		}
		inv.Packages = []Package{defaults}
		return inv, nil
	}
	if len(names) == 0 {
		return inv, unsupported("%s needs explicit package targets; bulk operations are blocked", inv.Operation)
	}
	for _, name := range names {
		p := defaults
		p.Name = name
		switch inv.Installer {
		case "brew":
			parts := strings.Split(name, "/")
			if len(parts) != 1 && len(parts) != 3 {
				return inv, unsupported("brew package must be a name or tap/name")
			}
			for _, part := range parts {
				if !identifier.MatchString(part) || part == "." || part == ".." {
					return inv, unsupported("invalid brew package %q", name)
				}
			}
			if len(parts) == 3 {
				p.Source = parts[0] + "/" + parts[1]
			}
		case "apt":
			pkg, v, exact := strings.Cut(name, "=")
			if exact {
				if !versionToken.MatchString(v) {
					return inv, unsupported("invalid apt version")
				}
				p.Version = v
			}
			base, a, qualified := strings.Cut(pkg, ":")
			p.Name = base
			if qualified {
				if !identifier.MatchString(a) {
					return inv, unsupported("invalid apt architecture")
				}
				p.Arch = a
			}
			if !identifier.MatchString(base) || strings.HasSuffix(base, ".deb") || strings.HasSuffix(base, "-") || strings.HasSuffix(base, "+") {
				return inv, unsupported("invalid apt package %q", name)
			}
		case "dnf":
			if m := nevra.FindStringSubmatch(name); m != nil {
				p.Name = m[1]
				p.Version = m[2]
				p.Arch = m[3]
			}
			if !identifier.MatchString(p.Name) || strings.HasSuffix(name, ".rpm") {
				return inv, unsupported("dnf requires a package name or full NEVRA")
			}
		}
		inv.Packages = append(inv.Packages, p)
	}
	return inv, nil
}

func globalFlag(name, flag string) bool {
	return (name == "apt" || name == "dnf") && (flag == "-y" || flag == "--assumeyes" || flag == "--assume-yes" || flag == "-q" || flag == "--quiet")
}
func harmlessFlag(name, flag string) bool {
	switch name {
	case "brew":
		return flag == "--quiet" || flag == "-q" || flag == "--verbose" || flag == "-v" || flag == "--debug"
	case "winget":
		return flag == "--silent" || flag == "--interactive" || flag == "--disable-interactivity" || flag == "--accept-package-agreements" || flag == "--accept-source-agreements"
	case "apt":
		return flag == "-y" || flag == "--assume-yes" || flag == "-q" || flag == "--quiet" || flag == "--no-install-recommends" || flag == "--reinstall" || flag == "--only-upgrade"
	case "dnf":
		return flag == "-y" || flag == "--assumeyes" || flag == "-q" || flag == "--quiet"
	}
	return false
}
func readCommand(name, command string) bool {
	var commands string
	switch name {
	case "brew":
		commands = " help info list ls search config --prefix --cellar --repository outdated deps uses "
	case "winget":
		commands = " help show list search "
	case "apt":
		commands = " help list search show policy "
	case "dnf":
		commands = " help list info search repolist repoquery "
	}
	return strings.Contains(commands, " "+command+" ")
}
func parseReadOnly(name, command string, args []string) error {
	// These options only affect the returned information. No shell, helper,
	// source/configuration override or mutation option is allowed through.
	allowed := map[string]string{
		"brew":   " --formula --cask --json --json=v1 --json=v2 --installed --versions --full-name --verbose -v --quiet -q --all --pinned --leaves --tree --recursive --include-build --include-test --include-optional --include-requirements ",
		"winget": " --exact -e --disable-interactivity --versions --accept-source-agreements ",
		"apt":    " --installed --upgradable --all-versions -a --names-only ",
		"dnf":    " --available --installed --all --duplicates --upgrades --userinstalled --verbose -v --quiet -q ",
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			if command == "help" && !readCommand(name, a) && a != "install" && a != "upgrade" && a != "reinstall" {
				return unsupported("help for unknown command %q", a)
			}
			if !identifier.MatchString(a) && !validBrewName(a) {
				return unsupported("invalid read-only argument %q", a)
			}
			continue
		}
		if strings.Contains(allowed[name], " "+a+" ") {
			continue
		}
		if name == "winget" && (a == "--id" || a == "--name" || a == "--source" || a == "--version" || a == "--architecture" || a == "--query") {
			i++
			if i < len(args) && versionToken.MatchString(args[i]) {
				continue
			}
		}
		return unsupported("option %q for %s", a, command)
	}
	return nil
}
func validBrewName(name string) bool {
	p := strings.Split(name, "/")
	return len(p) == 3 && identifier.MatchString(p[0]) && identifier.MatchString(p[1]) && identifier.MatchString(p[2])
}
