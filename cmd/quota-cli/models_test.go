package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/config"
)

func TestModelsOptions(t *testing.T) {
	opts, err := parseModelOptions([]string{"refresh", "--agent=codex", "--account=codex", "--json"}, io.Discard)
	if err != nil || !opts.refresh || !opts.jsonOut || opts.agent != "codex" || opts.account != "codex" {
		t.Fatalf("options = %+v, %v", opts, err)
	}
	for _, args := range [][]string{{"--agent=other"}, {"refresh", "extra"}, {"--unknown"}, {"--agent=claude", "refresh"}} {
		if _, err := parseModelOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runModels([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit %d", code)
	}
}

func TestModelAccountSelection(t *testing.T) {
	accounts, dirs, err := modelAccounts(config.Config{}, modelOptions{agent: "all"})
	if err != nil || len(accounts) != 2 || len(dirs) != 2 {
		t.Fatalf("accounts = %v, %v, %v", accounts, dirs, err)
	}
	accounts, _, err = modelAccounts(config.Config{}, modelOptions{agent: "codex", account: "codex"})
	if err != nil || len(accounts) != 1 || accounts[0].Provider != "codex" {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	if _, _, err := modelAccounts(config.Config{}, modelOptions{agent: "claude", account: "codex"}); err == nil {
		t.Fatal("accepted mismatched account")
	}
}

func TestModelTargetUsesAbsoluteAccountEnvironment(t *testing.T) {
	dir := t.TempDir()
	for _, provider := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(dir, provider), []byte("placeholder"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", "relative-claude-account")
	t.Setenv("CODEX_HOME", "relative-codex-account")
	t.Setenv("ANTHROPIC_API_KEY", "placeholder")
	t.Setenv("OPENAI_API_KEY", "placeholder")
	for _, provider := range []string{"claude", "codex"} {
		target, err := modelTarget(provider, "")
		if err != nil {
			t.Fatal(err)
		}
		want, _ := filepath.Abs("relative-" + provider + "-account")
		key := "CODEX_HOME"
		secretKey := "OPENAI_API_KEY"
		if provider == "claude" {
			key, secretKey = "CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY"
		}
		if target.ConfigDir != want || target.Binary != filepath.Join(dir, provider) {
			t.Fatalf("target = %+v", target)
		}
		found := false
		for _, entry := range target.Env {
			if entry == key+"="+want {
				found = true
			}
			if strings.HasPrefix(entry, secretKey+"=") {
				t.Fatalf("credential override retained: %s", secretKey)
			}
		}
		if !found {
			t.Fatalf("absolute %s missing", key)
		}
	}
}
