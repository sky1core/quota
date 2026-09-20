package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestFetchQuotaParsedWarnings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	dir := filepath.Join(home, ".claude")
	healthyDir := filepath.Join(home, ".claude-2")
	healthy := "Current session: 10% used\nCurrent week (all models): 10% used\nCurrent week (Opus): 10% used"
	quotacache.Put(quotaBarTestCacheKey(t, "claude", healthyDir), healthy, time.Now().Add(time.Hour))
	accounts := []config.ResolvedAccount{{Key: "claude", ConfigDir: dir}, {Key: "claude-2", ConfigDir: healthyDir}}
	lastOK := newQuotaData()
	stamps := map[string]time.Time{}
	for i, tc := range []struct {
		name, raw string
		want      []string
		rows      int
	}{
		{"model", strings.Replace(healthy, "(Opus): 10%", "(Opus): --%", 1), []string{`"Opus"`}, 2},
		{"aggregate and model", "Current session: --% used\nCurrent week (all models): 10% used\nCurrent week (Opus): --% used", []string{`"Session"`, `"Opus"`}, 1},
		{"long warning", "Current session: --% used\nCurrent week (all models): --% used\nCurrent week (Opus): --% used\nCurrent week (Sonnet): 10% used", []string{`"Session"`, `"Week"`, `"Opus"`}, 1},
		{"healthy recovery", healthy, nil, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quotacache.Put(quotaBarTestCacheKey(t, "claude", dir), tc.raw, time.Now().Add(time.Hour))
			data := fetchQuota(accounts, nil, time.Minute)
			if len(data.errs) != 0 {
				t.Fatalf("unexpected fetch failure: %v", data.errs)
			}
			warning := data.warns["claude"]
			_, tooltip := quotaDiagnosticText("Warning", warning)
			for _, label := range tc.want {
				if !strings.Contains(warning, "claude quota row "+label+" has an unreadable percentage") {
					t.Errorf("missing %s diagnostic: %q", label, warning)
				}
				if !strings.Contains(tooltip, "claude quota row "+label+" has an unreadable percentage") {
					t.Errorf("missing %s in menu tooltip: %q", label, tooltip)
				}
			}
			if len(tc.want) == 0 && warning != "" {
				t.Errorf("healthy quota warned: %q", warning)
			}
			if data.warns["claude-2"] != "" || data.values["claude-2_session"] != "90%" {
				t.Fatalf("another account affected: %+v", data)
			}
			count := 0
			for key := range data.values {
				if strings.HasPrefix(key, "claude_") {
					count++
				}
			}
			if count != tc.rows || refreshAccepted(data, "claude") != (tc.rows > 0) {
				t.Fatalf("unexpected partial refresh: values=%v accepted=%v", data.values, refreshAccepted(data, "claude"))
			}
			at := time.Unix(int64(i+1), 0)
			acceptFetch(data, &lastOK, stamps, []string{"claude", "claude-2"}, []string{"claude_session", "claude_weekly_all", "claude_extra_1", "claude-2_session", "claude-2_weekly_all", "claude-2_extra_1"}, nil, at)
			if tc.rows > 0 && stamps["claude"] != at {
				t.Errorf("partial or healthy refresh not accepted: %v", stamps)
			}
			if data.warns["claude"] != warning {
				t.Errorf("warning changed during display handoff: %v", data.warns)
			}
		})
	}
}

func quotaBarTestCacheKey(t *testing.T, provider, dir string) string {
	t.Helper()
	canonical, err := config.CanonicalAccountDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	return provider + ":" + canonical
}

func TestQuotaDiagnosticText(t *testing.T) {
	for _, msg := range []string{"quota unreadable", strings.Repeat("x", 119) + "한도 오류 " + strings.Repeat("x", 30)} {
		title, tooltip := quotaDiagnosticText("Warning", msg)
		if tooltip != "Warning: "+msg || !utf8.ValidString(title) {
			t.Fatalf("diagnostic lost or corrupted: title=%q tooltip=%q", title, tooltip)
		}
		if len([]rune(msg)) > 120 && !strings.HasSuffix(title, "…") {
			t.Fatalf("long title not abbreviated: %q", title)
		}
		if len([]rune(msg)) <= 120 && title != "  Warning: "+msg {
			t.Fatalf("short title changed: %q", title)
		}
	}
}
