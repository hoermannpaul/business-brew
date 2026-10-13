package nativecmd

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"business-brew/internal/model"
)

type queryResponse struct {
	name string
	args []string
	out  string
	err  error
}
type queryRunner struct {
	t         *testing.T
	responses []queryResponse
	calls     int
}

func (r *queryRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.t.Helper()
	if r.calls >= len(r.responses) {
		r.t.Fatalf("unexpected native query %s %v", name, args)
	}
	q := r.responses[r.calls]
	r.calls++
	if q.name != name || !reflect.DeepEqual(q.args, args) {
		r.t.Fatalf("query got %s %v; want %s %v", name, args, q.name, q.args)
	}
	return []byte(q.out), q.err
}
func TestParseNativeGrammar(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		args                 []string
		installer, operation string
		count                int
	}{
		{"brew", []string{"install", "--formula", "node@22", "git"}, "brew", "install", 2},
		{"brew", []string{"reinstall", "company/tools/thing"}, "brew", "reinstall", 1},
		{"winget.exe", []string{"install", "--id", "OpenJS.NodeJS.LTS", "--version", "22.14.0"}, "winget", "install", 1},
		{"apt-get", []string{"-y", "install", "--reinstall", "nodejs:amd64=22.1-1"}, "apt", "install", 1},
		{"dnf", []string{"upgrade", "git-0:2.49.0-1.fc42.x86_64", "-y"}, "dnf", "upgrade", 1},
	} {
		t.Run(tc.name+strings.Join(tc.args, " "), func(t *testing.T) {
			inv, err := Parse(tc.name, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if inv.Installer != tc.installer || inv.Operation != tc.operation || len(inv.Packages) != tc.count || !reflect.DeepEqual(inv.Args, tc.args) {
				t.Fatalf("bad invocation %+v", inv)
			}
		})
	}
}
func TestUnsafeNativeInvocationsRejected(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"brew", []string{"upgrade"}}, {"brew", []string{"bundle"}}, {"brew", []string{"tap", "evil/tools"}},
		{"brew", []string{"install", "./thing.rb"}}, {"brew", []string{"install", "https://host/tool.rb"}}, {"brew", []string{"install", "git", "--HEAD"}},
		{"brew", []string{"info", "git", "--github"}}, {"brew", []string{"install", "--cask", "--formula", "git"}},
		{"brew", []string{"help", "bundle"}},
		{"winget", []string{"install", "node"}}, {"winget", []string{"install", "--id", "Git.Git", "--override", "evil"}},
		{"winget", []string{"install", "--manifest", "local.yaml"}}, {"winget", []string{"upgrade", "--all"}},
		{"apt", []string{"upgrade", "git"}}, {"apt-get", []string{"upgrade", "git"}}, {"apt", []string{"install", "git-"}},
		{"apt", []string{"install", "git/bookworm"}}, {"apt", []string{"install", "local.deb"}}, {"apt", []string{"install", "git", "-o", "APT::Pre-Invoke=x"}},
		{"dnf", []string{"install", "git", "--enablerepo=evil"}}, {"dnf", []string{"install", "https://evil/pkg.rpm"}},
		{"dnf", []string{"install", "local.rpm"}}, {"dnf", []string{"module", "install", "nodejs"}}, {"dnf", []string{"repoquery", "--setopt=pluginpath=/evil"}},
	} {
		t.Run(tc.name+strings.Join(tc.args, " "), func(t *testing.T) {
			if _, err := Parse(tc.name, tc.args); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("wanted unsupported, got %v", err)
			}
		})
	}
}
func TestReadOnlyCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"brew", []string{"list", "--versions"}}, {"brew", []string{"info", "--json=v2", "git"}},
		{"winget", []string{"search", "--id", "Git.Git", "--exact"}}, {"apt", []string{"list", "--installed"}}, {"dnf", []string{"repolist"}},
	} {
		inv, err := Parse(tc.name, tc.args)
		if err != nil || !inv.ReadOnly {
			t.Fatalf("read only %s: %+v %v", tc.name, inv, err)
		}
	}
}
func TestBrewResolvesCanonicalFormulaAndRevision(t *testing.T) {
	inv, _ := Parse("brew", []string{"install", "node@22"})
	r := &queryRunner{t: t, responses: []queryResponse{{"brew", []string{"info", "--json=v2", "node@22"}, `{"formulae":[{"name":"node@22","full_name":"node@22","tap":"homebrew/core","versions":{"stable":"22.14.0"},"revision":2}],"casks":[]}`, nil}}}
	targets, err := Resolve(context.Background(), inv, r, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	v := targets[0]
	if v.Package != "node@22" || v.Version != "22.14.0" || v.NativeVersion() != "22.14.0_2" || v.Source != "homebrew/core" || v.PackageType != "formula" {
		t.Fatalf("bad target %+v", v)
	}
	pinned, err := PinnedArgs(inv, targets)
	if err != nil || !reflect.DeepEqual(pinned, []string{"install", "homebrew/core/node@22"}) {
		t.Fatalf("pin: %v %v", pinned, err)
	}
}
func TestBrewCaskAndAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		out string
		ok  bool
	}{
		{`{"formulae":[],"casks":[{"token":"visual-studio-code","full_token":"homebrew/cask/visual-studio-code","tap":"homebrew/cask","version":"1.99.0"}]}`, true},
		{`{"formulae":[],"casks":[{"token":"latest-tool","tap":"homebrew/cask","version":"latest"}]}`, false},
		{`{"formulae":[{},{}],"casks":[]}`, false},
	} {
		inv, _ := Parse("brew", []string{"install", "visual-studio-code"})
		r := &queryRunner{t: t, responses: []queryResponse{{"brew", []string{"info", "--json=v2", "visual-studio-code"}, tc.out, nil}}}
		_, err := Resolve(context.Background(), inv, r, "darwin", "arm64")
		if (err == nil) != tc.ok {
			t.Fatalf("result %v", err)
		}
	}
}
func TestAptUsesNativePinnedCandidate(t *testing.T) {
	inv, _ := Parse("apt-get", []string{"install", "-y", "nodejs"})
	r := &queryRunner{t: t, responses: []queryResponse{
		{"apt-cache", []string{"policy", "nodejs:amd64"}, "nodejs:\n  Installed: (none)\n  Candidate: 2:22.14.0-1\n", nil},
		{"apt-cache", []string{"madison", "nodejs:amd64"}, " nodejs | 2:22.14.0-1 | https://deb.example/linux stable/main amd64 Packages\nnodejs | 2:24.0-1 | https://deb.example/linux stable/main amd64 Packages", nil},
	}}
	targets, err := Resolve(context.Background(), inv, r, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if targets[0].NativeVersion() != "2:22.14.0-1" {
		t.Fatal(targets)
	}
	args, err := PinnedArgs(inv, targets)
	if err != nil || !reflect.DeepEqual(args, []string{"install", "-y", "nodejs:amd64=2:22.14.0-1"}) {
		t.Fatalf("pinned %v %v", args, err)
	}
}
func TestAptExplicitVersionUnavailableAndAmbiguousSource(t *testing.T) {
	inv, _ := Parse("apt", []string{"install", "git=1.2-1"})
	for _, out := range []string{"git | 1.3-1 | https://repo/main stable/main amd64 Packages", "git | 1.2-1 | https://repo/main stable/main amd64 Packages\ngit | 1.2-1 | https://other/main stable/main amd64 Packages"} {
		r := &queryRunner{t: t, responses: []queryResponse{{"apt-cache", []string{"madison", "git:amd64"}, out, nil}}}
		_, err := Resolve(context.Background(), inv, r, "linux", "amd64")
		if err == nil {
			t.Fatal("should block")
		}
		if strings.Contains(out, "1.3") && !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unavailable: %v", err)
		}
	}
}
func TestDNFUsesNativeLatestAndPinsRepository(t *testing.T) {
	inv, _ := Parse("dnf", []string{"install", "git", "-y"})
	r := &queryRunner{t: t, responses: []queryResponse{{"dnf", []string{"--quiet", "repoquery", "--available", "--latest-limit=1", "--archlist=x86_64,noarch", "--qf", "%{name}|%{epoch}:%{version}-%{release}|%{arch}|%{reponame}", "git"}, "git|0:2.49.0-1.fc42|x86_64|updates\n", nil}}}
	targets, err := Resolve(context.Background(), inv, r, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	args, err := PinnedArgs(inv, targets)
	want := []string{"--disablerepo=*", "--forcearch=x86_64", "install", "git-2.49.0-1.fc42", "-y", "--enablerepo=updates"}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("pin %v %v", args, err)
	}
}
func TestWingetChecksExactIDVersionAndSource(t *testing.T) {
	inv, _ := Parse("winget", []string{"install", "--id", "Git.Git", "--version", "2.49.0", "--silent"})
	for _, version := range []string{"2.49.0", "2.50.0"} {
		r := &queryRunner{t: t, responses: []queryResponse{
			{"winget", []string{"search", "--id", "Git.Git", "--exact", "--disable-interactivity"}, "Name Id Version Source\n----------------------\nGit Git.Git 2.49.0 winget", nil},
			{"winget", []string{"show", "--id", "Git.Git", "--exact", "--source", "winget", "--architecture", "x64", "--disable-interactivity", "--version", "2.49.0"}, "Found Git [Git.Git]\nVersion: " + version + "\n", nil},
		}}
		targets, err := Resolve(context.Background(), inv, r, "windows", "amd64")
		if version != "2.49.0" {
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("version mismatch %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		args, err := PinnedArgs(inv, targets)
		want := []string{"install", "--silent", "--id", "Git.Git", "--exact", "--version", "2.49.0", "--source", "winget", "--architecture", "x64"}
		if err != nil || !reflect.DeepEqual(args, want) {
			t.Fatalf("pinned %v %v", args, err)
		}
	}
}
func TestMalformedAndQueryErrorsFailClosed(t *testing.T) {
	inv, _ := Parse("brew", []string{"install", "git"})
	failure := fmt.Errorf("network unavailable")
	for _, q := range []queryResponse{{"brew", []string{"info", "--json=v2", "git"}, "not JSON", nil}, {"brew", []string{"info", "--json=v2", "git"}, "", failure}} {
		r := &queryRunner{t: t, responses: []queryResponse{q}}
		_, err := Resolve(context.Background(), inv, r, "darwin", "arm64")
		if err == nil || errors.Is(err, ErrUnavailable) {
			t.Fatalf("should be blocking query error: %v", err)
		}
	}
}
func TestPinnedTargetsRequireCountAndInstaller(t *testing.T) {
	inv, _ := Parse("brew", []string{"install", "git", "node"})
	if _, err := PinnedArgs(inv, []model.Target{}); err == nil {
		t.Fatal("missing targets accepted")
	}
}

func TestArchitectureOverrideRejectedBeforeQuery(t *testing.T) {
	for _, a := range []string{"arm64", "noarch"} {
		inv, _ := Parse("winget", []string{"install", "--id", "Git.Git", "--architecture", a})
		r := &queryRunner{t: t}
		_, err := Resolve(context.Background(), inv, r, "windows", "amd64")
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("cross-architecture %q: %v", a, err)
		}
	}
}
func TestDNFAmbiguousRepositoryFailsClosed(t *testing.T) {
	inv, _ := Parse("dnf", []string{"install", "git"})
	r := &queryRunner{t: t, responses: []queryResponse{{"dnf", []string{"--quiet", "repoquery", "--available", "--latest-limit=1", "--archlist=x86_64,noarch", "--qf", "%{name}|%{epoch}:%{version}-%{release}|%{arch}|%{reponame}", "git"}, "git|0:2.49.0-1.fc42|x86_64|updates\ngit|0:2.49.0-1.fc42|x86_64|other\n", nil}}}
	if _, err := Resolve(context.Background(), inv, r, "linux", "amd64"); err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("ambiguous source error: %v", err)
	}
}

type exitError int

func (e exitError) Error() string { return "native exit" }
func (e exitError) ExitCode() int { return int(e) }
func TestWingetAbsenceUsesDocumentedCodes(t *testing.T) {
	for _, tc := range []struct {
		code        uint32
		unavailable bool
	}{{0x8A150014, true}, {0x8A150017, true}, {0x8A150010, true}, {0x8A150008, false}, {0x8A150016, false}} {
		err := wingetError(fmt.Errorf("query: %w", exitError(int64(tc.code))), "")
		if errors.Is(err, ErrUnavailable) != tc.unavailable {
			t.Fatalf("code %x: %v", tc.code, err)
		}
	}
}
