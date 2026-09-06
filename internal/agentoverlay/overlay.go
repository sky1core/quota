package agentoverlay

// Per-entry presence status.
const (
	StatusPresent  = "present"
	StatusMissing  = "missing"
	StatusStale    = "stale"    // Claude: a claude.replaces command is present and will be replaced
	StatusMismatch = "mismatch" // Codex: a setting value or hook differs from the spec
)

// Per-runtime doctor/verify state.
const (
	StateInstalled    = "installed" // config entries present with no impediment (doctor's highest state)
	StateEnforced     = "enforced"  // the runtime's verify command exited 0 (verify only)
	StateDegraded     = "degraded"
	StateUnconfigured = "unconfigured"
	StateError        = "error"
)

type EntryStatus struct {
	Event   string `json:"event"`
	Command string `json:"command"`
	Status  string `json:"status"`
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
