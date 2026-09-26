package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestAccountSelectionUsesTimeAfterProbeWait(t *testing.T) {
	for _, mode := range []string{"exec", "select-all", "auto"} {
		t.Run(mode, func(t *testing.T) {
			home := autoPromptTestHome(t)
			base := time.Now().Truncate(time.Second).Add(time.Second)
			cfg := config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}}}
			putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current week (all models): 100% used")
			for _, entry := range []struct {
				dir   string
				used  int
				reset time.Duration
			}{
				{".codex", 65, 180 * time.Second}, {".codex-2", 30, 389 * time.Second},
			} {
				at := base.Add(entry.reset)
				raw := fmt.Sprintf(`{"rateLimits":{"secondary":{"usedPercent":%d,"windowDurationMins":10080,"resetsAt":%d}}}`, entry.used, at.Unix())
				quotacache.Put(quotaTestCacheKey(t, "codex", filepath.Join(home, entry.dir)), raw, at)
			}
			release := releaseProbeAfter(t, quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2")), 3*time.Second)
			defer release()
			var key string
			var err error
			switch mode {
			case "exec":
				account, e := selectCodexAccount(context.Background(), cfg)
				key, err = account.Key, e
			case "select-all":
				result, e := buildSelectAgentResult(context.Background(), cfg, selectAgentOptions{agent: selectAgentAll})
				err = e
				if result.Selected != nil {
					key = result.Selected.Key
				}
				if result.Generated.Before(base.Add(2 * time.Second)) {
					t.Errorf("generated time precedes completed probe wait: %s", result.Generated)
				}
			case "auto":
				catalogs := []accountModels{
					autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high")),
					autoPromptTestCatalog("codex-2", autoPromptTestModel("code-model", "high")),
				}
				account, e := selectAutoPromptAccount(context.Background(), cfg, autoPromptTestOptions(), catalogs)
				key, err = account.key, e
			}
			if err != nil || key != "codex" {
				t.Fatalf("selected %q, error %v; want codex using decision time", key, err)
			}
		})
	}
}

func TestSelectAgentRejectsObservationExpiringDuringOtherProvider(t *testing.T) {
	for _, boundary := range []string{"reset", "max-age"} {
		t.Run(boundary, func(t *testing.T) {
			home := autoPromptTestHome(t)
			now := time.Now()
			fetchedAt, validUntil := now, now.Add(time.Hour)
			if boundary == "reset" {
				validUntil = now.Add(time.Second)
			} else {
				fetchedAt = now.Add(-cliCacheMaxAge + time.Second)
			}
			claudeKey := quotaTestCacheKey(t, "claude", filepath.Join(home, ".claude"))
			quotacache.PutWithContext(context.Background(), claudeKey, "Current week (all models): 20% used", fetchedAt, validUntil)
			codexKey := quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex"))
			quotacache.Put(codexKey, `{"rateLimits":{"secondary":{"usedPercent":100,"windowDurationMins":10080}}}`, now.Add(time.Hour))
			release := releaseProbeAfter(t, codexKey, 3*time.Second)
			defer release()
			result, err := buildSelectAgentResult(context.Background(), config.Config{}, selectAgentOptions{agent: selectAgentAll})
			if err == nil || result.Selected != nil {
				t.Fatalf("selected expired observation: %+v, %v", result.Selected, err)
			}
			if len(result.Candidates) != 2 || !strings.Contains(result.Candidates[0].Error, "observation expired") {
				t.Fatalf("missing explicit expiry diagnostic: %+v", result.Candidates)
			}
			if _, ok := quotacache.Get(claudeKey, cliCacheMaxAge); ok {
				t.Fatal("expired cache unexpectedly readable")
			}
		})
	}
}

func TestQuotaScoringRejectsExpiredResetWithHealthyOtherWindow(t *testing.T) {
	now := time.Now()
	for _, provider := range []string{"claude", "codex"} {
		for _, offset := range []time.Duration{-time.Second, 0, time.Second} {
			t.Run(fmt.Sprintf("%s/%s", provider, offset), func(t *testing.T) {
				weeklyKey := "weekly"
				if provider == "claude" {
					weeklyKey = "weekly_all"
				}
				q := testQuota(testWindow(weeklyKey, "Week", 80, now.Add(time.Hour), 10080), testWindow("session", "Session", 80, now.Add(offset), 300))
				var ok bool
				if provider == "claude" {
					_, ok = scoreClaudeQuota(q, "", false, 5, now)
				} else {
					_, ok = scoreCodexQuota(q, false, 5, now)
				}
				if ok != (offset > 0) {
					t.Fatalf("usable=%v with reset offset %s", ok, offset)
				}
			})
		}
	}
}

func releaseProbeAfter(t *testing.T, key string, delay time.Duration) func() {
	t.Helper()
	unlock, err := quotacache.AcquireProbe(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { time.Sleep(delay); unlock(); close(done) }()
	return func() { <-done }
}
