// Package config loads the shared quota configuration
// (~/.config/quota/config.json), including additional Claude and Codex accounts
// to query beyond the defaults.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sky1core/quota/internal/agenthooks"
)

// ClaudeAccount is an additional Claude account to query, beyond the default
// logged-in account. Accounts are distinguished by their Claude config dir.
type ClaudeAccount struct {
	// Key is the top-level output key (e.g. "claude-2"). Must be unique and
	// must not collide with "claude".
	Key string `json:"key"`
	// ConfigDir is the account's CLAUDE_CONFIG_DIR, stored verbatim as the user
	// wrote it (e.g. "~/.claude-2"). Callers expand "~" via ExpandTilde at use time.
	ConfigDir string `json:"configDir"`
}

// CodexAccount is an additional Codex account to query, beyond the default
// logged-in account. Accounts are distinguished by their Codex home directory.
type CodexAccount struct {
	// Key is the top-level output key (e.g. "codex-2"). Must be unique and
	// must not collide with "codex".
	Key string `json:"key"`
	// Home is the account's CODEX_HOME, stored verbatim as the user wrote it
	// (e.g. "~/.codex-alt"). Callers expand "~" via ExpandTilde at use time.
	Home string `json:"home"`
}

type ExecPromptConfig struct {
	AccountSettings map[string]ExecPromptAccountSettings `json:"accountSettings,omitempty"`
}

type ExecPromptAccountSettings struct {
	MinLeftPct *float64 `json:"minLeftPct,omitempty"`
}

type UpdateConfig struct {
	Ref string `json:"ref,omitempty"`
}

// Config is the parsed ~/.config/quota/config.json.
type Config struct {
	ClaudeAccounts []ClaudeAccount   `json:"claudeAccounts"`
	CodexAccounts  []CodexAccount    `json:"codexAccounts"`
	ExecPrompt     *ExecPromptConfig `json:"execPrompt,omitempty"`
	Update         *UpdateConfig     `json:"update,omitempty"`
}

// Path returns the config file location.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "config.json")
}

// Load reads and parses the config file. A missing file is not an error and
// yields a zero Config. configDir values are returned verbatim (no tilde
// expansion); callers expand them via ExpandTilde at query time.
func Load() (Config, error) {
	b, err := os.ReadFile(Path())
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, err
	}
	return c, nil
}

func Save(c Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var replacement map[string]json.RawMessage
	if err := json.Unmarshal(b, &replacement); err != nil {
		return err
	}
	return Update(func(root map[string]any) error {
		clear(root)
		for key, value := range replacement {
			root[key] = value
		}
		return nil
	})
}

func Update(update func(map[string]any) error) error {
	_, err := agenthooks.UpdateJSONObjectWithBackup(Path(), update)
	return err
}

// ResolvedAccount is a validated Claude account to query.
type ResolvedAccount struct {
	Key       string // "claude" (default) or "claude-2"
	ConfigDir string
	Label     string // "Claude", "Claude 2"
}

// ClaudeExtraKeyRe constrains additional (non-default) Claude account keys to
// "claude-<N>". This makes the output key recognizable to consumers (which
// match ^claude-?\d+$) and yields a deterministic display label. Shared by
// quota-cli's `account add` validation and by ResolveAccounts so the query-time
// and add-time rules can never drift apart.
var ClaudeExtraKeyRe = regexp.MustCompile(`^claude-\d+$`)

// accountLabel returns the human display label for an account key:
// "claude" → "Claude"; "claude-2" → "Claude 2". The caller guarantees key is
// either "claude" or matches ClaudeExtraKeyRe.
func accountLabel(key string) string {
	if key == "claude" {
		return "Claude"
	}
	return "Claude " + strings.TrimPrefix(key, "claude-")
}

func (c Config) ResolveAccounts() ([]ResolvedAccount, []string) {
	extras := make([]accountDirectory, 0, len(c.ClaudeAccounts))
	for _, a := range c.ClaudeAccounts {
		extras = append(extras, accountDirectory{a.Key, a.ConfigDir})
	}
	resolved, skipped := resolveAccountDirectories("claude", extras, ClaudeExtraKeyRe)
	accounts := make([]ResolvedAccount, 0, len(resolved))
	for _, a := range resolved {
		accounts = append(accounts, ResolvedAccount{Key: a.key, ConfigDir: a.dir, Label: accountLabel(a.key)})
	}
	return accounts, skipped
}

// CodexExtraKeyRe constrains additional (non-default) Codex account keys to
// "codex-<N>", mirroring ClaudeExtraKeyRe. This yields a recognizable output key
// and a deterministic display label. Shared by quota-cli's `account add`
// validation and by ResolveCodexAccounts so add-time and query-time rules stay
// in sync.
var CodexExtraKeyRe = regexp.MustCompile(`^codex-\d+$`)

// ResolvedCodexAccount is a validated Codex account to query.
type ResolvedCodexAccount struct {
	Key   string // "codex" (default) or "codex-2"
	Home  string
	Label string // "Codex", "Codex 2"
}

// codexAccountLabel returns the human display label for a Codex account key:
// "codex" → "Codex"; "codex-2" → "Codex 2". The caller guarantees key is either
// "codex" or matches CodexExtraKeyRe.
func codexAccountLabel(key string) string {
	if key == "codex" {
		return "Codex"
	}
	return "Codex " + strings.TrimPrefix(key, "codex-")
}

func (c Config) ResolveCodexAccounts() ([]ResolvedCodexAccount, []string) {
	extras := make([]accountDirectory, 0, len(c.CodexAccounts))
	for _, a := range c.CodexAccounts {
		extras = append(extras, accountDirectory{a.Key, a.Home})
	}
	resolved, skipped := resolveAccountDirectories("codex", extras, CodexExtraKeyRe)
	accounts := make([]ResolvedCodexAccount, 0, len(resolved))
	for _, a := range resolved {
		accounts = append(accounts, ResolvedCodexAccount{Key: a.key, Home: a.dir, Label: codexAccountLabel(a.key)})
	}
	return accounts, skipped
}

// ExpandTilde expands a leading "~" or "~/" to the user's home directory.
// Config stores configDir as the user wrote it; callers expand at query time so
// the stored file stays portable across machines/homes.
func ExpandTilde(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	if p == "~" {
		return home
	}
	return filepath.Join(home, p[2:])
}
