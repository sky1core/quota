package main

import (
	"testing"

	"github.com/sky1core/quota/internal/config"
)

func TestValidateNewAccount(t *testing.T) {
	existing := []config.ClaudeAccount{{Key: "claude-2", ConfigDir: "/a"}}
	if err := validateNewAccount(existing, "claude-3", "/b"); err != nil {
		t.Errorf("valid account rejected: %v", err)
	}
	bad := []struct {
		name, key, dir string
	}{
		{"empty key", "", "/b"},
		{"empty dir", "claude-3", ""},
		{"no dash", "claude3", "/b"},
		{"arbitrary key", "work", "/b"},
		{"duplicate key", "claude-2", "/b"},
		{"duplicate dir", "claude-3", "/a"},
	}
	for _, tt := range bad {
		if err := validateNewAccount(existing, tt.key, tt.dir); err == nil {
			t.Errorf("%s: expected rejection, got nil", tt.name)
		}
	}
}

func TestValidateNewCodexAccount(t *testing.T) {
	existing := []config.CodexAccount{{Key: "codex-2", Home: "/a"}}
	if err := validateNewCodexAccount(existing, "codex-3", "/b"); err != nil {
		t.Errorf("valid account rejected: %v", err)
	}
	bad := []struct {
		name, key, home string
	}{
		{"empty key", "", "/b"},
		{"empty home", "codex-3", ""},
		{"no dash", "codex3", "/b"},
		{"claude key", "claude-2", "/b"},
		{"arbitrary key", "work", "/b"},
		{"duplicate key", "codex-2", "/b"},
		{"duplicate home", "codex-3", "/a"},
	}
	for _, tt := range bad {
		if err := validateNewCodexAccount(existing, tt.key, tt.home); err == nil {
			t.Errorf("%s: expected rejection, got nil", tt.name)
		}
	}
}

func TestCodexAccountAddRemove_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if code := accountAdd([]string{"codex-2", "~/.codex-alt"}); code != 0 {
		t.Fatalf("add exit code = %d, want 0", code)
	}
	cfg, _ := config.Load()
	if len(cfg.CodexAccounts) != 1 || cfg.CodexAccounts[0].Key != "codex-2" {
		t.Fatalf("after add: %+v", cfg.CodexAccounts)
	}
	if cfg.CodexAccounts[0].Home != "~/.codex-alt" {
		t.Errorf("home must be stored verbatim, got %q", cfg.CodexAccounts[0].Home)
	}
	// A codex add must not create any claude account.
	if len(cfg.ClaudeAccounts) != 0 {
		t.Errorf("codex add leaked into claude accounts: %+v", cfg.ClaudeAccounts)
	}

	if code := accountRemove([]string{"codex-2"}); code != 0 {
		t.Fatalf("rm exit code = %d, want 0", code)
	}
	if cfg2, _ := config.Load(); len(cfg2.CodexAccounts) != 0 {
		t.Errorf("after rm: %+v", cfg2.CodexAccounts)
	}
}

// TestAccountAdd_RoutesByKeyPrefix pins that the same `account add` surface sends
// claude-<N> to Claude and codex-<N> to Codex, and rejects any other key.
func TestAccountAdd_RoutesByKeyPrefix(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if code := accountAdd([]string{"claude-2", "~/.claude-2"}); code != 0 {
		t.Fatalf("claude add failed: %d", code)
	}
	if code := accountAdd([]string{"codex-2", "~/.codex-2"}); code != 0 {
		t.Fatalf("codex add failed: %d", code)
	}
	// A key matching neither provider format must be rejected without mutating.
	if code := accountAdd([]string{"work", "/x"}); code == 0 {
		t.Error("non-provider key should be rejected")
	}

	cfg, _ := config.Load()
	if len(cfg.ClaudeAccounts) != 1 || cfg.ClaudeAccounts[0].Key != "claude-2" {
		t.Errorf("claude routing wrong: %+v", cfg.ClaudeAccounts)
	}
	if len(cfg.CodexAccounts) != 1 || cfg.CodexAccounts[0].Key != "codex-2" {
		t.Errorf("codex routing wrong: %+v", cfg.CodexAccounts)
	}
}

func TestAccountAddRemove_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if code := accountAdd([]string{"claude-2", "~/.claude-2"}); code != 0 {
		t.Fatalf("add exit code = %d, want 0", code)
	}
	cfg, _ := config.Load()
	if len(cfg.ClaudeAccounts) != 1 || cfg.ClaudeAccounts[0].Key != "claude-2" {
		t.Fatalf("after add: %+v", cfg.ClaudeAccounts)
	}
	if cfg.ClaudeAccounts[0].ConfigDir != "~/.claude-2" {
		t.Errorf("configDir must be stored verbatim, got %q", cfg.ClaudeAccounts[0].ConfigDir)
	}

	// Duplicate key is rejected (non-zero) and does not mutate the file.
	if code := accountAdd([]string{"claude-2", "/other"}); code == 0 {
		t.Error("duplicate-key add should fail")
	}
	if cfg2, _ := config.Load(); len(cfg2.ClaudeAccounts) != 1 {
		t.Errorf("rejected add must not change config: %+v", cfg2.ClaudeAccounts)
	}

	if code := accountRemove([]string{"claude-2"}); code != 0 {
		t.Fatalf("rm exit code = %d, want 0", code)
	}
	if cfg3, _ := config.Load(); len(cfg3.ClaudeAccounts) != 0 {
		t.Errorf("after rm: %+v", cfg3.ClaudeAccounts)
	}

	// Removing a missing key fails.
	if code := accountRemove([]string{"claude-9"}); code == 0 {
		t.Error("removing a missing key should fail")
	}
}
