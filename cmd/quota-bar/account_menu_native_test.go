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

func TestNativeAccountMenuIntegration(t *testing.T) {
	if os.Getenv("QUOTA_NATIVE_MENU_TEST") != "1" {
		t.Skip("requires a macOS GUI session; set QUOTA_NATIVE_MENU_TEST=1")
	}
	dir := t.TempDir()
	for source, target := range map[string]string{
		"../../go.mod": "go.mod", "../../go.sum": "go.sum",
		"account_menu.go":                     "account_menu.go",
		"menu_order_darwin.go":                "menu_order_darwin.go",
		"menu_order_darwin.h":                 "menu_order_darwin.h",
		"menu_order_darwin.m":                 "menu_order.source",
		"testdata/native-menu/main.go":        "main.go",
		"testdata/native-menu/check_darwin.m": "check_darwin.m",
	} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, target), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "native-menu-test")
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
		t.Fatalf("native menu: %v", err)
	}
}
