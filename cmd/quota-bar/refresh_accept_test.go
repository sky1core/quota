package main

import (
	"strings"
	"testing"
	"time"
)

func codexPayload(windows []map[string]any, windowErrors []string) map[string]any {
	p := map[string]any{}
	if windows != nil {
		p["windows"] = windows
	}
	if windowErrors != nil {
		p["windowErrors"] = windowErrors
	}
	return p
}

func win(key, label string, left int) map[string]any {
	return map[string]any{"key": key, "label": label, "left": left}
}

func TestApplyResult_WindowErrors(t *testing.T) {
	t.Run("partial: valid rows kept, diagnostic surfaced", func(t *testing.T) {
		d := newQuotaData()
		d.applyResult("codex", false, codexPayload(
			[]map[string]any{win("weekly", "7d", 88)},
			[]string{"codex primary window has no windowDurationMins"},
		))
		if d.values["codex_weekly"] != "88%" || d.labels["codex_weekly"] != "7d" {
			t.Errorf("valid row dropped: %v / %v", d.values, d.labels)
		}
		if d.warns["codex"] == "" {
			t.Error("diagnostic not surfaced as a warning")
		}
	})

	t.Run("all-invalid: diagnostic joined, no rows", func(t *testing.T) {
		d := newQuotaData()
		d.applyResult("codex", false, codexPayload(nil, []string{"primary unreadable", "secondary unreadable"}))
		if len(d.values) != 0 {
			t.Errorf("no valid window must yield no rows: %v", d.values)
		}
		if !strings.Contains(d.warns["codex"], "primary") || !strings.Contains(d.warns["codex"], "secondary") {
			t.Errorf("both diagnostics must join into the warning: %q", d.warns["codex"])
		}
	})

	t.Run("legitimate empty: no diagnostic, no rows, no warning", func(t *testing.T) {
		d := newQuotaData()
		d.applyResult("codex", false, map[string]any{"planType": "pro"})
		if len(d.values) != 0 || len(d.warns) != 0 {
			t.Errorf("empty response must not warn: values=%v warns=%v", d.values, d.warns)
		}
	})
}

