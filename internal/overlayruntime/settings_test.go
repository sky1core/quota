package overlayruntime

import "testing"

func TestClaudeInstructionModeLegacyPriority(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want string
		ok   bool
	}{
		{
			name: "legacy applies when new mode is explicit default",
			data: claudeModeTestData(map[string]any{
				"instructionFiles":    "claude-md-or-agents-md",
				"projectInstructions": "claude",
			}),
			want: claudeModeClaudeOnly,
			ok:   true,
		},
		{
			name: "legacy none applies when new mode is explicit default",
			data: claudeModeTestData(map[string]any{
				"instructionFiles":    "claude-md-or-agents-md",
				"projectInstructions": "none",
			}),
			want: claudeModeManagedOnly,
			ok:   true,
		},
		{
			name: "non-default new mode wins",
			data: claudeModeTestData(map[string]any{
				"instructionFiles":    "claude-md-and-agents-md",
				"projectInstructions": "none",
			}),
			want: claudeModeClaudeAnd,
			ok:   true,
		},
		{
			name: "legacy alone",
			data: claudeModeTestData(map[string]any{"projectInstructions": "both"}),
			want: claudeModeClaudeAnd,
			ok:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := claudeInstructionMode(tt.data)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("claudeInstructionMode = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func claudeModeTestData(options map[string]any) map[string]any {
	return map[string]any{
		"pluginConfigs": map[string]any{
			"agents-md@builtin": map[string]any{
				"options": options,
			},
		},
	}
}
