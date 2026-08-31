package claude

import (
	"path/filepath"
	"testing"
)

// TestClaudeCacheKey pins the resolved-path keying that keeps a default-account
// entry cached under one inherited CLAUDE_CONFIG_DIR from being served to a run
// that inherited a different one.
func TestClaudeCacheKey(t *testing.T) {
	// Explicit configDir is used verbatim (callers pass an already-expanded path).
	if got := claudeCacheKey("/x/.claude-2"); got != "claude:/x/.claude-2" {
		t.Errorf("explicit configDir: got %q", got)
	}

	// Empty configDir resolves to the inherited CLAUDE_CONFIG_DIR — the same
	// account fetchEnv will probe, so two different inherited values yield two
	// different keys (no cross-environment bleed).
	t.Setenv("CLAUDE_CONFIG_DIR", "/env/.claude-9")
	if got := claudeCacheKey(""); got != "claude:/env/.claude-9" {
		t.Errorf("inherited CLAUDE_CONFIG_DIR: got %q", got)
	}

	// Empty configDir with no inherited value falls back to ~/.claude.
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("HOME", home)
	if got, want := claudeCacheKey(""), "claude:"+filepath.Join(home, ".claude"); got != want {
		t.Errorf("default account: got %q want %q", got, want)
	}
}
