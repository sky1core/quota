package render

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/quotacache"
)

func TestTextParsedQuotaWarnings(t *testing.T) {
	for _, tc := range []struct {
		name, raw, diagnostic string
		rows                  []string
	}{
		{"model", "Current session: 10% used\nCurrent week (all models): 10% used\nCurrent week (Opus): --% used", `"Opus"`, []string{"Session", "Week"}},
		{"aggregate", "Current session: --% used\nCurrent week (all models): 10% used\nCurrent week (Opus): 10% used", `"Session"`, []string{"Week", "Opus"}},
		{"healthy", "Current session: 10% used\nCurrent week (all models): 10% used\nCurrent week (Opus): 10% used", "", []string{"Session", "Week", "Opus"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", "")
			dir := filepath.Join(home, ".claude")
			quotacache.Put(renderTestCacheKey(t, "claude", dir), tc.raw, time.Now().Add(time.Hour))
			data, err := claude.GetQuotaForConfigDir(time.Second, dir, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			got := Text(map[string]any{"claude": data})
			if tc.diagnostic != "" {
				if !strings.Contains(got, "Warning: claude quota row "+tc.diagnostic+" has an unreadable percentage") {
					t.Fatalf("missing diagnostic: %s", got)
				}
			} else if strings.Contains(got, "Warning:") {
				t.Fatalf("healthy quota warned: %s", got)
			}
			for _, row := range tc.rows {
				if !strings.Contains(got, fmtLine(item{label: row, left: "90%"})) {
					t.Errorf("missing valid %s row: %s", row, got)
				}
			}
		})
	}
}

func renderTestCacheKey(t *testing.T, provider, dir string) string {
	t.Helper()
	canonical, err := config.CanonicalAccountDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	return provider + ":" + canonical
}

func TestTextWarningsStayWithAccount(t *testing.T) {
	got := Text(map[string]any{
		"claude": map[string]any{
			"windowErrors": []string{"session unreadable"},
			"modelWindowErrors": map[string]string{
				"Sonnet": "sonnet unreadable",
				"Opus":   "opus unreadable",
			},
		},
		"claude-2": map[string]any{"windows": []map[string]any{{"label": "Session", "left": 75}}},
		"codex":    map[string]any{"windowErrors": []string{"primary unreadable"}},
		"errors":   []any{map[string]any{"provider": "codex-2", "error": "timeout"}},
	})
	claudeSection, remainder, ok := strings.Cut(got, "Claude 2\n")
	want := "  Warning: session unreadable\n  Warning: opus unreadable\n  Warning: sonnet unreadable\n"
	if !ok || !strings.Contains(claudeSection, want) {
		t.Fatalf("missing or unordered account warnings: %s", got)
	}
	healthySection, _, ok := strings.Cut(remainder, "Codex\n")
	if !ok || strings.Contains(healthySection, "Warning:") || !strings.Contains(healthySection, "75%") {
		t.Fatalf("healthy account changed: %s", got)
	}
	if !strings.Contains(got, "Codex\n  Warning: primary unreadable\n") || !strings.Contains(got, "codex-2: timeout") {
		t.Fatalf("missing other provider diagnostic: %s", got)
	}
}
