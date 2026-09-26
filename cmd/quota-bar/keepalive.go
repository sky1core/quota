package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/idle"
	"github.com/sky1core/quota/internal/keepalive"
)

const keepaliveQuiesceTimeout = 30 * time.Second

func keepaliveScheduledAt(c keepalive.Config, now time.Time) (time.Time, bool) {
	if !c.Enabled {
		return time.Time{}, false
	}
	allowed := false
	for _, weekday := range c.Weekdays {
		allowed = allowed || weekday == now.Weekday().String()[:3]
	}
	if !allowed {
		return time.Time{}, false
	}
	at, err := time.Parse("15:04", c.Time)
	if err != nil {
		return time.Time{}, false
	}
	return time.Date(now.Year(), now.Month(), now.Day(), at.Hour(), at.Minute(), 0, 0, now.Location()), true
}

func keepaliveNeedsAwake(c keepalive.Config, now time.Time, running bool, attemptedDay string) bool {
	if !c.Enabled {
		return false
	}
	if running {
		return true
	}
	window := time.Duration(c.ActivityMinutes) * time.Minute
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for ; !day.After(now.Add(window)); day = day.AddDate(0, 0, 1) {
		if day.Format("2006-01-02") <= attemptedDay {
			continue
		}
		scheduled, ok := keepaliveScheduledAt(c, day)
		if ok && !now.Before(scheduled.Add(-window)) && now.Sub(scheduled) <= 30*time.Second {
			return true
		}
	}
	return false
}

func keepaliveAttemptStarting(c keepalive.Config, now time.Time) bool {
	scheduled, ok := keepaliveScheduledAt(c, now)
	return ok && !now.Before(scheduled) && now.Sub(scheduled) <= 30*time.Second
}

type keepaliveTicks struct {
	mu      sync.Mutex
	active  int
	drained chan struct{}
	blocked bool
	ctx     context.Context
	cancel  context.CancelFunc
}

func newKeepaliveTicks(parent context.Context) *keepaliveTicks {
	ctx, cancel := context.WithCancel(parent)
	return &keepaliveTicks{ctx: ctx, cancel: cancel}
}

func (k *keepaliveTicks) begin() (ctx context.Context, done func(), ok bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.blocked {
		return nil, nil, false
	}
	if k.active == 0 {
		k.drained = make(chan struct{})
	}
	k.active++
	return k.ctx, func() {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.active--
		if k.active == 0 {
			close(k.drained)
		}
	}, true
}

func (k *keepaliveTicks) quiesce(timeout time.Duration) bool {
	k.mu.Lock()
	k.blocked = true
	k.cancel()
	if k.active == 0 {
		k.mu.Unlock()
		return true
	}
	done := k.drained
	k.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (k *keepaliveTicks) resume(parent context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ctx, k.cancel = context.WithCancel(parent)
	k.blocked = false
}

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
	claudeDefault, _ := config.DefaultAccountDirectory("claude")
	codexDefault, _ := config.DefaultAccountDirectory("codex")
	var accounts []keepalive.Account
	for _, a := range claudeAccounts {
		dir := a.ConfigDir
		if dir == "" {
			dir = claudeDefault
		}
		accounts = append(accounts, keepalive.Account{Provider: "claude", Key: a.Key, Home: dir})
	}
	for _, a := range codexAccounts {
		dir := a.Home
		if dir == "" {
			dir = codexDefault
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

func stopKeepalive(service *keepalive.Service, current settings) settings {
	service.Stop()
	next := keepalive.DefaultConfig()
	if current.Keepalive != nil {
		next = *current.Keepalive
	}
	next.Enabled = false
	current.Keepalive = &next
	return current
}

func persistKeepaliveOff() error {
	_, err := agenthooks.TryUpdateJSONObjectWithBackup(settingsPath(), func(root map[string]any) error {
		value, ok := root["keepalive"]
		if !ok {
			root["keepalive"] = map[string]any{"enabled": false}
			return nil
		}
		fields, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("keepalive must be an object; runtime is stopped but settings were not saved")
		}
		fields["enabled"] = false
		return nil
	})
	return err
}
