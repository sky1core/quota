package agentoverlay

import (
	"fmt"

	"github.com/sky1core/quota/internal/agenthooks"
)

func ClaudeSettingsPath() string { return agenthooks.ClaudeSettingsPath() }

func PlanClaude(spec *Spec) RuntimePlan {
	root, err := agenthooks.ReadJSONObject(ClaudeSettingsPath())
	plan := inspectClaude(spec, root)
	if spec.Claude != nil && err != nil {
		plan.Error = err.Error()
	}
	return plan
}

func ApplyClaude(spec *Spec) (RuntimePlan, error) {
	if spec.Claude == nil {
		return inspectClaude(spec, nil), nil
	}
	if err := spec.Validate(); err != nil {
		return RuntimePlan{}, err
	}
	root, err := agenthooks.UpdateJSONObjectWithBackup(ClaudeSettingsPath(), func(root map[string]any) error {
		if v, exists := root["hooks"]; exists {
			if _, ok := v.(map[string]any); !ok {
				return fmt.Errorf("hooks must be an object")
			}
		}
		hooks := objectAt(root, "hooks")
		for _, event := range sortedKeys(spec.Claude.Hooks) {
			if v, exists := hooks[event]; exists {
				if _, ok := v.([]any); !ok {
					return fmt.Errorf("hooks.%s must be an array", event)
				}
			}
			entries := spec.Claude.Hooks[event]
			groups := removeManagedGroups(hookGroups(hooks[event]), managedCommandSet(entries, spec.Claude.Replaces))
			for _, e := range entries {
				groups = append(groups, claudeHookGroup(e))
			}
			hooks[event] = groups
		}
		return nil
	})
	plan := inspectClaude(spec, root)
	if err != nil {
		plan.Error = err.Error()
		return plan, err
	}
	doc := doctorFromPlan(plan)
	if doc.State != StateInstalled {
		return plan, fmt.Errorf("overlay entries saved; inspection: %s%s", doc.Error, doc.Reason)
	}
	return plan, nil
}

func DoctorClaude(spec *Spec) RuntimeDoctor { return doctorFromPlan(PlanClaude(spec)) }

func inspectClaude(spec *Spec, root map[string]any) RuntimePlan {
	plan := RuntimePlan{Runtime: "claude", Path: ClaudeSettingsPath(), Configured: spec.Claude != nil}
	if spec.Claude == nil {
		return plan
	}
	if err := spec.Validate(); err != nil {
		plan.Error = err.Error()
		return plan
	}
	plan.Reasons = append(plan.Reasons, blockingBool(root, "disableAllHooks", true)...)
	plan.Reasons = append(plan.Reasons, blockingBool(root, "allowManagedHooksOnly", true)...)
	hooks, _ := root["hooks"].(map[string]any)
	for _, event := range sortedKeys(spec.Claude.Hooks) {
		for _, e := range spec.Claude.Hooks[event] {
			status, reason := inspectHookEntry(hooks[event], claudeHookGroup(e), e.Command, spec.Claude.Replaces)
			if status == StatusMismatch {
				status = StatusStale
			}
			plan.Entries = append(plan.Entries, EntryStatus{Event: event, Command: e.Command, Status: status, Reason: reason})
		}
	}
	return plan
}

func claudeHookGroup(e HookEntry) map[string]any {
	return map[string]any{"hooks": []any{map[string]any{"type": "command", "command": e.Command}}}
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
