package agentoverlay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

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

// PlanCodex reports the per-setting and per-entry status of config.toml without
// modifying it. On a TOML parse error it returns a plan carrying Error.
func PlanCodex(spec *Spec) RuntimePlan {
	plan := RuntimePlan{Runtime: "codex", Path: CodexConfigPath(), Configured: spec.Codex != nil}
	if spec.Codex == nil {
		return plan
	}
	root, err := readCodexConfig(plan.Path)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	for _, key := range sortedKeys(spec.Codex.Settings) {
		plan.Settings = append(plan.Settings, SettingStatus{
			Key:    key,
			Status: codexSettingStatus(root, key, spec.Codex.Settings[key]),
		})
	}
	for _, event := range sortedKeys(spec.Codex.Hooks) {
		for _, e := range spec.Codex.Hooks[event] {
			plan.Entries = append(plan.Entries, EntryStatus{Event: event, Command: e.Command, Status: codexHookStatus(root, event, e)})
		}
	}
	return plan
}

// DoctorCodex classifies the Codex runtime and, when degraded, builds the TOML
// snippet the user must add to config.toml by hand. It never writes.
func DoctorCodex(spec *Spec) RuntimeDoctor {
	doc := RuntimeDoctor{Runtime: "codex", Path: CodexConfigPath()}
	if spec.Codex == nil {
		doc.State = StateUnconfigured
		return doc
	}
	root, err := readCodexConfig(doc.Path)
	if err != nil {
		doc.State = StateError
		doc.Error = err.Error()
		return doc
	}
	addSettings := map[string]any{}
	for _, key := range sortedKeys(spec.Codex.Settings) {
		status := codexSettingStatus(root, key, spec.Codex.Settings[key])
		if status == StatusPresent {
			continue
		}
		doc.Settings = append(doc.Settings, SettingStatus{Key: key, Status: status})
		if status == StatusMismatch {
			doc.Replacements = append(doc.Replacements,
				fmt.Sprintf("replace the value of %s with %s", key, tomlValueString(spec.Codex.Settings[key])))
		} else {
			addSettings[key] = spec.Codex.Settings[key]
		}
	}
	addHooks := map[string][]HookEntry{}
	for _, event := range sortedKeys(spec.Codex.Hooks) {
		for _, e := range spec.Codex.Hooks[event] {
			status := codexHookStatus(root, event, e)
			if status == StatusPresent {
				continue
			}
			doc.Missing = append(doc.Missing, EntryStatus{Event: event, Command: e.Command, Status: status})
			if status == StatusMismatch {
				doc.Replacements = append(doc.Replacements,
					fmt.Sprintf("replace the %s hook %q to set additionalContextLimit=%s", event, e.Command, contextLimitString(e)))
			} else {
				addHooks[event] = append(addHooks[event], e)
			}
		}
	}
	if len(doc.Settings) == 0 && len(doc.Missing) == 0 {
		doc.State = StateInstalled
		return doc
	}
	doc.State = StateDegraded
	doc.Reason = "one or more settings or hooks are missing or mismatched"
	if len(addSettings) > 0 || len(addHooks) > 0 {
		doc.Snippet = codexSnippet(addSettings, addHooks)
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

func codexHookStatus(root map[string]any, event string, e HookEntry) string {
	hooks, ok := root["hooks"].(map[string]any)
	if !ok {
		return StatusMissing
	}
	status := StatusMissing
	for _, group := range tomlTableArray(hooks[event]) {
		for _, table := range tomlTableArray(group["hooks"]) {
			if t, _ := table["type"].(string); t != "command" {
				continue
			}
			cmd, ok := table["command"].(string)
			if !ok || cmd != e.Command {
				continue
			}
			if e.AdditionalContextLimit == nil || settingsValueEqual(*e.AdditionalContextLimit, table["additionalContextLimit"]) {
				return StatusPresent
			}
			status = StatusMismatch
		}
	}
	return status
}

// tomlValueString renders a spec value as it would appear on the right-hand side
// of a config.toml assignment, for the "replace the value of ..." guidance.
func tomlValueString(v any) string {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{"v": normalizeForTOML(v)}); err != nil {
		return fmt.Sprintf("%v", v)
	}
	line := strings.TrimSpace(buf.String())
	return strings.TrimSpace(strings.TrimPrefix(line, "v ="))
}

func contextLimitString(e HookEntry) string {
	if e.AdditionalContextLimit == nil {
		return "unset"
	}
	return fmt.Sprintf("%d", *e.AdditionalContextLimit)
}

func tomlTableArray(v any) []map[string]any {
	switch arr := v.(type) {
	case []map[string]any:
		return arr
	case []any:
		out := make([]map[string]any, 0, len(arr))
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// settingsValueEqual compares a spec value against a TOML-decoded value by their
// JSON encodings, so a JSON number (float64) and its TOML integer counterpart
// (int64) compare equal without a hand-written numeric ladder.
func settingsValueEqual(spec, actual any) bool {
	sb, err1 := json.Marshal(spec)
	ab, err2 := json.Marshal(actual)
	return err1 == nil && err2 == nil && bytes.Equal(sb, ab)
}

func codexSnippet(settings map[string]any, hooks map[string][]HookEntry) string {
	doc := map[string]any{}
	for key, value := range settings {
		doc[key] = normalizeForTOML(value)
	}
	if len(hooks) > 0 {
		hooksMap := map[string]any{}
		for event, entries := range hooks {
			groups := make([]map[string]any, 0, len(entries))
			for _, e := range entries {
				table := map[string]any{"type": "command", "command": e.Command}
				if e.AdditionalContextLimit != nil {
					table["additionalContextLimit"] = *e.AdditionalContextLimit
				}
				groups = append(groups, map[string]any{"hooks": []map[string]any{table}})
			}
			hooksMap[event] = groups
		}
		doc["hooks"] = hooksMap
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return ""
	}
	return buf.String()
}

// normalizeForTOML rewrites integral JSON floats to int64 so the emitted snippet
// shows integer settings as integers rather than "32768.0".
func normalizeForTOML(v any) any {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && !math.IsInf(x, 0) {
			return int64(x)
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalizeForTOML(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = normalizeForTOML(e)
		}
		return out
	}
	return v
}
