package installers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"business-brew/internal/model"
)

type step struct {
	command string
	output  string
	err     error
}
type scriptedRunner struct {
	t     *testing.T
	steps []step
	calls []string
}

func (s *scriptedRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	s.t.Helper()
	command := name + " " + strings.Join(args, " ")
	s.calls = append(s.calls, command)
	if len(s.steps) == 0 {
		s.t.Fatalf("unexpected command: %s", command)
	}
	expected := s.steps[0]
	s.steps = s.steps[1:]
	if expected.command != "" && command != expected.command {
		s.t.Fatalf("command = %q, want %q", command, expected.command)
	}
	return []byte(expected.output), expected.err
}

func target() model.Target {
	return model.Target{Tool: "go", Version: "1.26.0", Package: "go", Installer: "brew", Source: "homebrew/core", OS: "darwin", Arch: "arm64"}
}

func TestBrewNeverInstallsNewerVersion(t *testing.T) {
	r := &scriptedRunner{t: t, steps: []step{{output: `{"formulae":[{"full_name":"go","tap":"homebrew/core","versions":{"stable":"1.26.1"},"installed":[]}]}`}}}
	err := (Native{Runner: r}).Install(context.Background(), target())
	if !errors.Is(err, ErrUnavailable) || len(r.calls) != 1 {
		t.Fatalf("fallback install allowed: %v, %v", err, r.calls)
	}
}

func TestNativeVersionMappingAndPostInstallVerification(t *testing.T) {
	target := target()
	target.PackageVersion = "1.26.0_1"
	metadata := `{"formulae":[{"full_name":"go","tap":"homebrew/core","revision":1,"versions":{"stable":"1.26.0"},"installed":[]}]}`
	r := &scriptedRunner{t: t, steps: []step{{output: metadata}, {command: "brew install --formula homebrew/core/go"}, {output: metadata}}}
	if err := (Native{Runner: r}).Install(context.Background(), target); err == nil {
		t.Fatal("missing exact installed version accepted")
	}
}

func TestCaskVersionAndSource(t *testing.T) {
	x := target()
	x.Package = "editor"
	x.PackageType = "cask"
	x.Source = "homebrew/cask"
	x.PackageVersion = "1.26.0,42"
	r := &scriptedRunner{t: t, steps: []step{{command: "brew info --json=v2 --cask homebrew/cask/editor", output: `{"casks":[{"full_token":"editor","tap":"homebrew/cask","version":"1.26.0,42","installed":"1.26.0,42"}]}`}}}
	s, err := (Native{Runner: r}).Check(context.Background(), x)
	if err != nil || !s.Installed || !s.Available {
		t.Fatalf("cask state %v: %v", s, err)
	}
}

func TestArgumentInjectionRejectedWithoutCommands(t *testing.T) {
	for _, bad := range []string{"--help", "go;touch /tmp/pwn", "$(whoami)", "https://evil.example/go"} {
		x := target()
		x.Package = bad
		r := &scriptedRunner{t: t}
		if err := (Native{Runner: r}).Install(context.Background(), x); err == nil {
			t.Fatal("invalid package accepted", bad)
		}
	}
}

func TestAptRejectsAmbiguousRepository(t *testing.T) {
	x := target()
	x.OS = "linux"
	x.Arch = "amd64"
	x.Installer = "apt"
	x.PackageVersion = "1.26.0-1"
	x.Source = "https://packages.acme.example"
	r := &scriptedRunner{t: t, steps: []step{{output: "go | 1.26.0-1 | https://packages.acme.example stable/main amd64 Packages\ngo | 1.26.0-1 | https://other.example stable/main amd64 Packages"}}}
	if _, err := (Native{Runner: r}).Check(context.Background(), x); err == nil {
		t.Fatal("ambiguous source allowed")
	}
}

func TestDNFChecksArchitectureAndEpoch(t *testing.T) {
	x := target()
	x.OS = "linux"
	x.Arch = "arm64"
	x.Installer = "dnf"
	x.PackageVersion = "1:1.26.0-1"
	x.Source = "company"
	r := &scriptedRunner{t: t, steps: []step{{command: "dnf --disablerepo=* --enablerepo=company repoquery --available --qf %{name}|%{epoch}:%{version}-%{release}|%{arch} go", output: "go|1:1.26.0-1|aarch64\n"}, {output: "go|1:1.26.0-1|x86_64\n"}}}
	s, err := (Native{Runner: r}).Check(context.Background(), x)
	if err != nil || !s.Available || s.Installed {
		t.Fatalf("incorrect architecture state: %v %v", s, err)
	}
}

func TestDNFInstallsAndVerifiesNoarchPackage(t *testing.T) {
	x := target()
	x.OS, x.Arch, x.Installer, x.Source, x.PackageVersion = "linux", "amd64", "dnf", "company", "1.26.0-1"
	metadata := "go|0:1.26.0-1|noarch\n"
	r := &scriptedRunner{t: t, steps: []step{
		{output: metadata}, {err: errors.New("not installed")},
		{command: "dnf --disablerepo=* --enablerepo=company --forcearch=x86_64 install --assumeyes go-1.26.0-1"},
		{output: metadata}, {output: metadata},
	}}
	if err := (Native{Runner: r}).Install(context.Background(), x); err != nil {
		t.Fatalf("noarch install failed: %v", err)
	}
}

type processError int

func (e processError) Error() string { return "native process exited" }
func (e processError) ExitCode() int { return int(e) }
func TestWinGetUnavailableVersionAndArchitecture(t *testing.T) {
	x := target()
	x.OS = "windows"
	x.Arch = "amd64"
	x.Installer = "winget"
	x.Package = "GoLang.Go"
	x.Source = "winget"
	r := &scriptedRunner{t: t, steps: []step{{command: "winget show --id GoLang.Go --exact --version 1.26.0 --source winget --architecture x64 --disable-interactivity", err: processError(-1978335209)}}}
	if _, err := (Native{Runner: r}).Check(context.Background(), x); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("known missing version not classified: %v", err)
	}
	r = &scriptedRunner{t: t, steps: []step{{err: processError(-1978335217)}}}
	if _, err := (Native{Runner: r}).Check(context.Background(), x); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatal("source failure incorrectly classified as package absence")
	}
}
