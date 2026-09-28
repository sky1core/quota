package agenthooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type InstructionCommandState int

const (
	InstructionNotManaged InstructionCommandState = iota
	InstructionCurrent
	InstructionOtherExecutable
	InstructionLegacy
)

func OwnsInstructionCommand(command, executable, agent, event string) bool {
	return InstructionCommand(command, executable, agent, event) != InstructionNotManaged
}

func InstructionCommand(command, executable, agent, event string) InstructionCommandState {
	if inv, ok := parseDirectShellInvocation(command); ok {
		argv := inv.Argv
		if len(argv) >= 6 && filepath.IsAbs(argv[0]) && argv[1] == "agent" {
			if argv[2] == "instructions" && (argv[3] == "_prepare" || argv[3] == "_hook") && ownsInstructionPrepareArgs(argv[4:], agent, event) {
				if len(argv) == 6 && argv[3] == "_prepare" && argv[4] == "--agent="+agent && argv[5] == "--event="+event {
					if sameExecutable(argv[0], executable) {
						return InstructionCurrent
					}
					return InstructionOtherExecutable
				}
				return InstructionLegacy
			}
			if argv[2] == "overlay" && argv[3] == "hook" && len(argv) == 6 && argv[4] == "--runtime="+agent && argv[5] == "--event="+event {
				return InstructionLegacy
			}
		}
	}
	legacy := map[string]string{"claude/SessionStart": "json SessionStart CLAUDE.md CLAUDE.local.md . claude-session", "claude/WorktreeCreate": "claude-worktree-create", "claude/WorktreeRemove": "claude-worktree-remove", "codex/SessionStart": "json SessionStart AGENTS.md - . codex-session", "codex/SubagentStart": "json SubagentStart AGENTS.md - . codex-subagent"}
	if suffix, ok := legacy[agent+"/"+event]; ok && command == `sh "$HOME/.local/bin/agents-overlay-context" `+suffix {
		return InstructionLegacy
	}
	return InstructionNotManaged
}

func instructionContract(argv []string) bool {
	return len(argv) >= 4 && instructionContractWords(argv[1], argv[2], argv[3])
}

func instructionContractWords(a, b, c string) bool {
	return a == "agent" && ((b == "instructions" && (c == "_prepare" || c == "_hook")) || (b == "overlay" && c == "hook"))
}

func mentionsInstructionContract(command string) bool {
	words := strings.Fields(strings.NewReplacer(`"`, "", `'`, "").Replace(command))
	for i, word := range words {
		if filepath.Base(word) == "agents-overlay-context" {
			return true
		}
		if i+2 < len(words) && instructionContractWords(word, words[i+1], words[i+2]) {
			return true
		}
	}
	return false
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
func SuspiciousInstructionCommand(command string) bool {
	invocations, err := ParseShellInvocations(strings.NewReplacer("$HOME", "/placeholder-home", "${HOME}", "/placeholder-home").Replace(command))
	if err != nil {
		return mentionsInstructionContract(command)
	}
	for _, inv := range invocations {
		if inv.DynamicCommand {
			if mentionsInstructionContract(strings.Join(inv.Argv, " ")) {
				return true
			}
			continue
		}
		if instructionContract(inv.Argv) {
			return true
		}
		for _, word := range inv.Argv {
			if filepath.Base(word) == "agents-overlay-context" {
				return true
			}
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
