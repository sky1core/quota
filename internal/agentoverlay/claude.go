package agentoverlay

import "github.com/sky1core/quota/internal/agenthooks"

// ClaudeSettingsPath is the Claude settings.json target
// (CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json), shared with
// the agenthooks installer.
func ClaudeSettingsPath() string {
	return agenthooks.ClaudeSettingsPath()
}

// PlanClaude reports the per-entry status of the Claude hooks without touching
// the settings file. On a parse error it returns a plan carrying Error.
func PlanClaude(spec *Spec) RuntimePlan {
	plan := RuntimePlan{Runtime: "claude", Path: ClaudeSettingsPath(), Configured: spec.Claude != nil}
	if spec.Claude == nil {
		return plan
	}
	root, err := agenthooks.ReadJSONObject(plan.Path)
	if err != nil {
		plan.Error = err.Error()
		return plan
	}
	hooksObj := objectAt(root, "hooks")
	for _, event := range sortedKeys(spec.Claude.Hooks) {
		for _, e := range spec.Claude.Hooks[event] {
			plan.Entries = append(plan.Entries, EntryStatus{
				Event:   event,
				Command: e.Command,
				Status:  claudeEntryStatus(hooksObj, event, e, spec.Claude.Replaces),
			})
		}
	}
	return plan
}

// ApplyClaude installs every spec hook entry into settings.json, replacing any
// managed entry (exact match with a spec command or a claude.replaces command)
// and preserving all other entries. The file is backed up before an atomic
// rewrite.
func ApplyClaude(spec *Spec) (RuntimePlan, error) {
	plan := RuntimePlan{Runtime: "claude", Path: ClaudeSettingsPath(), Configured: spec.Claude != nil}
	if spec.Claude == nil {
		return plan, nil
	}
	root, err := agenthooks.ReadJSONObject(plan.Path)
	if err != nil {
		return plan, err
	}
	hooksObj := objectAt(root, "hooks")
	for _, event := range sortedKeys(spec.Claude.Hooks) {
		entries := spec.Claude.Hooks[event]
		managed := managedCommandSet(entries, spec.Claude.Replaces)
		groups := removeManagedGroups(hookGroups(hooksObj[event]), managed)
		for _, e := range entries {
			groups = append(groups, claudeHookGroup(e))
		}
		hooksObj[event] = groups
	}
	root["hooks"] = hooksObj
	if err := agenthooks.WriteJSONObjectWithBackup(plan.Path, root); err != nil {
		return plan, err
	}
	for _, event := range sortedKeys(spec.Claude.Hooks) {
		for _, e := range spec.Claude.Hooks[event] {
			plan.Entries = append(plan.Entries, EntryStatus{Event: event, Command: e.Command, Status: StatusPresent})
		}
	}
	return plan, nil
}

// DoctorClaude classifies the Claude runtime as installed/degraded/unconfigured/
// error. installed is the highest state doctor reports; enforced is decided only
// by verify. A settings-root disableAllHooks:true keeps the runtime degraded even
// when every entry is present, because the hooks would not run.
func DoctorClaude(spec *Spec) RuntimeDoctor {
	doc := RuntimeDoctor{Runtime: "claude", Path: ClaudeSettingsPath()}
	if spec.Claude == nil {
		doc.State = StateUnconfigured
		return doc
	}
	root, err := agenthooks.ReadJSONObject(doc.Path)
	if err != nil {
		doc.State = StateError
		doc.Error = err.Error()
		return doc
	}
	hooksObj := objectAt(root, "hooks")
	for _, event := range sortedKeys(spec.Claude.Hooks) {
		for _, e := range spec.Claude.Hooks[event] {
			status := claudeEntryStatus(hooksObj, event, e, spec.Claude.Replaces)
			if status != StatusPresent {
				doc.Missing = append(doc.Missing, EntryStatus{Event: event, Command: e.Command, Status: status})
			}
		}
	}
	if disabled, _ := root["disableAllHooks"].(bool); disabled {
		doc.State = StateDegraded
		doc.Reason = "disableAllHooks is true; hooks will not run"
		return doc
	}
	if len(doc.Missing) > 0 {
		doc.State = StateDegraded
		doc.Reason = "one or more hook entries are missing or stale"
		return doc
	}
	doc.State = StateInstalled
	return doc
}

