// Package config loads the shared quota configuration
// (~/.config/quota/config.json), currently the list of additional Claude
// accounts to query beyond the default.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

// Config is the parsed ~/.config/quota/config.json.
type Config struct {
	ClaudeAccounts []ClaudeAccount `json:"claudeAccounts"`
	CodexAccounts  []CodexAccount  `json:"codexAccounts"`
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

// Save writes the config atomically (temp file + rename). configDir values are
// stored verbatim as the user provided them (e.g. "~/.claude-2").
func Save(c Config) error {
	p := Path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ResolvedAccount is a validated Claude account to query.
type ResolvedAccount struct {
	Key       string // "claude" (default) or "claude-2"
	ConfigDir string // expanded; "" for the default account
	Label     string // "Claude", "Claude 2"
}

// ClaudeExtraKeyRe constrains additional (non-default) Claude account keys to
// "claude-<N>". This makes the output key recognizable to consumers (sky-ai
// reads ^claude-?\d+$) and yields a deterministic display label. Shared by
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

// ResolveAccounts returns the accounts to query: the default "claude" account
// first, then each valid extra account, plus a human-readable message for every
// skipped entry. Skip rules, applied in order, are:
//   - empty key or configDir;
//   - key not matching ClaudeExtraKeyRe (e.g. "claude-<N>");
//   - duplicate key (including a collision with the default "claude");
//   - duplicate configDir after ExpandTilde (querying the same account twice
//     is a config mistake, not a second account).
//
// The default account is always {Key:"claude", ConfigDir:"", Label:"Claude"}
// and always leads the slice. Extra configDirs are tilde-expanded in the
// returned ConfigDir; the default account keeps an empty ConfigDir.
func (c Config) ResolveAccounts() ([]ResolvedAccount, []string) {
	accounts := []ResolvedAccount{{Key: "claude", ConfigDir: "", Label: "Claude"}}
	var skipped []string
	seenKey := map[string]bool{"claude": true}
	seenDir := map[string]bool{}
	for _, a := range c.ClaudeAccounts {
		exp := ExpandTilde(a.ConfigDir)
		switch {
		case a.Key == "" || a.ConfigDir == "":
			skipped = append(skipped, fmt.Sprintf("claude account skipped: empty key or configDir (key=%q)", a.Key))
		case !ClaudeExtraKeyRe.MatchString(a.Key):
			skipped = append(skipped, fmt.Sprintf("claude account key %q must match claude-<N> (e.g. claude-2), skipped", a.Key))
		case seenKey[a.Key]:
			skipped = append(skipped, fmt.Sprintf("claude account key %q is a duplicate, skipped", a.Key))
		case seenDir[exp]:
			skipped = append(skipped, fmt.Sprintf("claude account %q configDir %q duplicates another account, skipped", a.Key, a.ConfigDir))
		default:
			seenKey[a.Key] = true
			seenDir[exp] = true
			accounts = append(accounts, ResolvedAccount{Key: a.Key, ConfigDir: exp, Label: accountLabel(a.Key)})
		}
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
	Home  string // expanded CODEX_HOME; "" for the default account
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

// ResolveCodexAccounts returns the Codex accounts to query: the default "codex"
// account first, then each valid extra account, plus a human-readable message
// for every skipped entry. It mirrors ResolveAccounts (Claude). Skip rules,
// applied in order, are:
//   - empty key or home;
//   - key not matching CodexExtraKeyRe (e.g. "codex-<N>");
//   - duplicate key (including a collision with the default "codex");
//   - duplicate home after ExpandTilde (two entries pointing at the same
//     CODEX_HOME are the same account — redundant).
//
// The default account is always {Key:"codex", Home:"", Label:"Codex"} and always
// leads the slice. Extra homes are tilde-expanded in the returned Home; the
// default account keeps an empty Home (inherits the process CODEX_HOME).
func (c Config) ResolveCodexAccounts() ([]ResolvedCodexAccount, []string) {
	accounts := []ResolvedCodexAccount{{Key: "codex", Home: "", Label: "Codex"}}
	var skipped []string
	seenKey := map[string]bool{"codex": true}
	seenDir := map[string]bool{}
	for _, a := range c.CodexAccounts {
		exp := ExpandTilde(a.Home)
		switch {
		case a.Key == "" || a.Home == "":
			skipped = append(skipped, fmt.Sprintf("codex account skipped: empty key or home (key=%q)", a.Key))
		case !CodexExtraKeyRe.MatchString(a.Key):
			skipped = append(skipped, fmt.Sprintf("codex account key %q must match codex-<N> (e.g. codex-2), skipped", a.Key))
		case seenKey[a.Key]:
			skipped = append(skipped, fmt.Sprintf("codex account key %q is a duplicate, skipped", a.Key))
		case seenDir[exp]:
			skipped = append(skipped, fmt.Sprintf("codex account %q home %q duplicates another account, skipped", a.Key, a.Home))
		default:
			seenKey[a.Key] = true
			seenDir[exp] = true
			accounts = append(accounts, ResolvedCodexAccount{Key: a.Key, Home: exp, Label: codexAccountLabel(a.Key)})
		}
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
