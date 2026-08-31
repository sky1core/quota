package codex

import (
	"path/filepath"
	"testing"
)

// TestCodexCacheKey mirrors TestClaudeCacheKey: the shared-cache key is the
// resolved CODEX_HOME, so two inherited environments never share a default
// account's entry.
func TestCodexCacheKey(t *testing.T) {
	// Explicit home is used verbatim (callers pass an already-expanded path).
	if got := codexCacheKey("/x/.codex-alt"); got != "codex:/x/.codex-alt" {
		t.Errorf("explicit home: got %q", got)
	}

	// Empty home resolves to the inherited CODEX_HOME.
	t.Setenv("CODEX_HOME", "/env/.codex-9")
	if got := codexCacheKey(""); got != "codex:/env/.codex-9" {
		t.Errorf("inherited CODEX_HOME: got %q", got)
	}

	// Empty home with no inherited value falls back to ~/.codex.
	home := t.TempDir()
	t.Setenv("CODEX_HOME", "")
	t.Setenv("HOME", home)
	if got, want := codexCacheKey(""), "codex:"+filepath.Join(home, ".codex"); got != want {
		t.Errorf("default account: got %q want %q", got, want)
	}
}
