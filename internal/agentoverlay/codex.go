package agentoverlay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// CodexConfigPath is the Codex config.toml target (CODEX_HOME/config.toml, else
// ~/.codex/config.toml). It is only ever read, never rewritten.
func CodexConfigPath() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return filepath.Join(dir, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

func PlanCodex(spec *Spec) RuntimePlan {
	root, err := readCodexConfig(CodexConfigPath())
	plan := inspectCodex(spec, root)
	if spec.Codex != nil && err != nil {
		plan.Error = err.Error()
	}
	return plan
}

func inspectCodex(spec *Spec, root map[string]any) RuntimePlan {
	plan := RuntimePlan{Runtime: "codex", Path: CodexConfigPath(), Configured: spec.Codex != nil}
	if spec.Codex == nil {
		return plan
	}
	if err := spec.Validate(); err != nil {
		plan.Error = err.Error()
		return plan
	}
	expected, err := expectedCodexConfig(spec.Codex.Settings, spec.Codex.Hooks)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	if hasCodexHookEntries(expected) {
		if features, exists := root["features"]; exists {
			if obj, ok := features.(map[string]any); ok {
				_, hasCanonical := obj["hooks"]
				if alias, exists := obj["codex_hooks"]; hasCanonical && exists {
					if _, ok := alias.(bool); !ok {
						plan.Reasons = append(plan.Reasons, "features.codex_hooks must be a boolean")
					}
				}
				keys := []string{"hooks"}
				if !hasCanonical {
					keys = []string{"codex_hooks"}
				}
				for _, key := range keys {
					for _, reason := range blockingBool(obj, key, false) {
						plan.Reasons = append(plan.Reasons, "features."+reason)
					}
				}
			} else {
				plan.Reasons = append(plan.Reasons, "features must be a table")
			}
		}
	}
	for _, key := range sortedKeys(spec.Codex.Settings) {
		plan.Settings = append(plan.Settings, SettingStatus{Key: key, Status: codexSettingStatus(root, key, expected[key])})
	}
	hooks, _ := root["hooks"].(map[string]any)
	expectedHooks, _ := expected["hooks"].(map[string]any)
	for _, event := range sortedKeys(spec.Codex.Hooks) {
		for i, e := range spec.Codex.Hooks[event] {
			group := groupArray(expectedHooks[event])[i].(map[string]any)
			status, reason := inspectHookEntry(hooks[event], group, e.Command, nil)
			plan.Entries = append(plan.Entries, EntryStatus{Event: event, Command: e.Command, Status: status, Reason: reason})
		}
	}
	return plan
}

func hasCodexHookEntries(config map[string]any) bool {
	hooks, _ := config["hooks"].(map[string]any)
	for _, groups := range hooks {
		for _, rawGroup := range groupArray(groups) {
			group, _ := rawGroup.(map[string]any)
			if len(groupArray(group["hooks"])) > 0 {
				return true
			}
		}
	}
	return false
}

func DoctorCodex(spec *Spec) RuntimeDoctor {
	plan := PlanCodex(spec)
	doc := doctorFromPlan(plan)
	if doc.State != StateDegraded {
		return doc
	}
	addSettings := map[string]any{}
	for _, s := range doc.Settings {
		if s.Status == StatusMissing {
			addSettings[s.Key] = spec.Codex.Settings[s.Key]
		} else {
			snippet, err := codexSnippet(map[string]any{s.Key: spec.Codex.Settings[s.Key]}, nil)
			if err != nil {
				doc.State = StateError
				doc.Error = err.Error()
				return doc
			}
			doc.Replacements = append(doc.Replacements, fmt.Sprintf("replace the value of %s using (preserve unrelated settings):\n%s", s.Key, snippet))
		}
	}
	addHooks := map[string][]HookEntry{}
	for _, entry := range doc.Missing {
		if entry.Status == StatusMissing {
			for _, e := range spec.Codex.Hooks[entry.Event] {
				if e.Command == entry.Command {
					addHooks[entry.Event] = append(addHooks[entry.Event], e)
				}
			}
		} else {
			for _, e := range spec.Codex.Hooks[entry.Event] {
				if e.Command != entry.Command {
					continue
				}
				snippet, err := codexSnippet(nil, map[string][]HookEntry{entry.Event: {e}})
				if err != nil {
					doc.State = StateError
					doc.Error = err.Error()
					return doc
				}
				doc.Replacements = append(doc.Replacements, fmt.Sprintf("replace only the %s hook %q (%s); preserve other hooks in its group and install this separate group:\n%s", entry.Event, entry.Command, entry.Reason, snippet))
			}
		}
	}
	if len(addSettings) > 0 || len(addHooks) > 0 {
		snippet, err := codexSnippet(addSettings, addHooks)
		if err != nil {
			doc.State = StateError
			doc.Error = err.Error()
		} else {
			doc.Snippet = snippet
		}
	}
	return doc
}

func readCodexConfig(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, err
	}
	root := map[string]any{}
	if err := toml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	return root, nil
}

func codexSettingStatus(root map[string]any, key string, want any) string {
	got, ok := root[key]
	if !ok {
		return StatusMissing
	}
	if settingsValueEqual(want, got) {
		return StatusPresent
	}
	return StatusMismatch
}

func settingsValueEqual(spec, actual any) bool {
	sb, err1 := json.Marshal(spec)
	ab, err2 := json.Marshal(actual)
	return err1 == nil && err2 == nil && bytes.Equal(sb, ab)
}

func expectedCodexConfig(settings map[string]any, hooks map[string][]HookEntry) (map[string]any, error) {
	doc := map[string]any{}
	for key, value := range settings {
		normalized, err := normalizeForTOML(value)
		if err != nil {
			return nil, fmt.Errorf("codex.settings.%s: %w", key, err)
		}
		doc[key] = normalized
	}
	if len(hooks) > 0 {
		if _, exists := doc["hooks"]; exists {
			return nil, fmt.Errorf("codex.settings.hooks conflicts with codex.hooks")
		}
		hooksMap := map[string]any{}
		for event, entries := range hooks {
			groups := make([]any, 0, len(entries))
			for _, e := range entries {
				groups = append(groups, codexHookGroup(e))
			}
			hooksMap[event] = groups
		}
		doc["hooks"] = hooksMap
	}
	return doc, nil
}

func codexHookGroup(e HookEntry) map[string]any {
	hook := map[string]any{"type": "command", "command": e.Command}
	if e.AdditionalContextLimit != nil {
		hook["additionalContextLimit"] = *e.AdditionalContextLimit
	}
	return map[string]any{"hooks": []any{hook}}
}

func codexSnippet(settings map[string]any, hooks map[string][]HookEntry) (string, error) {
	doc, err := expectedCodexConfig(settings, hooks)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return "", err
	}
	return buf.String(), nil
}
