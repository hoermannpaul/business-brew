package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"business-brew/internal/managed"
	"business-brew/internal/model"
	"business-brew/internal/shims"
	"business-brew/internal/wrapper"
)

func TestMain(m *testing.M) {
	if os.Getenv("BUSINESS_BREW_ENTRY_TEST") == "1" && wrapper.IsInstaller(os.Args[0]) {
		main()
		return
	}
	os.Exit(m.Run())
}

func TestGeneratedBrewShimDispatchesActualEntryPoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native fixture uses a POSIX executable script; Windows builds verify compilation")
	}
	for _, scenario := range []string{"approved", "blocked", "inventory"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			var serviceCalls, catalogCalls atomic.Int32
			requests := make(chan model.RequestInput, 1)
			target := model.Target{ID: "node", Tool: "node", Version: "22.14.0", OS: runtime.GOOS, Arch: runtime.GOARCH, Installer: "brew", Package: "node@22", Source: "homebrew/core", PackageType: "formula"}
			if scenario == "approved" {
				target.Grants = []model.Grant{{Organization: "acme"}}
			}
			service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				serviceCalls.Add(1)
				if r.Header.Get("Authorization") != "Bearer test-credential" {
					t.Error("shim did not use managed credential")
					http.Error(w, "invalid credential", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				var response any
				switch r.URL.Path {
				case "/api/v1/identity":
					response = model.Identity{Organization: "acme", Developer: "alice", Projects: []string{"billing"}}
				case "/api/v1/catalog":
					catalogCalls.Add(1)
					response = model.Catalog{SchemaVersion: 1, Revision: 1, Targets: []model.Target{target}}
				case "/api/v1/requests":
					if r.Method != http.MethodPost {
						t.Error("unexpected request method")
						http.Error(w, "invalid method", http.StatusMethodNotAllowed)
						return
					}
					var input model.RequestInput
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Errorf("invalid shim request: %v", err)
						http.Error(w, "invalid input", http.StatusBadRequest)
						return
					}
					select {
					case requests <- input:
					default:
						t.Error("shim created duplicate request submissions")
					}
					response = model.SoftwareRequest{ID: "request-1", Status: "pending", URL: "https://requests.example/request-1"}
				default:
					t.Errorf("unexpected shim service route: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Errorf("encode fixture response: %v", err)
				}
			}))
			defer service.Close()

			native := filepath.Join(root, "real-brew")
			const script = `#!/bin/sh
printf '%s\n' "$*" >> "$BREW_TEST_CALLS"
case "$1" in
  info)
    installed='[]'
    if [ -f "$BREW_TEST_INSTALLED" ]; then installed='[{"version":"22.14.0"}]'; fi
    printf '{"formulae":[{"name":"node@22","full_name":"node@22","tap":"homebrew/core","versions":{"stable":"22.14.0"},"installed":%s}]}\n' "$installed"
    ;;
  install)
    printf '%s\n' "$@" > "$BREW_TEST_ARGUMENTS"
    : > "$BREW_TEST_INSTALLED"
    printf 'native progress\n'
    printf 'native warnings\n' >&2
    ;;
  list)
    printf 'native inventory\n'
    exit 19
    ;;
  *) exit 91 ;;
esac
`
			if err := os.WriteFile(native, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			profile := model.Profile{SchemaVersion: 1, ServiceURL: service.URL, Organization: "acme", Developer: "alice", Projects: []string{"billing"}, CredentialEnv: "BREW_ENTRY_TOKEN", NativeBinaries: map[string]string{"brew": native}}
			profilePath := filepath.Join(root, "company-profile.json")
			data, err := json.Marshal(profile)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(profilePath, data, 0600); err != nil {
				t.Fatal(err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			shimDirectory := filepath.Join(root, "shims")
			if err := shims.Deploy(shimDirectory, self); err != nil {
				t.Fatal(err)
			}
			args := []string{"install", "node@22"}
			if scenario == "inventory" {
				args = []string{"list", "--versions"}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, filepath.Join(shimDirectory, "brew"), args...)
			command.Dir = root
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				if name != managed.ActiveEnvironment && name != "BUSINESS_BREW_PROFILE" && name != "BREW_ENTRY_TOKEN" && name != "BUSINESS_BREW_ENTRY_TEST" {
					command.Env = append(command.Env, entry)
				}
			}
			argumentsPath := filepath.Join(root, "native-arguments")
			callsPath := filepath.Join(root, "native-calls")
			installedPath := filepath.Join(root, "installed")
			command.Env = append(command.Env, "BUSINESS_BREW_ENTRY_TEST=1", "BUSINESS_BREW_PROFILE="+profilePath, "BREW_ENTRY_TOKEN=test-credential", "BREW_TEST_ARGUMENTS="+argumentsPath, "BREW_TEST_CALLS="+callsPath, "BREW_TEST_INSTALLED="+installedPath)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			code := 0
			if err := command.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatalf("native executable never reached: %v; stderr=%s", err, stderr.String())
			}
			switch scenario {
			case "approved":
				if code != 0 || stdout.String() != "native progress\n" || stderr.String() != "native warnings\n" {
					t.Fatalf("authorized native output/exit changed: code=%d out=%q err=%q", code, stdout.String(), stderr.String())
				}
				arguments, err := os.ReadFile(argumentsPath)
				if err != nil || string(arguments) != "install\nhomebrew/core/node@22\n" {
					t.Fatalf("native arguments were not pinned: %q %v", arguments, err)
				}
				if strings.Count(string(calls), "info ") != 3 || catalogCalls.Load() != 2 || len(requests) != 0 {
					t.Fatalf("preflight/recheck/postverify missing: calls=%q catalogs=%d requests=%d", calls, catalogCalls.Load(), len(requests))
				}
			case "blocked":
				if code != wrapper.ExitBlocked || !strings.Contains(stderr.String(), "https://requests.example/request-1") || len(requests) != 1 {
					t.Fatalf("blocked request/link lost: code=%d out=%q err=%q", code, stdout.String(), stderr.String())
				}
				input := <-requests
				if input.RequestType != "approval" || input.Package != "node@22" || input.PackageVersion != "22.14.0" || input.Project != "" {
					t.Fatalf("request candidate/scope changed: %+v", input)
				}
				if _, err := os.Stat(installedPath); !os.IsNotExist(err) || strings.Contains(string(calls), "install ") {
					t.Fatal("blocked command ran native install")
				}
			case "inventory":
				if code != 19 || stdout.String() != "native inventory\n" || string(calls) != "list --versions\n" || serviceCalls.Load() != 0 {
					t.Fatalf("inventory passthrough changed: code=%d out=%q calls=%q service=%d stderr=%q", code, stdout.String(), calls, serviceCalls.Load(), stderr.String())
				}
			}
		})
	}
}
