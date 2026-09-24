package agenthooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
		case strings.HasPrefix(arg, "--part="):
			if part, err := strconv.Atoi(strings.TrimPrefix(arg, "--part=")); err != nil || part < 1 {
				return false
			}
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
