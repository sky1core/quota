package main

import "testing"

func TestAutoPromptEnvRemovesClaudeEffortOverride(t *testing.T) {
	base := []string{"CLAUDE_CODE_EFFORT_LEVEL=low", "CLAUDE_CONFIG_DIR=/old", "KEEP=value"}
	got := envMap(autoPromptEnv(autoPromptAccount{provider: "claude", dir: "/selected"}, base))
	if _, exists := got["CLAUDE_CODE_EFFORT_LEVEL"]; exists {
		t.Fatal("inherited effort can override requested effort")
	}
	if got["CLAUDE_CONFIG_DIR"] != "/selected" || got["KEEP"] != "value" {
		t.Fatalf("wrong child environment: %v", got)
	}
	if base[0] != "CLAUDE_CODE_EFFORT_LEVEL=low" {
		t.Fatal("parent environment was modified")
	}
}
