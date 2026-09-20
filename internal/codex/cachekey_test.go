package codex

import (
	"testing"

	"github.com/sky1core/quota/internal/config"
)

// TestCodexCacheKey mirrors TestClaudeCacheKey: the default cache key is
// quota's default Codex home, independent of the caller's environment.
func TestCodexCacheKey(t *testing.T) {
	// Explicit home is used verbatim (callers pass an already-expanded path).
	if got := codexCacheKey("/x/.codex-alt"); got != "codex:/x/.codex-alt" {
		t.Errorf("explicit home: got %q", got)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "/env/.codex-9")
	defaultHome, err := config.DefaultAccountDirectory("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := codexCacheKey(""), "codex:"+defaultHome; got != want {
		t.Errorf("default account: got %q want %q", got, want)
	}
}
