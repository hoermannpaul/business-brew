package nativecmd

import (
	"fmt"
	"strings"

	"business-brew/internal/installers"
	"business-brew/internal/model"
)

// PinnedArgs retains supported presentation/interaction flags and binds native
// operands to the preflighted identities and versions. The caller must refresh
// policy immediately before invoking the trusted installer with these arguments.
func PinnedArgs(inv Invocation, targets []model.Target) ([]string, error) {
	if inv.ReadOnly {
		return append([]string(nil), inv.Args...), nil
	}
	if len(targets) != len(inv.Packages) || len(targets) == 0 {
		return nil, fmt.Errorf("resolved target count differs from invocation")
	}
	for _, t := range targets {
		if t.Installer != inv.Installer {
			return nil, fmt.Errorf("resolved installer differs from invocation")
		}
		if err := installers.Validate(t); err != nil {
			return nil, err
		}
	}
	if inv.Installer == "winget" {
		if len(targets) != 1 {
			return nil, unsupported("winget requires one explicit ID")
		}
		var args []string
		for i := 0; i < len(inv.Args); i++ {
			a := inv.Args[i]
			flag, _, equal := strings.Cut(a, "=")
			switch flag {
			case "--id", "--version", "-v", "--source", "-s", "--architecture", "-a":
				if !equal {
					i++
				}
				continue
			case "--exact", "-e":
				continue
			}
			args = append(args, a)
		}
		t := targets[0]
		return append(args, "--id", t.Package, "--exact", "--version", t.NativeVersion(), "--source", t.Source, "--architecture", nativeArch("winget", t.Arch)), nil
	}
	args := make([]string, 0, len(inv.Args)+8)
	operands := 0
	afterCommand := false
	for _, a := range inv.Args {
		if !afterCommand {
			args = append(args, a)
			if a == inv.Operation {
				afterCommand = true
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			args = append(args, a)
			continue
		}
		if operands >= len(targets) {
			return nil, fmt.Errorf("unexpected native package operand")
		}
		t := targets[operands]
		operands++
		switch inv.Installer {
		case "brew":
			args = append(args, t.Source+"/"+t.Package)
		case "apt":
			args = append(args, t.Package+":"+nativeArch("apt", t.Arch)+"="+t.NativeVersion())
		case "dnf":
			// Omitting the architecture suffix permits noarch RPMs. Force the
			// native resolver to the host architecture; preflight rejects a
			// version having more than one host/noarch/source candidate.
			args = append(args, t.Package+"-"+t.NativeVersion())
		}
	}
	if operands != len(targets) {
		return nil, fmt.Errorf("native operands differ from resolved targets")
	}
	if inv.Installer == "dnf" {
		args = append([]string{"--disablerepo=*", "--forcearch=" + nativeArch("dnf", targets[0].Arch)}, args...)
		sources := map[string]bool{}
		for _, t := range targets {
			if !sources[t.Source] {
				args = append(args, "--enablerepo="+t.Source)
				sources[t.Source] = true
			}
		}
	}
	return args, nil
}
