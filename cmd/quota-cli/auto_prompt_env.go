package main

import (
	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
)

func autoPromptEnv(account autoPromptAccount, base []string) []string {
	if account.provider == "codex" {
		return codex.EnvForHome(base, account.dir)
	}
	return claude.EnvForConfigDir(base, account.dir)
}
