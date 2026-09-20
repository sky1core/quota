package agenthooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type InstructionHook struct {
	Event string
	Owned bool
}

func OwnsInstructionCommand(command, executable, agent, event string) bool {
	if ownsCurrentInstructionCommand(command, executable, agent, event) {
		return true
	}
	legacy := map[string]string{"claude/SessionStart": "json SessionStart CLAUDE.md CLAUDE.local.md . claude-session", "claude/WorktreeCreate": "claude-worktree-create", "claude/WorktreeRemove": "claude-worktree-remove", "codex/SessionStart": "json SessionStart AGENTS.md - . codex-session", "codex/SubagentStart": "json SubagentStart AGENTS.md - . codex-subagent"}
	suffix, ok := legacy[agent+"/"+event]
	return ok && command == `sh "$HOME/.local/bin/agents-overlay-context" `+suffix
}
func ownsCurrentInstructionCommand(command, executable, agent, event string) bool {
	inv, ok := parseDirectShellInvocation(command)
	if !ok {
		return false
	}
	argv := inv.Argv
	if len(argv) < 6 || !sameExecutable(argv[0], executable) {
		return false
	}
	if argv[1] == "agent" && argv[2] == "instructions" && (argv[3] == "_prepare" || argv[3] == "_hook") {
		return ownsInstructionPrepareArgs(argv[4:], agent, event)
	}
	return argv[1] == "agent" &&
		argv[2] == "overlay" &&
		argv[3] == "hook" &&
		len(argv) == 6 &&
		argv[4] == "--runtime="+agent &&
		argv[5] == "--event="+event
}

func ownsInstructionPrepareArgs(args []string, agent, event string) bool {
	seenAgent, seenEvent := false, false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--agent="+agent:
			seenAgent = true
		case arg == "--event="+event:
			seenEvent = true
		case strings.HasPrefix(arg, "--claude-config-dir="), strings.HasPrefix(arg, "--codex-home="):
		case arg == "--claude-config-dir", arg == "--codex-home":
			i++
			if i >= len(args) || args[i] == "" {
				return false
			}
		default:
			return false
		}
	}
	return seenAgent && seenEvent
}
func sameExecutable(a, b string) bool {
	if a == b {
		return true
	}
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return false
	}
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return os.SameFile(aInfo, bInfo)
}
func SuspiciousInstructionCommand(command string, knownExecutables ...string) bool {
	invocations, err := ParseShellInvocations(strings.NewReplacer("$HOME", "/placeholder-home", "${HOME}", "/placeholder-home").Replace(command))
	if err != nil {
		return false
	}
	for _, inv := range invocations {
		argv := inv.Argv
		if len(argv) == 0 {
			continue
		}
		quotaExecutable := filepath.Base(argv[0]) == "quota-cli"
		for _, known := range knownExecutables {
			if argv[0] == known {
				quotaExecutable = true
				break
			}
		}
		if quotaExecutable && len(argv) >= 4 && argv[1] == "agent" && argv[2] == "instructions" && (argv[3] == "_prepare" || argv[3] == "_hook") {
			return true
		}
		if quotaExecutable && len(argv) >= 4 && argv[1] == "agent" && argv[2] == "overlay" && argv[3] == "hook" {
			return true
		}
		if filepath.Base(argv[0]) == "agents-overlay-context" {
			return true
		}
		if len(argv) > 1 && (filepath.Base(argv[0]) == "sh" || filepath.Base(argv[0]) == "bash") && filepath.Base(argv[1]) == "agents-overlay-context" {
			return true
		}
	}
	return false
}
func instructionHookArray(v any) ([]any, bool) {
	switch a := v.(type) {
	case []any:
		return a, true
	case []map[string]any:
		r := make([]any, len(a))
		for n := range a {
			r[n] = a[n]
		}
		return r, true
	default:
		return nil, false
	}
}
func ClaudeInstructionHooks(root map[string]any, executable string) ([]InstructionHook, error) {
	return instructionHooks(root, executable, "claude")
}
func CodexInstructionHooks(root map[string]any, executable string) ([]InstructionHook, error) {
	return instructionHooks(root, executable, "codex")
}
func instructionHooks(root map[string]any, executable, agent string) ([]InstructionHook, error) {
	raw, exists := root["hooks"]
	if !exists {
		return nil, nil
	}
	hooks, valid := raw.(map[string]any)
	if !valid {
		return nil, fmt.Errorf("hooks must be an object")
	}
	entries := []InstructionHook{}
	for event, raw := range hooks {
		if agent == "codex" && event == "state" {
			if err := ValidateCodexHookState(raw); err != nil {
				return nil, err
			}
			continue
		}
		groups, valid := instructionHookArray(raw)
		if !valid {
			return nil, fmt.Errorf("hooks.%s must be an array", event)
		}
		for _, rawGroup := range groups {
			group, valid := rawGroup.(map[string]any)
			if !valid {
				return nil, fmt.Errorf("hooks.%s group must be an object", event)
			}
			commands, valid := instructionHookArray(group["hooks"])
			if !valid {
				return nil, fmt.Errorf("hooks.%s group hooks must be an array", event)
			}
			for _, rawHook := range commands {
				hook, valid := rawHook.(map[string]any)
				if !valid {
					return nil, fmt.Errorf("hooks.%s hook must be an object", event)
				}
				command, _ := hook["command"].(string)
				owned := OwnsInstructionCommand(command, executable, agent, event)
				if owned || SuspiciousInstructionCommand(command, executable) {
					entries = append(entries, InstructionHook{Event: event, Owned: owned})
				}
			}
		}
	}
	return entries, nil
}
func ValidateCodexHookState(raw any) error {
	states, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("hooks.state must be an object")
	}
	for key, rawState := range states {
		state, ok := rawState.(map[string]any)
		if !ok {
			return fmt.Errorf("hooks.state.%s must be an object", key)
		}
		if value, exists := state["enabled"]; exists {
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("hooks.state.%s.enabled must be a boolean", key)
			}
		}
		if value, exists := state["trusted_hash"]; exists {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("hooks.state.%s.trusted_hash must be a string", key)
			}
		}
	}
	return nil
}
