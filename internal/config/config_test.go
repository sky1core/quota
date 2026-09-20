package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/agenthooks"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfgDir := filepath.Join(dir, ".config", "quota")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	c, err := Load()
	if err != nil {
		t.Fatalf("missing file should not be an error: %v", err)
	}
	if len(c.ClaudeAccounts) != 0 {
		t.Errorf("expected no accounts, got %d", len(c.ClaudeAccounts))
	}
}

func TestLoad_KeepsConfigDirVerbatim(t *testing.T) {
	// Load must NOT expand tilde — the stored form is preserved so a
	// load→save round-trip stays portable. Expansion happens at query time.
	writeConfig(t, `{"claudeAccounts":[{"key":"claude-2","configDir":"~/.claude-2"}]}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ClaudeAccounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(c.ClaudeAccounts))
	}
	a := c.ClaudeAccounts[0]
	if a.Key != "claude-2" {
		t.Errorf("key = %q, want claude-2", a.Key)
	}
	if a.ConfigDir != "~/.claude-2" {
		t.Errorf("configDir = %q, want ~/.claude-2 (must be verbatim, not expanded)", a.ConfigDir)
	}
}

func TestExpandTilde(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cases := map[string]string{
		"~/.claude-2": filepath.Join(dir, ".claude-2"),
		"~":           dir,
		"/opt/x":      "/opt/x",
		"relative":    "relative",
		"":            "",
	}
	for in, want := range cases {
		if got := ExpandTilde(in); got != want {
			t.Errorf("ExpandTilde(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	in := Config{ClaudeAccounts: []ClaudeAccount{
		{Key: "claude-2", ConfigDir: "~/.claude-2"},
		{Key: "claude-3", ConfigDir: "/opt/c3"},
	}}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.ClaudeAccounts) != 2 {
		t.Fatalf("round-trip lost accounts: %+v", got.ClaudeAccounts)
	}
	// configDir must survive verbatim (no expansion on save or load).
	if got.ClaudeAccounts[0].ConfigDir != "~/.claude-2" {
		t.Errorf("round-trip changed configDir: %q", got.ClaudeAccounts[0].ConfigDir)
	}
}

func TestLoad_BadJSON(t *testing.T) {
	writeConfig(t, `{not valid json`)
	if _, err := Load(); err == nil {
		t.Error("expected a parse error for malformed JSON")
	}
}

func TestLoad_EmptyAccounts(t *testing.T) {
	writeConfig(t, `{"claudeAccounts":[]}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.ClaudeAccounts) != 0 {
		t.Errorf("expected 0 accounts, got %d", len(c.ClaudeAccounts))
	}
}

func TestLoad_ExecPromptAccountSettings(t *testing.T) {
	writeConfig(t, `{"execPrompt":{"accountSettings":{"claude":{"minLeftPct":40},"codex-2":{"minLeftPct":5}}}}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.ExecPrompt == nil {
		t.Fatal("missing execPrompt config")
	}
	if got := c.ExecPrompt.AccountSettings["claude"].MinLeftPct; got == nil || *got != 40 {
		t.Fatalf("claude minLeftPct = %v, want 40", got)
	}
	if got := c.ExecPrompt.AccountSettings["codex-2"].MinLeftPct; got == nil || *got != 5 {
		t.Fatalf("codex-2 minLeftPct = %v, want 5", got)
	}
}

func TestLoad_UpdateRef(t *testing.T) {
	writeConfig(t, `{"update":{"ref":"main"}}`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Update == nil || c.Update.Ref != "main" {
		t.Fatalf("update ref = %+v, want main", c.Update)
	}
}

func resolvedKeys(accts []ResolvedAccount) []string {
	out := make([]string, len(accts))
	for i, a := range accts {
		out[i] = a.Key
	}
	return out
}

func TestResolveAccounts_DefaultOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	accts, skipped := Config{}.ResolveAccounts()
	if len(skipped) != 0 {
		t.Fatalf("no config → no skipped, got %v", skipped)
	}
	if len(accts) != 1 {
		t.Fatalf("expected only the default account, got %v", resolvedKeys(accts))
	}
	got := accts[0]
	want, err := DefaultAccountDirectory("claude")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "claude" || got.ConfigDir != want || got.Label != "Claude" {
		t.Errorf("default account = %+v, want %s", got, want)
	}
}

func TestResolveAccounts_ValidExtras(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	accts, skipped := Config{ClaudeAccounts: []ClaudeAccount{
		{Key: "claude-2", ConfigDir: "~/.claude-2"},
		{Key: "claude-3", ConfigDir: "/opt/c3"},
	}}.ResolveAccounts()
	if len(skipped) != 0 {
		t.Fatalf("expected no skipped, got %v", skipped)
	}
	want := []string{"claude", "claude-2", "claude-3"}
	if got := resolvedKeys(accts); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	// Label formatting.
	if accts[1].Label != "Claude 2" {
		t.Errorf("claude-2 label = %q, want %q", accts[1].Label, "Claude 2")
	}
	if accts[2].Label != "Claude 3" {
		t.Errorf("claude-3 label = %q, want %q", accts[2].Label, "Claude 3")
	}
	// configDir must be tilde-expanded in the resolved result.
	if want, err := CanonicalAccountDirectory(filepath.Join(dir, ".claude-2")); err != nil || accts[1].ConfigDir != want {
		t.Errorf("claude-2 configDir = %q, want %q (expanded)", accts[1].ConfigDir, want)
	}
	if accts[2].ConfigDir != "/opt/c3" {
		t.Errorf("claude-3 configDir = %q, want /opt/c3", accts[2].ConfigDir)
	}
}

func TestResolveAccounts_SkipsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		account ClaudeAccount
	}{
		{"empty key", ClaudeAccount{Key: "", ConfigDir: "/a"}},
		{"empty configDir", ClaudeAccount{Key: "claude-2", ConfigDir: ""}},
		{"no dash", ClaudeAccount{Key: "claude2", ConfigDir: "/a"}},
		{"non-numeric", ClaudeAccount{Key: "claude-x", ConfigDir: "/a"}},
		{"arbitrary key", ClaudeAccount{Key: "work", ConfigDir: "/a"}},
		{"collides with default", ClaudeAccount{Key: "claude", ConfigDir: "/a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accts, skipped := Config{ClaudeAccounts: []ClaudeAccount{tt.account}}.ResolveAccounts()
			if len(skipped) != 1 {
				t.Fatalf("expected 1 skipped message, got %v", skipped)
			}
			if got := resolvedKeys(accts); len(got) != 1 || got[0] != "claude" {
				t.Errorf("invalid account must not be resolved, got %v", got)
			}
		})
	}
}

func TestResolveAccounts_DuplicateKey(t *testing.T) {
	accts, skipped := Config{ClaudeAccounts: []ClaudeAccount{
		{Key: "claude-2", ConfigDir: "/a"},
		{Key: "claude-2", ConfigDir: "/b"},
	}}.ResolveAccounts()
	if len(skipped) != 2 {
		t.Fatalf("expected 2 duplicate-key skip, got %v", skipped)
	}
	if got := resolvedKeys(accts); strings.Join(got, ",") != "claude" {
		t.Errorf("ambiguous keys retained, got %v", got)
	}
}

func TestResolveAccounts_DuplicateConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	accts, skipped := Config{ClaudeAccounts: []ClaudeAccount{
		{Key: "claude-2", ConfigDir: "~/.same"},
		{Key: "claude-3", ConfigDir: filepath.Join(dir, ".same")},
	}}.ResolveAccounts()
	if len(skipped) != 2 {
		t.Fatalf("expected 2 duplicate-configDir skip, got %v", skipped)
	}
	if got := resolvedKeys(accts); strings.Join(got, ",") != "claude" {
		t.Errorf("ambiguous directories retained, got %v", got)
	}
}

func resolvedCodexKeys(accts []ResolvedCodexAccount) []string {
	out := make([]string, len(accts))
	for i, a := range accts {
		out[i] = a.Key
	}
	return out
}

func TestResolveCodexAccounts_DefaultOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	accts, skipped := Config{}.ResolveCodexAccounts()
	if len(skipped) != 0 {
		t.Fatalf("no config → no skipped, got %v", skipped)
	}
	if len(accts) != 1 {
		t.Fatalf("expected only the default account, got %v", resolvedCodexKeys(accts))
	}
	got := accts[0]
	want, err := DefaultAccountDirectory("codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.Key != "codex" || got.Home != want || got.Label != "Codex" {
		t.Errorf("default account = %+v, want %s", got, want)
	}
}

func TestResolveCodexAccounts_ValidExtras(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	accts, skipped := Config{CodexAccounts: []CodexAccount{
		{Key: "codex-2", Home: "~/.codex-2"},
		{Key: "codex-3", Home: "/opt/cx3"},
	}}.ResolveCodexAccounts()
	if len(skipped) != 0 {
		t.Fatalf("expected no skipped, got %v", skipped)
	}
	want := []string{"codex", "codex-2", "codex-3"}
	if got := resolvedCodexKeys(accts); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", got, want)
	}
	if accts[1].Label != "Codex 2" {
		t.Errorf("codex-2 label = %q, want %q", accts[1].Label, "Codex 2")
	}
	// home must be tilde-expanded in the resolved result.
	if want, err := CanonicalAccountDirectory(filepath.Join(dir, ".codex-2")); err != nil || accts[1].Home != want {
		t.Errorf("codex-2 home = %q, want %q (expanded)", accts[1].Home, want)
	}
	if accts[2].Home != "/opt/cx3" {
		t.Errorf("codex-3 home = %q, want /opt/cx3", accts[2].Home)
	}
}

func TestResolveCodexAccounts_SkipsInvalid(t *testing.T) {
	tests := []struct {
		name    string
		account CodexAccount
	}{
		{"empty key", CodexAccount{Key: "", Home: "/a"}},
		{"empty home", CodexAccount{Key: "codex-2", Home: ""}},
		{"no dash", CodexAccount{Key: "codex2", Home: "/a"}},
		{"non-numeric", CodexAccount{Key: "codex-x", Home: "/a"}},
		{"claude key rejected", CodexAccount{Key: "claude-2", Home: "/a"}},
		{"collides with default", CodexAccount{Key: "codex", Home: "/a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accts, skipped := Config{CodexAccounts: []CodexAccount{tt.account}}.ResolveCodexAccounts()
			if len(skipped) != 1 {
				t.Fatalf("expected 1 skipped message, got %v", skipped)
			}
			if got := resolvedCodexKeys(accts); len(got) != 1 || got[0] != "codex" {
				t.Errorf("invalid account must not be resolved, got %v", got)
			}
		})
	}
}

func TestResolveCodexAccounts_DuplicateKey(t *testing.T) {
	accts, skipped := Config{CodexAccounts: []CodexAccount{
		{Key: "codex-2", Home: "/a"},
		{Key: "codex-2", Home: "/b"},
	}}.ResolveCodexAccounts()
	if len(skipped) != 2 {
		t.Fatalf("expected 2 duplicate-key skip, got %v", skipped)
	}
	if got := resolvedCodexKeys(accts); strings.Join(got, ",") != "codex" {
		t.Errorf("ambiguous keys retained, got %v", got)
	}
}

func TestResolveCodexAccounts_DuplicateHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	accts, skipped := Config{CodexAccounts: []CodexAccount{
		{Key: "codex-2", Home: "~/.same"},
		{Key: "codex-3", Home: filepath.Join(dir, ".same")},
	}}.ResolveCodexAccounts()
	if len(skipped) != 2 {
		t.Fatalf("expected 2 duplicate-home skip, got %v", skipped)
	}
	if got := resolvedCodexKeys(accts); strings.Join(got, ",") != "codex" {
		t.Errorf("ambiguous directories retained, got %v", got)
	}
}

func TestSaveLoad_CodexRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	in := Config{
		ClaudeAccounts: []ClaudeAccount{{Key: "claude-2", ConfigDir: "~/.claude-2"}},
		CodexAccounts:  []CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}},
		ExecPrompt: &ExecPromptConfig{AccountSettings: map[string]ExecPromptAccountSettings{
			"claude": {MinLeftPct: testFloatPtr(40)},
		}},
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.CodexAccounts) != 1 || got.CodexAccounts[0].Key != "codex-2" {
		t.Fatalf("round-trip lost codex accounts: %+v", got.CodexAccounts)
	}
	// home must survive verbatim (no expansion on save or load).
	if got.CodexAccounts[0].Home != "~/.codex-2" {
		t.Errorf("round-trip changed home: %q", got.CodexAccounts[0].Home)
	}
	// Claude accounts must be unaffected by the added codex list.
	if len(got.ClaudeAccounts) != 1 || got.ClaudeAccounts[0].Key != "claude-2" {
		t.Errorf("claude accounts corrupted: %+v", got.ClaudeAccounts)
	}
	if got.ExecPrompt == nil || got.ExecPrompt.AccountSettings["claude"].MinLeftPct == nil || *got.ExecPrompt.AccountSettings["claude"].MinLeftPct != 40 {
		t.Errorf("execPrompt settings corrupted: %+v", got.ExecPrompt)
	}
}

func TestResolveAccounts_InvalidDoesNotBlockValid(t *testing.T) {
	// A format-violating entry must not poison seenKey/seenDir: a rejected
	// "claude2" at /a must not stop a valid "claude-2" also at /a.
	accts, skipped := Config{ClaudeAccounts: []ClaudeAccount{
		{Key: "claude2", ConfigDir: "/a"},  // format violation → skipped
		{Key: "claude-2", ConfigDir: "/a"}, // valid, must still resolve
	}}.ResolveAccounts()
	if len(skipped) != 1 {
		t.Fatalf("expected exactly 1 skip (the invalid entry), got %v", skipped)
	}
	if got := resolvedKeys(accts); strings.Join(got, ",") != "claude,claude-2" {
		t.Errorf("valid account after an invalid one must resolve, got %v", got)
	}
	if accts[1].ConfigDir != "/a" {
		t.Errorf("configDir = %q, want /a", accts[1].ConfigDir)
	}
}

func testFloatPtr(v float64) *float64 {
	return &v
}

func TestUpdatePreservesJSONAndAborts(t *testing.T) {
	writeConfig(t, `{"unknown":{"id":9007199254740993,"decimal":1.234567890123456789,"exponent":1e+80}}`)
	if err := Update(func(root map[string]any) error {
		root["claudeAccounts"] = []ClaudeAccount{{Key: "claude-2", ConfigDir: "~/account"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"9007199254740993", "1.234567890123456789", "1e+80"} {
		if !strings.Contains(string(before), number) {
			t.Fatalf("number %s changed: %s", number, before)
		}
	}
	failure := errors.New("rejected")
	if err := Update(func(root map[string]any) error {
		delete(root, "unknown")
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("update error = %v", err)
	}
	after, err := os.ReadFile(Path())
	if err != nil || string(after) != string(before) {
		t.Fatalf("rejected update changed file: %s, %v", after, err)
	}
}

func TestSaveSharesSettingsLock(t *testing.T) {
	writeConfig(t, `{"claudeAccounts":[]}`)
	locked := make(chan struct{})
	release := make(chan struct{})
	settingsDone := make(chan error, 1)
	go func() {
		_, err := agenthooks.UpdateJSONObjectWithBackup(Path(), func(root map[string]any) error {
			close(locked)
			<-release
			root["settingsChange"] = true
			return nil
		})
		settingsDone <- err
	}()
	<-locked
	saved := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		saved <- Save(Config{CodexAccounts: []CodexAccount{{Key: "codex-2", Home: "~/account"}}})
	}()
	<-started
	select {
	case err := <-saved:
		close(release)
		<-settingsDone
		t.Fatalf("Save completed while settings held lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-settingsDone; err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil || len(cfg.CodexAccounts) != 1 {
		t.Fatalf("replacement not saved: %+v, %v", cfg, err)
	}
}

func TestConfigWritersRejectMalformedJSON(t *testing.T) {
	for _, body := range []string{`{broken`, `null`, `[]`, `{} {}`} {
		t.Run(body, func(t *testing.T) {
			writeConfig(t, body)
			called := false
			if err := Update(func(root map[string]any) error {
				called = true
				return nil
			}); err == nil || called {
				t.Fatalf("invalid config accepted: called=%v err=%v", called, err)
			}
			if err := Save(Config{}); err == nil {
				t.Fatal("Save replaced malformed config")
			}
			got, err := os.ReadFile(Path())
			if err != nil || string(got) != body {
				t.Fatalf("malformed config changed: %s, %v", got, err)
			}
		})
	}
}