func claudeHookGroup(e HookEntry) map[string]any {
	return map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": e.Command}},
	}
}

// claudeEntryStatus reports present when the exact spec command is installed for
// the event, stale when a claude.replaces command is present (so apply will
// replace it), and missing otherwise. Matching is by exact string only.
func claudeEntryStatus(hooksObj map[string]any, event string, e HookEntry, replaces []string) string {
	replaceSet := map[string]bool{}
	for _, r := range replaces {
		replaceSet[r] = true
	}
	foundReplace := false
	for _, group := range hookGroups(hooksObj[event]) {
		groupMap, ok := group.(map[string]any)
		if !ok {
			continue
		}
		hooks, ok := groupMap["hooks"].([]any)
		if !ok {
			continue
		}
		// A group matcher restricts when its hooks run, so an entry inside a
		// matcher-scoped group never counts as the unrestricted spec entry.
		restricted := false
		if m, exists := groupMap["matcher"]; exists {
			if s, isString := m.(string); !isString || s != "" {
				restricted = true
			}
		}
		for _, hook := range hooks {
			cmd, ok := hookAnyFormCommand(hook)
			if !ok {
				continue
			}
			if cmd == e.Command {
				// Only the exact object form this installer writes, in an
				// unrestricted group, counts as present; string entries and
				// malformed or scoped variants are stale until apply
				// normalizes them, so status never overclaims what runs.
				if _, strict := hookCommandString(hook); strict {
					if _, isString := hook.(string); !isString && !restricted {
						return StatusPresent
					}
				}
				foundReplace = true
				continue
			}
			if replaceSet[cmd] {
				foundReplace = true
			}
		}
	}
	if foundReplace {
		return StatusStale
	}
	return StatusMissing
}

// managedCommandSet is the set of command strings this spec owns for one event:
// the event's spec commands plus every claude.replaces command. Only these exact
// strings are removed on apply.
func managedCommandSet(entries []HookEntry, replaces []string) map[string]bool {
	managed := map[string]bool{}
	for _, e := range entries {
		managed[e.Command] = true
	}
	for _, r := range replaces {
		managed[r] = true
	}
	return managed
}

// removeManagedGroups drops every hook whose command string is in managed.
// Groups emptied by the removal are dropped; all other entries are preserved.
func removeManagedGroups(groups []any, managed map[string]bool) []any {
	var out []any
	for _, group := range groups {
		groupMap, ok := group.(map[string]any)
		if !ok {
			out = append(out, group)
			continue
		}
		hooks, ok := groupMap["hooks"].([]any)
		if !ok {
			out = append(out, group)
			continue
		}
		filtered := make([]any, 0, len(hooks))
		for _, hook := range hooks {
			if cmd, ok := hookAnyFormCommand(hook); ok && managed[cmd] {
				continue
			}
			filtered = append(filtered, hook)
		}
		if len(filtered) == 0 {
			continue
		}
		next := make(map[string]any, len(groupMap))
		for k, v := range groupMap {
			next[k] = v
		}
		next["hooks"] = filtered
		out = append(out, next)
	}
	return out
}

// hookAnyFormCommand reads the command of a hook entry in any shape (plain
// string, or an object with a command field regardless of its type). Ownership
// removal uses this lenient form so a malformed variant of a managed command is
// cleaned up rather than left beside the fresh entry.
func hookAnyFormCommand(hook any) (string, bool) {
	switch v := hook.(type) {
	case string:
		return v, true
	case map[string]any:
		cmd, ok := v["command"].(string)
		return cmd, ok
	default:
		return "", false
	}
}

// hookCommandString reads the command of a hook entry. Claude accepts a hook as
// a plain string or as an object; an object counts only when its type is
// "command", matching the entries this installer writes.
func hookCommandString(hook any) (string, bool) {
	switch v := hook.(type) {
	case string:
		return v, true
	case map[string]any:
		if t, ok := v["type"].(string); !ok || t != "command" {
			return "", false
		}
		cmd, ok := v["command"].(string)
		return cmd, ok
	default:
		return "", false
	}
}
