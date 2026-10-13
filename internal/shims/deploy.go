// Package shims creates explicit, non-overwriting native installer entry points.
package shims

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

func Deploy(directory, binaryPath string) error {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(binaryPath) {
		return fmt.Errorf("shim directory and binary path must be absolute")
	}
	info, err := os.Stat(binaryPath)
	if err != nil || !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0111 == 0) {
		return fmt.Errorf("shim source must be a regular executable")
	}
	names := []string{"brew", "winget", "apt", "apt-get", "dnf"}
	if runtime.GOOS == "windows" {
		for i := range names {
			names[i] += ".exe"
		}
	}
	for _, name := range names {
		_, err := os.Lstat(filepath.Join(directory, name))
		if err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("shim destination %s already exists or cannot be inspected", name)
		}
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		return fmt.Errorf("cannot create shim directory: %w", err)
	}
	for _, name := range names {
		path := filepath.Join(directory, name)
		if runtime.GOOS != "windows" {
			if err := os.Symlink(binaryPath, path); err != nil {
				return fmt.Errorf("cannot create %s shim: %w", name, err)
			}
			continue
		}
		if err := copyExecutable(binaryPath, path); err != nil {
			return fmt.Errorf("cannot create %s shim: %w", name, err)
		}
	}
	return nil
}

func copyExecutable(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
