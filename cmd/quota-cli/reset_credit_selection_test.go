package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func resetCreditQuotaResponse(now time.Time, status string, expiry, reset time.Duration) string {
	return fmt.Sprintf(`{"rateLimits":{"primary":{"usedPercent":20,"windowDurationMins":300,"resetsAt":%d},"secondary":{"usedPercent":20,"windowDurationMins":10080,"resetsAt":%d}},"rateLimitResetCredits":{"credits":[{"status":%q,"expiresAt":%d}]}}`, now.Add(reset).Unix(), now.Add(7*24*time.Hour).Unix(), status, now.Add(expiry).Unix())
}

func serveResetCreditQuota(t *testing.T, raw string) {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("QUOTA_TEST_RESPONSE", raw)
	script := `#!/bin/sh
[ "$1" = app-server ] || exit 2
while IFS= read -r line; do
 case "$line" in
  *'"id":1'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}' ;;
  *'"id":2'*) printf '{"jsonrpc":"2.0","id":2,"result":%s}\n' "$QUOTA_TEST_RESPONSE"; exit 0 ;;
 esac
done
`
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestCodexResetCreditExpiryPreservesAccountSelection(t *testing.T) {
	for _, mode := range []string{"explicit", "select", "automatic"} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cached=%t", mode, cached), func(t *testing.T) {
				home := autoPromptTestHome(t)
				now := time.Now()
				dir := filepath.Join(home, ".codex")
				key := quotaTestCacheKey(t, "codex", dir)
				expiry := -time.Minute
				cfg := config.Config{}
				if cached {
					expiry = 2 * time.Second
					cfg.CodexAccounts = []config.CodexAccount{{Key: "codex-2", Home: "~/.codex-2"}}
					extraKey := quotaTestCacheKey(t, "codex", filepath.Join(home, ".codex-2"))
					quotacache.Put(extraKey, autoPromptCodexQuota(0, 0), now.Add(time.Hour))
					release := releaseProbeAfter(t, extraKey, 3*time.Second)
					defer release()
				}
				raw := resetCreditQuotaResponse(now, "available", expiry, time.Hour)
				if cached {
					quotacache.PutWithContext(context.Background(), key, raw, now, time.Unix(now.Add(expiry).Unix(), 0))
				} else {
					serveResetCreditQuota(t, raw)
				}
				putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current week (all models): 100% used")
				var selected string
				var err error
				switch mode {
				case "explicit":
					account, e := selectCodexAccount(context.Background(), cfg)
					selected, err = account.Key, e
				case "select":
					result, e := buildSelectAgentResult(context.Background(), cfg, selectAgentOptions{agent: selectAgentAll})
					err = e
					if result.Selected != nil {
						selected = result.Selected.Key
					}
				case "automatic":
					catalogs := []accountModels{autoPromptTestCatalog("codex", autoPromptTestModel("code-model", "high"))}
					if cached {
						catalogs = append(catalogs, autoPromptTestCatalog("codex-2", autoPromptTestModel("code-model", "high")))
					}
					account, e := selectAutoPromptAccount(context.Background(), cfg, autoPromptTestOptions(), catalogs)
					selected, err = account.key, e
				}
				if err != nil || selected != "codex" {
					t.Fatalf("selected=%q error=%v; want healthy Codex account despite expired reset credit", selected, err)
				}
				if _, ok := quotacache.Get(key, cliCacheMaxAge); ok {
					t.Fatal("display cache must expire with the reset credit")
				}
			})
		}
	}
}

func TestCodexObservationAndDisplayValidity(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, tc := range []struct {
			name, status  string
			expiry, reset time.Duration
		}{
			{"credit-before-reset", "available", time.Minute, time.Hour},
			{"consumed-credit", "consumed", -time.Minute, time.Hour},
			{"quota-before-credit", "available", time.Hour, time.Minute},
		} {
			t.Run(fmt.Sprintf("%s/cached=%t", tc.name, cached), func(t *testing.T) {
				home := autoPromptTestHome(t)
				now := time.Now()
				fetched := now.Add(-10 * time.Second)
				reset := time.Unix(now.Add(tc.reset).Unix(), 0)
				bound := reset
				if tc.status == "available" && tc.expiry < tc.reset {
					bound = time.Unix(now.Add(tc.expiry).Unix(), 0)
				}
				raw := resetCreditQuotaResponse(now, tc.status, tc.expiry, tc.reset)
				dir := filepath.Join(home, ".codex")
				key := quotaTestCacheKey(t, "codex", dir)
				if cached {
					quotacache.PutWithContext(context.Background(), key, raw, fetched, bound)
				} else {
					serveResetCreditQuota(t, raw)
				}
				quota, validity, err := codex.GetQuotaForHomeWithValidity(context.Background(), 5*time.Second, dir, cliCacheMaxAge)
				if err != nil {
					t.Fatal(err)
				}
				if cached && !validity.FetchedAt.Equal(fetched) {
					t.Fatalf("cached observation time changed: %v", validity.FetchedAt)
				}
				_, display, ok := quotacache.GetWithValidity(key, cliCacheMaxAge)
				if !ok || !display.ValidUntil.Equal(bound) {
					t.Fatalf("display boundary=%v want=%v readable=%v", display.ValidUntil, bound, ok)
				}
				if !validity.ValidUntil.Equal(reset) {
					t.Errorf("quota boundary=%v want=%v", validity.ValidUntil, reset)
				}
				if !validity.ValidAt(time.Now(), cliCacheMaxAge) {
					t.Error("fresh observation rejected")
				}
				for _, decision := range []time.Time{reset, validity.FetchedAt.Add(cliCacheMaxAge + time.Nanosecond)} {
					results := []quotaProbeResult{{quota: quota, validity: validity}}
					rejectExpiredQuotaResults(results, decision)
					if results[0].err == nil {
						t.Errorf("expired observation accepted at %v", decision)
					}
				}
			})
		}
	}
}
