package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeSettingsIntegration(t *testing.T) {
	if os.Getenv("QUOTA_NATIVE_SETTINGS_TEST") != "1" {
		t.Skip("requires a macOS GUI session; set QUOTA_NATIVE_SETTINGS_TEST=1")
	}
	dir := t.TempDir()
	sources := map[string]string{
		"../../go.mod": "go.mod", "../../go.sum": "go.sum",
		"settings_window_darwin.go": "settings_window_darwin.go",
		"settings_window_darwin.h":  "settings_window_darwin.h",
		"settings_window_darwin.m":  "settings_window.source",
		"plist_darwin.go":           "plist_darwin.go", "Info.plist": "Info.plist",
		"testdata/native-settings/main.go":        "main.go",
		"testdata/native-settings/check_darwin.m": "check_darwin.m",
	}
	for source, target := range sources {
		b, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, target), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "native-settings-test")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("native build: %v\n%s", err, out)
	}
	run := exec.CommandContext(ctx, binary)
	run.Dir = dir
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") {
			run.Env = append(run.Env, entry)
		}
	}
	run.Env = append(run.Env, "HOME="+dir)
	out, err := run.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatalf("native settings: %v", err)
	}
}
