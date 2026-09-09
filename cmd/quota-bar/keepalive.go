package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/idle"
	"github.com/sky1core/quota/internal/keepalive"
)

func changeKeepalive(service *keepalive.Service, current settings, next keepalive.Config, now time.Time) (settings, error) {
	if err := next.Validate(); err != nil {
		return current, err
	}
	if !next.Enabled {
		service.Stop()
		current.Keepalive = &next
	}
	updated := current
	updated.Keepalive = &next
	if err := saveSettings(updated); err != nil {
		return current, err
	}
	if err := service.Configure(next, now); err != nil {
		return current, err
	}
	return updated, nil
}

func newKeepaliveService(claudeAccounts []config.ResolvedAccount, codexAccounts []config.ResolvedCodexAccount) *keepalive.Service {
	home, _ := os.UserHomeDir()
	var accounts []keepalive.Account
	for _, a := range claudeAccounts {
		dir := a.ConfigDir
		if dir == "" {
			dir = os.Getenv("CLAUDE_CONFIG_DIR")
			if dir == "" {
				dir = filepath.Join(home, ".claude")
			}
		}
		accounts = append(accounts, keepalive.Account{Provider: "claude", Key: a.Key, Home: dir})
	}
	for _, a := range codexAccounts {
		dir := a.Home
		if dir == "" {
			dir = os.Getenv("CODEX_HOME")
			if dir == "" {
				dir = filepath.Join(home, ".codex")
			}
		}
		accounts = append(accounts, keepalive.Account{Provider: "codex", Key: a.Key, Home: dir})
	}
	return keepalive.NewService(&keepalive.Runtime{Accounts: accounts}, filepath.Join(filepath.Dir(settingsPath()), "keepalive-state.json"), idle.Seconds)
}

func keepaliveResultText(result keepalive.Result) (string, string) {
	title := fmt.Sprintf("Keepalive: %s · %d/%d replies", result.Status, result.Confirmed, result.Selected)
	if result.Confirmed > 0 {
		title += fmt.Sprintf(" · cache: %d reused, %d miss, %d unknown", result.CacheHits, result.CacheMisses, result.CacheUnknown)
	}
	if result.Error != nil {
		title += " — see log"
	}
	return title, strings.Join(result.CacheDetails, "\n")
}
