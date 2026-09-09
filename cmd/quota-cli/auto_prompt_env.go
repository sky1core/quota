package main

import (
	"strings"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
)

func autoPromptEnv(account autoPromptAccount, base []string) []string {
	if account.provider == "codex" {
		return codex.EnvForHome(base, account.dir)
	}
	env := claude.EnvForConfigDir(base, account.dir)
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key != "CLAUDE_CODE_EFFORT_LEVEL" {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
