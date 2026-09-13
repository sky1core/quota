package agentinstructions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexCanarySetupUsesEffectiveConfig(t *testing.T) {
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("real Codex CLI is not installed")
	}
	root := t.TempDir()
	home, repo, bin := filepath.Join(root, "home"), filepath.Join(root, "repo"), filepath.Join(root, "bin")
	account := filepath.Join(home, ".codex")
	for _, dir := range []string{account, repo, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", account)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(account, "config.toml"), []byte("project_doc_max_bytes=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexec "+quote(codex)+" -c project_doc_max_bytes=65536 \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := gitInit(ctx, repo); err != nil {
		t.Fatal(err)
	}
	shared, local := markerLine(canaryLabelShared, "shared-token"), markerLine(canaryLabelLocal, "local-token")
	if err := writeMarkerFile(repo, sharedRuleFile, shared); err != nil {
		t.Fatal(err)
	}
	if err := writeMarkerFile(repo, localRuleFile, local); err != nil {
		t.Fatal(err)
	}
	native, err := InspectNativeCodexConfig(ctx, repo)
	if err != nil || native.ProjectDocMaxBytes == nil || *native.ProjectDocMaxBytes != 65536 {
		t.Fatalf("native effective config: %+v %v", native, err)
	}
	if err := runSetup(ctx, repo, "codex"); err != nil {
		t.Fatal(err)
	}
	merged, err := os.ReadFile(filepath.Join(repo, "AGENTS.override.md"))
	if err != nil || string(merged) != shared+"\n"+local {
		t.Fatalf("canary instructions: %q %v", merged, err)
	}
}
