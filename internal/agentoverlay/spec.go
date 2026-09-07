// Package agentoverlay installs and verifies instruction-overlay hook and
// settings entries for the Claude and Codex agent CLIs from a user-owned spec
// file. quota-cli supplies only the generic machinery; the concrete command
// strings live in the spec file, never in this package.
package agentoverlay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const SpecVersion = 1

// Spec is the parsed agent-overlay.json. Each runtime section is optional; an
// omitted runtime is reported as unconfigured rather than filled from a default.
type Spec struct {
	Version int         `json:"version"`
	Claude  *ClaudeSpec `json:"claude,omitempty"`
	Codex   *CodexSpec  `json:"codex,omitempty"`
	Verify  *VerifySpec `json:"verify,omitempty"`
}

// ClaudeSpec holds the Claude hook entries and the exact command strings a prior
// spec version installed. Replaces is compared with == only; there is no
// prefix/pattern/argv[0] interpretation.
type ClaudeSpec struct {
	Hooks    map[string][]HookEntry `json:"hooks,omitempty"`
	Replaces []string               `json:"replaces,omitempty"`
}

type CodexSpec struct {
	Settings map[string]any         `json:"settings,omitempty"`
	Hooks    map[string][]HookEntry `json:"hooks,omitempty"`
}

// HookEntry is a single hook command for one event. AdditionalContextLimit is a
// Codex-only field, serialized only when the spec sets it.
type HookEntry struct {
	Command                string `json:"command"`
	AdditionalContextLimit *int   `json:"additionalContextLimit,omitempty"`
}

// VerifySpec carries the per-runtime live verification commands. A runtime is
// enforced only when its command exits 0; a missing command leaves that runtime
// degraded.
type VerifySpec struct {
	Claude *VerifyRuntimeSpec `json:"claude,omitempty"`
	Codex  *VerifyRuntimeSpec `json:"codex,omitempty"`
}

type VerifyRuntimeSpec struct {
	Command []string `json:"command"`
}

// DefaultSpecPath is the spec location used when --spec is not given.
func DefaultSpecPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "agent-overlay.json")
}

// LoadSpec reads and validates the spec at path. A missing file is an explicit
// error: no runtime is filled from an implicit default.
func LoadSpec(path string) (*Spec, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("agent overlay spec not found: %s", path)
		}
		return nil, err
	}
	var spec Spec
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	// dec.More() misses trailing "]"/"}", so probe with a second decode: only
	// pure whitespace after the spec object yields io.EOF.
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("%s: trailing content after spec object", path)
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &spec, nil
}

func (s Spec) Validate() error {
	if s.Version != SpecVersion {
		return fmt.Errorf("unsupported agent overlay spec version %d", s.Version)
	}
	if s.Claude != nil {
		if err := validateHooks("claude", s.Claude.Hooks); err != nil {
			return err
		}
		managed := map[string]bool{}
		for _, entries := range s.Claude.Hooks {
			for _, e := range entries {
				managed[e.Command] = true
			}
		}
		for _, r := range s.Claude.Replaces {
			if strings.TrimSpace(r) == "" {
				return errors.New("claude.replaces has an empty command")
			}
			if managed[r] {
				return fmt.Errorf("claude.replaces command %q is also a claude hook command", r)
			}
		}
	}
	if s.Codex != nil {
		if err := validateHooks("codex", s.Codex.Hooks); err != nil {
			return err
		}
		for key, value := range s.Codex.Settings {
			if _, err := normalizeForTOML(value); err != nil {
				return fmt.Errorf("codex.settings.%s: %w", key, err)
			}
			if strings.TrimSpace(key) == "" {
				return errors.New("codex settings has an empty key")
			}
		}
	}
	if s.Codex != nil && len(s.Codex.Hooks) > 0 {
		if _, exists := s.Codex.Settings["hooks"]; exists {
			return errors.New("codex.settings.hooks conflicts with codex.hooks")
		}
	}
	if s.Verify != nil {
		if err := validateVerifyRuntime("claude", s.Verify.Claude); err != nil {
			return err
		}
		if err := validateVerifyRuntime("codex", s.Verify.Codex); err != nil {
			return err
		}
	}
	return nil
}

func validateVerifyRuntime(name string, v *VerifyRuntimeSpec) error {
	if v == nil {
		return nil
	}
	if len(v.Command) == 0 || strings.TrimSpace(v.Command[0]) == "" {
		return fmt.Errorf("verify.%s.command must have a non-empty argv[0]", name)
	}
	return nil
}

func validateHooks(runtime string, hooks map[string][]HookEntry) error {
	for event, entries := range hooks {
		if strings.TrimSpace(event) == "" {
			return fmt.Errorf("%s hooks has an empty event name", runtime)
		}
		seen := map[string]bool{}
		for _, e := range entries {
			if seen[e.Command] {
				return fmt.Errorf("%s hook %q has a duplicate command", runtime, event)
			}
			seen[e.Command] = true
			if e.AdditionalContextLimit != nil {
				if runtime == "claude" {
					return fmt.Errorf("claude hook %q does not support additionalContextLimit", event)
				}
				if *e.AdditionalContextLimit < 0 {
					return fmt.Errorf("%s hook %q additionalContextLimit must be nonnegative", runtime, event)
				}
			}
			if strings.TrimSpace(e.Command) == "" {
				return fmt.Errorf("%s hook %q has an empty command", runtime, event)
			}
		}
	}
	return nil
}

// InitTemplate returns a placeholder spec that documents the schema. Every
// command is a /path/to/overlay-hook placeholder the user must replace.
func InitTemplate() Spec {
	zero := 0
	return Spec{
		Version: SpecVersion,
		Claude: &ClaudeSpec{
			Hooks: map[string][]HookEntry{
				"SessionStart":   {{Command: "/path/to/overlay-hook session"}},
				"WorktreeCreate": {{Command: "/path/to/overlay-hook worktree-create"}},
			},
			Replaces: []string{"/path/to/overlay-hook session --previous"},
		},
		Codex: &CodexSpec{
			Settings: map[string]any{
				"project_doc_max_bytes":          32768,
				"project_doc_fallback_filenames": []any{},
			},
			Hooks: map[string][]HookEntry{
				"SessionStart": {{Command: "/path/to/overlay-hook codex-session", AdditionalContextLimit: &zero}},
			},
		},
		Verify: &VerifySpec{
			Claude: &VerifyRuntimeSpec{Command: []string{"/path/to/overlay-hook", "verify", "claude"}},
			Codex:  &VerifyRuntimeSpec{Command: []string{"/path/to/overlay-hook", "verify", "codex"}},
		},
	}
}

// SaveSpec writes spec to path atomically. An existing file is refused unless
// force is set.
func SaveSpec(path string, spec Spec, force bool) (string, error) {
	if err := spec.Validate(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil && !force {
		return "", fmt.Errorf("%s already exists", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", err
	}
	b = append(b, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", err
	}
	return path, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
