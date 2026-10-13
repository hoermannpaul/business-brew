package shims

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDeployCreatesOnlyInstallerShimsAndNeverOverwrites(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "business-brew")
	if err := os.WriteFile(binary, []byte("fixture executable"), 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "shims")
	if err := Deploy(directory, binary); err != nil {
		t.Fatal(err)
	}
	names := []string{"brew", "winget", "apt", "apt-get", "dnf"}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 5 {
		t.Fatalf("unexpected shims: %v %v", entries, err)
	}
	for _, name := range names {
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		path := filepath.Join(directory, name)
		if runtime.GOOS != "windows" {
			got, err := os.Readlink(path)
			if err != nil || got != binary {
				t.Fatalf("invalid shim %s", name)
			}
		} else {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "fixture executable" {
				t.Fatalf("invalid shim %s", name)
			}
		}
	}
	if err := Deploy(directory, binary); err == nil {
		t.Fatal("existing shims overwritten")
	}
}

func TestDeployPreflightsConflictsBeforeWriting(t *testing.T) {
	directory := t.TempDir()
	binary := filepath.Join(t.TempDir(), "business-brew")
	if err := os.WriteFile(binary, []byte("fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	name := "dnf"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte("existing"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Deploy(directory, binary); err == nil {
		t.Fatal("conflict accepted")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 {
		t.Fatal("partial deployment despite preexisting conflict")
	}
	if err := Deploy("relative", binary); err == nil {
		t.Fatal("relative directory accepted")
	}
}