func TestRefreshAccepted(t *testing.T) {
	mk := func(fn func(quotaData)) quotaData { d := newQuotaData(); fn(d); return d }
	cases := []struct {
		name string
		d    quotaData
		want bool
	}{
		{"transport error", mk(func(d quotaData) { d.errs["codex"] = "boom" }), false},
		{"all-invalid diagnostic", mk(func(d quotaData) { d.warns["codex"] = "bad" }), false},
		{"partial diagnostic keeps rows", mk(func(d quotaData) {
			d.warns["codex"] = "bad"
			d.values["codex_weekly"] = "80%"
		}), true},
		{"legit empty", mk(func(d quotaData) {}), true},
		{"healthy", mk(func(d quotaData) { d.values["codex_5h"] = "90%" }), true},
	}
	for _, c := range cases {
		if got := refreshAccepted(c.d, "codex"); got != c.want {
			t.Errorf("%s: refreshAccepted = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestClaudeWarningClearsAfterHealthyRefresh(t *testing.T) {
	lastOK := newQuotaData()
	stamps := map[string]time.Time{}
	keys := []string{"claude_session"}
	for i, diagnostics := range [][]string{{"unreadable weekly quota"}, nil} {
		d := newQuotaData()
		d.applyResult("claude", true, map[string]any{
			"windows":      []map[string]any{win("session", "Session", 75)},
			"windowErrors": diagnostics,
		})
		acceptFetch(d, &lastOK, stamps, []string{"claude"}, keys, nil, time.Unix(int64(i), 0))
		if (d.warns["claude"] != "") != (i == 0) || d.values["claude_session"] != "75%" {
			t.Fatalf("incorrect warning transition: %+v", d)
		}
	}
}

func TestRefreshAccepted_ProviderPrefixIsolation(t *testing.T) {
	d := newQuotaData()
	d.warns["codex"] = "bad"
	d.values["codex-2_weekly"] = "80%" // a different account's row
	if refreshAccepted(d, "codex") {
		t.Error("codex-2 rows must not make an all-invalid codex look accepted")
	}
}

func seedHealthyCodex(t *testing.T, lastOK *quotaData, lastSuccessAt map[string]time.Time, at time.Time) {
	t.Helper()
	d := newQuotaData()
	d.applyResult("codex", false, codexPayload([]map[string]any{
		win("5h", "5h", 90), win("weekly", "7d", 70),
	}, nil))
	acceptFetch(d, lastOK, lastSuccessAt, []string{"codex"}, []string{"codex_5h", "codex_weekly"}, []string{"codex"}, at)
}

func TestAcceptFetch_AllInvalidCarriesAndDoesNotAdvance(t *testing.T) {
	providers := []string{"codex"}
	allKeys := []string{"codex_5h", "codex_weekly"}
	codexKeys := []string{"codex"}
	lastOK := quotaData{}
	lastSuccessAt := map[string]time.Time{}
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	seedHealthyCodex(t, &lastOK, lastSuccessAt, t0)
	if lastSuccessAt["codex"] != t0 || lastOK.values["codex_5h"] != "90%" {
		t.Fatalf("healthy round must snapshot and stamp: %v %v", lastSuccessAt, lastOK.values)
	}

	t1 := t0.Add(3 * time.Minute)
	d2 := newQuotaData()
	d2.applyResult("codex", false, codexPayload(nil, []string{"primary unreadable", "secondary unreadable"}))
	acceptFetch(d2, &lastOK, lastSuccessAt, providers, allKeys, codexKeys, t1)

	if lastSuccessAt["codex"] != t0 {
		t.Errorf("all-invalid must NOT advance lastSuccessAt: got %v want %v", lastSuccessAt["codex"], t0)
	}
	if d2.values["codex_5h"] != "90%" || d2.values["codex_weekly"] != "70%" {
		t.Errorf("all-invalid must carry last good values into display: %v", d2.values)
	}
	if d2.warns["codex"] == "" {
		t.Error("diagnostic must remain visible on the carried round")
	}
	if lastOK.values["codex_5h"] != "90%" || lastOK.values["codex_weekly"] != "70%" {
		t.Errorf("lastOK must be untouched by an all-invalid round: %v", lastOK.values)
	}
}

func TestAcceptFetch_PartialKeepsValidRowsAndAdvances(t *testing.T) {
	providers := []string{"codex"}
	allKeys := []string{"codex_5h", "codex_weekly"}
	codexKeys := []string{"codex"}
	lastOK := quotaData{}
	lastSuccessAt := map[string]time.Time{}
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	seedHealthyCodex(t, &lastOK, lastSuccessAt, t0)

	t1 := t0.Add(3 * time.Minute)
	d2 := newQuotaData()
	d2.applyResult("codex", false, codexPayload(
		[]map[string]any{win("weekly", "7d", 55)}, // only weekly valid
		[]string{"5h window unreadable"},
	))
	acceptFetch(d2, &lastOK, lastSuccessAt, providers, allKeys, codexKeys, t1)

	if lastSuccessAt["codex"] != t1 {
		t.Errorf("a round with ≥1 valid window must advance success: got %v", lastSuccessAt["codex"])
	}
	if d2.values["codex_weekly"] != "55%" {
		t.Errorf("fresh valid row must remain: %v", d2.values)
	}
	if d2.warns["codex"] == "" {
		t.Error("warning must still show alongside the valid row")
	}
	if _, ok := lastOK.values["codex_5h"]; ok {
		t.Errorf("the dropped 5h window must clear from lastOK on an accepted round: %v", lastOK.values)
	}
	if lastOK.values["codex_weekly"] != "55%" {
		t.Errorf("valid row must snapshot into lastOK: %v", lastOK.values)
	}
}

func TestAcceptFetch_EmptyClearsVanishedWindows(t *testing.T) {
	providers := []string{"codex"}
	allKeys := []string{"codex_5h", "codex_weekly"}
	codexKeys := []string{"codex"}
	lastOK := quotaData{}
	lastSuccessAt := map[string]time.Time{}
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	seedHealthyCodex(t, &lastOK, lastSuccessAt, t0)

	t1 := t0.Add(3 * time.Minute)
	d2 := newQuotaData()
	d2.applyResult("codex", false, map[string]any{"planType": "pro"}) // no windows, no diagnostic
	acceptFetch(d2, &lastOK, lastSuccessAt, providers, allKeys, codexKeys, t1)

	if lastSuccessAt["codex"] != t1 {
		t.Errorf("a legitimately empty (undiagnosed) response is success: got %v", lastSuccessAt["codex"])
	}
	if len(d2.values) != 0 {
		t.Errorf("empty success must not carry stale values: %v", d2.values)
	}
	if len(lastOK.values) != 0 {
		t.Errorf("vanished windows must clear from lastOK: %v", lastOK.values)
	}
}
