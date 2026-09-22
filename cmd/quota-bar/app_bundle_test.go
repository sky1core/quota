package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureAppBundleWritesAndRepointsWrapper(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	first := filepath.Join(t.TempDir(), "quota-bar-1")
	second := filepath.Join(t.TempDir(), "quota-bar-2")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensureAppBundle(first); err != nil {
		t.Fatal(err)
	}
	plist, err := os.ReadFile(filepath.Join(appBundlePath(), "Contents", "Info.plist"))
	if err != nil || !bytes.Equal(plist, bundleInfoPlist) {
		t.Fatalf("Info.plist not written from embedded copy: %v", err)
	}
	if got, _ := os.Readlink(appBundleExecutable()); got != first {
		t.Fatalf("symlink = %q, want %q", got, first)
	}
	if err := ensureAppBundle(second); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(appBundleExecutable()); got != second {
		t.Fatalf("symlink after repoint = %q, want %q", got, second)
	}
	entries, err := os.ReadDir(filepath.Dir(appBundleExecutable()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "quota-bar" {
		t.Fatalf("temporary symlink left behind: %v", entries)
	}
}

func TestLoginRegistrationRunsThroughAppBundle(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	content, err := newLoginRegistration()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "<string>"+appBundleExecutable()+"</string>") {
		t.Fatalf("plist does not launch the app bundle executable:\n%s", content)
	}
	real, err := realExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.Readlink(appBundleExecutable()); got != real {
		t.Fatalf("bundle symlink = %q, want running binary %q", got, real)
	}
}
