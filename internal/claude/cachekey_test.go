package claude

import (
	"testing"

	"github.com/sky1core/quota/internal/config"
)

// TestClaudeCacheKey pins that the default account cache key is quota's default
// config directory, independent of the caller's environment.
func TestClaudeCacheKey(t *testing.T) {
	// Explicit configDir is used verbatim (callers pass an already-expanded path).
	if got := claudeCacheKey("/x/.claude-2"); got != "claude:/x/.claude-2" {
		t.Errorf("explicit configDir: got %q", got)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "/env/.claude-9")
	defaultConfigDir, err := config.DefaultAccountDirectory("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claudeCacheKey(""), "claude:"+defaultConfigDir; got != want {
		t.Errorf("default account: got %q want %q", got, want)
	}
}
