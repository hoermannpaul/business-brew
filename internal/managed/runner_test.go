package managed

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunnerRejectsSelfCopyAndRelativePath(t *testing.T) {
	self := filepath.Join(t.TempDir(), "business-brew")
	copyPath := filepath.Join(t.TempDir(), "brew")
	for _, path := range []string{self, copyPath} {
		if err := os.WriteFile(path, []byte("wrapper executable"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{self, copyPath, "relative"} {
		r := Runner{Executable: self, NativeBinaries: map[string]string{"brew": path}}
		if _, err := r.Binary("brew"); err == nil {
			t.Fatal("recursive or relative binary accepted")
		}
	}
}

func TestRunnerPreservesArgumentsAndRemovesShimPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a Unix executable script")
	}
	self := filepath.Join(t.TempDir(), "business-brew")
	if err := os.WriteFile(self, []byte("wrapper"), 0700); err != nil {
		t.Fatal(err)
	}
	shims := t.TempDir()
	if err := os.Symlink(self, filepath.Join(shims, "brew")); err != nil {
		t.Fatal(err)
	}
	native := filepath.Join(t.TempDir(), "brew")
	if err := os.WriteFile(native, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" \"$BUSINESS_BREW_WRAPPER_ACTIVE\" \"$PATH\" \"$LC_ALL\" \"$HOMEBREW_NO_INSTALL_CLEANUP\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shims+string(os.PathListSeparator)+"/usr/bin")
	t.Setenv("LC_ALL", "custom-locale")
	t.Setenv("HOMEBREW_NO_INSTALL_CLEANUP", "caller-preference")
	r := Runner{Executable: self, NativeBinaries: map[string]string{"brew": native}}
	out, err := r.Run(context.Background(), "brew", "argument with spaces;literal")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "argument with spaces;literal\n1\n/usr/bin\nC\ncaller-preference\n" {
		t.Fatalf("unexpected native output %q", out)
	}
	if _, err := r.Binary("sudo"); err == nil {
		t.Fatal("unsupported executable accepted")
	}
	for _, entry := range r.Environment() {
		if strings.HasPrefix(entry, "PATH=") && strings.Contains(entry, shims) {
			t.Fatal("shim PATH retained")
		}
	}
}

func TestRawEnvironmentPreservesLocaleAndCleanupPreferences(t *testing.T) {
	t.Setenv("LC_ALL", "custom-locale")
	t.Setenv("HOMEBREW_NO_INSTALL_CLEANUP", "caller-preference")
	t.Setenv("HOMEBREW_NO_AUTO_UPDATE", "0")
	environment := Runner{}.Environment()
	values := map[string]string{}
	for _, entry := range environment {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	if values["LC_ALL"] != "custom-locale" || values["HOMEBREW_NO_INSTALL_CLEANUP"] != "caller-preference" {
		t.Fatal("raw installer environment changed caller locale or cleanup preference")
	}
	if values["HOMEBREW_NO_AUTO_UPDATE"] != "1" || values[ActiveEnvironment] != "1" {
		t.Fatal("candidate stability or recursion guard missing")
	}
}

func TestEnvironmentPreservesNativeBinaryDirectoryAndRemovesCopiedShims(t *testing.T) {
	nativeDirectory := t.TempDir()
	shimDirectory := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	self := filepath.Join(nativeDirectory, "business-brew"+suffix)
	for path, contents := range map[string]string{
		self: "wrapper executable",
		filepath.Join(nativeDirectory, "brew"+suffix):          "real brew executable",
		filepath.Join(nativeDirectory, "native-helper"+suffix): "native dependency",
		filepath.Join(shimDirectory, "brew"+suffix):            "wrapper executable",
	} {
		if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{shimDirectory, nativeDirectory}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "alias")
		if err := os.Symlink(shimDirectory, alias); err != nil {
			t.Fatal(err)
		}
		paths = append([]string{alias}, paths...)
	}
	t.Setenv("PATH", strings.Join(paths, string(os.PathListSeparator)))
	r := Runner{Executable: self}
	for _, entry := range r.Environment() {
		if strings.HasPrefix(entry, "PATH=") && entry != "PATH="+nativeDirectory {
			t.Fatalf("native tool directory was stripped or copied shim retained: %s", entry)
		}
	}
}
