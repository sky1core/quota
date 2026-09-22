package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

// Info.plist is also linked into the binary (plist_darwin.go) so the process
// itself knows its bundle identifier; the wrapper bundle is what makes
// LaunchServices and the macOS menu bar settings see the same identity.
//
//go:embed Info.plist
var bundleInfoPlist []byte

func appBundlePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "quota-bar.app")
}

func appBundleExecutable() string {
	return filepath.Join(appBundlePath(), "Contents", "MacOS", "quota-bar")
}

// runningFromAppBundle reports whether this process was started through the
// wrapper bundle's executable path (os.Executable keeps the symlink path).
func runningFromAppBundle() bool {
	exe, err := os.Executable()
	return err == nil && filepath.Clean(exe) == appBundleExecutable()
}

// ensureAppBundle writes the wrapper bundle and points its executable symlink
// at target, the real quota-bar binary. It is idempotent and only rewrites
// what differs.
func ensureAppBundle(target string) error {
	contents := filepath.Join(appBundlePath(), "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		return err
	}
	plist := filepath.Join(contents, "Info.plist")
	if cur, err := os.ReadFile(plist); err != nil || !bytes.Equal(cur, bundleInfoPlist) {
		if err := os.WriteFile(plist, bundleInfoPlist, 0o644); err != nil {
			return err
		}
	}
	link := appBundleExecutable()
	if cur, err := os.Readlink(link); err == nil && cur == target {
		return nil
	}
	tmp := fmt.Sprintf("%s.new.%d", link, os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", link, err)
	}
	return nil
}

// realExecutable resolves the running binary behind any symlink (including
// the wrapper bundle's) so the bundle always points at the real file.
func realExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
