package agentoverlay

import "strings"

// Per-entry presence status.
const (
	StatusPresent  = "present"
	StatusMissing  = "missing"
	StatusStale    = "stale"    // Claude: a managed entry differs or a replaced command remains
	StatusMismatch = "mismatch" // Codex: a setting value or hook differs from the spec
)

// Per-runtime doctor/verify state.
const (
	StateInstalled    = "installed" // supported entries match with no inspected blocker
	StateEnforced     = "enforced"  // the runtime's verify command exited 0 (verify only)
	StateDegraded     = "degraded"
	StateUnconfigured = "unconfigured"
	StateError        = "error"
)

type EntryStatus struct {
	Event   string `json:"event"`
	Command string `json:"command"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
}

type SettingStatus struct {
	Key    string `json:"key"`
	Status string `json:"status"`
}

// RuntimePlan is the plan view for one runtime: target path plus per-entry
// status. It never modifies files.
type RuntimePlan struct {
	Runtime    string          `json:"runtime"`
	Path       string          `json:"path"`
	Configured bool            `json:"configured"`
	Reasons    []string        `json:"reasons,omitempty"`
	Settings   []SettingStatus `json:"settings,omitempty"`
	Entries    []EntryStatus   `json:"entries,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// RuntimeDoctor is the doctor/verify verdict for one runtime.
type RuntimeDoctor struct {
	Runtime      string          `json:"runtime"`
	Path         string          `json:"path"`
	State        string          `json:"state"`
	Reason       string          `json:"reason,omitempty"`
	Settings     []SettingStatus `json:"settings,omitempty"`
	Missing      []EntryStatus   `json:"missing,omitempty"`
	Snippet      string          `json:"snippet,omitempty"`
	Replacements []string        `json:"replacements,omitempty"`
	Verify       *LiveResult     `json:"verify,omitempty"`
	Error        string          `json:"error,omitempty"`
}

func objectAt(root map[string]any, key string) map[string]any {
	if v, ok := root[key].(map[string]any); ok {
		return v
	}
	obj := map[string]any{}
	root[key] = obj
	return obj
}

func hookGroups(v any) []any {
	if groups, ok := v.([]any); ok {
		return groups
	}
	return []any{}
}

func doctorFromPlan(plan RuntimePlan) RuntimeDoctor {
	doc := RuntimeDoctor{Runtime: plan.Runtime, Path: plan.Path, State: StateInstalled}
	if !plan.Configured {
		doc.State = StateUnconfigured
		return doc
	}
	if plan.Error != "" {
		doc.State = StateError
		doc.Error = plan.Error
		return doc
	}
	reasons := append([]string{}, plan.Reasons...)
	for _, entry := range plan.Entries {
		if entry.Status != StatusPresent {
			doc.Missing = append(doc.Missing, entry)
			reasons = append(reasons, entry.Event+": "+entry.Reason)
		}
	}
	for _, setting := range plan.Settings {
		if setting.Status != StatusPresent {
			doc.Settings = append(doc.Settings, setting)
			reasons = append(reasons, setting.Key+": "+setting.Status)
		}
	}
	if len(reasons) > 0 {
		doc.State = StateDegraded
		doc.Reason = strings.Join(reasons, "; ")
	}
	return doc
}
