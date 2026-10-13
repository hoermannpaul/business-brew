package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExportPublicPageUnderRepositoryPath(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"--output", dir, "--base-path", "/business-brew", "--repository-url", "https://github.com/example/business-brew"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, want := range []string{`href="/business-brew/"`, `href="/business-brew/static/landing.css"`, `src="/business-brew/static/landing.js"`, `data-management-url="https://github.com/example/business-brew#self-hosting"`, `data-management-label="Read the self-hosting guide"`} {
		if !strings.Contains(page, want) {
			t.Errorf("export missing %s", want)
		}
	}
	for _, bad := range []string{`href="/admin"`, `href="/"`, `src="/static/`, `{{`, "Admin sign in"} {
		if strings.Contains(page, bad) {
			t.Errorf("export contains service-only markup %s", bad)
		}
	}
	var files []string
	err = filepath.WalkDir(dir, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			relative, _ := filepath.Rel(dir, name)
			files = append(files, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(files, []string{"index.html", "static/brew-mark.svg", "static/landing.css", "static/landing.js"}) {
		t.Fatalf("unexpected exported files: %v", files)
	}
}

func TestExportRejectsInvalidHostingConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"--base-path", "//example.com/site"},
		{"--base-path", "site"},
		{"--base-path", "/site?query"},
		{"--repository-url", "javascript:alert(1)"},
		{"--repository-url", "https://user:secret@example.com/repo"},
	} {
		if err := run(append([]string{"--output", t.TempDir()}, args...)); err == nil {
			t.Errorf("accepted invalid options: %v", args)
		}
	}
}
