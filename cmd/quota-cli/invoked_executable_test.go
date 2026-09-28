package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
)

func TestInvokedExecutablePathKeepsTheInvokedLinkOnlyForThisBinary(t *testing.T) {
	dir := t.TempDir()
	running := filepath.Join(dir, "versions", "v1", "quota-cli")
	other := filepath.Join(dir, "versions", "v2", "quota-cli")
	for _, path := range []string{running, other} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "bin", "quota-cli")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(running, link); err != nil {
		t.Fatal(err)
	}
	lookPath := func(name string) (string, error) {
		switch name {
		case "quota-cli":
			return link, nil
		case "quota-cli-dot":
			return link, exec.ErrDot
		}
		return "", errors.New("not found")
	}
	current := filepath.Join(dir, "current")
	if err := os.Symlink(filepath.Join(dir, "versions", "v1", "bin"), current); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "versions", "v1", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, tc := range []struct{ arg0, want string }{
		{link, link},
		{"quota-cli", link},
		{"quota-cli-dot", link},
		{"bin/quota-cli", link},
		{"./bin/../bin/quota-cli", dir + "/./bin/../bin/quota-cli"},
		{current + "/../quota-cli", current + "/../quota-cli"},
		{running, running},
		{other, running},
		{filepath.Join(dir, "missing", "quota-cli"), running},
		{"unknown-name", running},
	} {
		if got := invokedExecutablePath(tc.arg0, running, lookPath); got != tc.want {
			t.Errorf("arg0 %q: got %q, want %q", tc.arg0, got, tc.want)
		}
	}
}

func TestInvokedExecutableThroughASymlinkReportsTheLink(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "stable", "quota-cli")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(link, "-test.run=^TestInvokedExecutableChildPrintsItsPath$")
	cmd.Env = append(os.Environ(), "QUOTA_TEST_INVOKED_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
	var got string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "invoked=") {
			got = strings.TrimPrefix(line, "invoked=")
		}
	}
	if got != link {
		t.Fatalf("binary run through %s reported %q\n%s", link, got, out)
	}
}

func TestInvokedExecutableChildPrintsItsPath(t *testing.T) {
	if os.Getenv("QUOTA_TEST_INVOKED_CHILD") != "1" {
		t.Skip("child mode only")
	}
	path, err := invokedExecutable()
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("invoked=%s\n", path)
}

func TestInstructionsSetupThroughASymlinkStoresTheLinkPath(t *testing.T) {
	if os.Getenv("QUOTA_TEST_SETUP_LINK_CHILD") == "1" {
		os.Args = []string{os.Args[0], "agent", "instructions", "setup", "--json"}
		os.Exit(run())
		return
	}
	home := instructionsHome(t)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "stable", "quota-cli")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(link, "-test.run=^TestInstructionsSetupThroughASymlinkStoresTheLinkPath$")
	cmd.Env = append(os.Environ(), "QUOTA_TEST_SETUP_LINK_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("setup through %s failed: %v\n%s", link, err, out)
	}
	for _, rel := range []string{".claude/settings.json", ".codex/hooks.json"} {
		body, err := os.ReadFile(filepath.Join(home, rel))
		if err != nil {
			t.Fatal(err)
		}
		var root any
		if err := json.Unmarshal(body, &root); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		commands := hookCommands(root)
		viaLink := 0
		for _, command := range commands {
			if strings.HasPrefix(command, agenthooks.ShellQuote([]string{link})+" agent instructions _prepare ") {
				viaLink++
			}
			if strings.Contains(command, agenthooks.ShellQuote([]string{binary})) {
				t.Fatalf("%s stores the resolved path instead of the invoked link: %s", rel, command)
			}
		}
		if viaLink == 0 {
			t.Fatalf("%s does not store the invoked link path %s: %q", rel, link, commands)
		}
	}
	t.Chdir(instructionsRepo(t))
	code, stdout, stderr := runInstructions(t, "", "status")
	if !strings.Contains(stdout, "claude: configured;") || strings.Contains(stdout, "managed hook") {
		t.Fatalf("status through the real path after a link install: %d\n%s%s", code, stdout, stderr)
	}
}

func hookCommands(v any) []string {
	switch x := v.(type) {
	case map[string]any:
		var out []string
		if command, ok := x["command"].(string); ok {
			out = append(out, command)
		}
		for _, child := range x {
			out = append(out, hookCommands(child)...)
		}
		return out
	case []any:
		var out []string
		for _, child := range x {
			out = append(out, hookCommands(child)...)
		}
		return out
	}
	return nil
}
