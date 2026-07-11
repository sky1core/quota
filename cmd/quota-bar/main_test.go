package main

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

func TestResolveVersion(t *testing.T) {
	biVersioned := &debug.BuildInfo{Main: debug.Module{Version: "v0.7.0"}}
	biLocal := &debug.BuildInfo{
		Main:     debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "438784f550e2ffb48f703fa668ec5df3d94b1018"}},
	}
	biBare := &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}

	cases := []struct {
		name   string
		ldflag string
		bi     *debug.BuildInfo
		ok     bool
		want   string
	}{
		{"ldflags override wins", "v9.9.9", biVersioned, true, "v9.9.9"},
		{"module version from @install", "", biVersioned, true, "v0.7.0"},
		{"local build -> short vcs revision", "", biLocal, true, "438784f"},
		{"devel without vcs -> dev", "", biBare, true, "dev"},
		{"no build info -> dev", "", nil, false, "dev"},
	}
	for _, c := range cases {
		if got := resolveVersion(c.ldflag, c.bi, c.ok); got != c.want {
			t.Errorf("%s: resolveVersion(%q, …) = %q, want %q", c.name, c.ldflag, got, c.want)
		}
	}
}

func TestResetText(t *testing.T) {
	cases := []struct {
		name           string
		showResetTime  bool
		rel, abs, want string
	}{
		{"relative mode uses rel", false, "5d 15h", "Jul 6 15:04", "5d 15h"},
		{"clock mode uses abs", true, "5d 15h", "Jul 6 15:04", "Jul 6 15:04"},
		{"clock mode falls back to rel when abs unknown", true, "5d 15h", "", "5d 15h"},
		{"relative mode ignores abs", false, "2h", "Jul 6 15:04", "2h"},
		{"both empty stays empty", true, "", "", ""},
	}
	for _, c := range cases {
		if got := resetText(c.showResetTime, c.rel, c.abs); got != c.want {
			t.Errorf("%s: resetText(%v,%q,%q) = %q, want %q", c.name, c.showResetTime, c.rel, c.abs, got, c.want)
		}
	}
}

// TestRowTitle_ModeSwitch verifies the exact menu string a row shows in each
// mode — the visible behavior of the "Reset as clock time" toggle (req #2).
func TestRowTitle_ModeSwitch(t *testing.T) {
	const rel, abs = "2d 16h", "Jul 13 12:00"
	cases := []struct {
		name          string
		label, value  string
		showResetTime bool
		rel, abs      string
		want          string
	}{
		{"relative mode", "Weekly", "90%", false, rel, abs, "Weekly 90% (2d 16h)"},
		{"clock mode", "Weekly", "90%", true, rel, abs, "Weekly 90% (Jul 13 12:00)"},
		{"clock mode falls back when abs unknown", "5h", "85%", true, "2h 30m", "", "5h 85% (2h 30m)"},
		{"no reset omits parens", "Fable", "100%", false, "", "", "Fable 100%"},
		{"stale marker preserved in value", "Session", "99%?", true, rel, abs, "Session 99%? (Jul 13 12:00)"},
	}
	for _, c := range cases {
		got := rowTitle(c.label, c.value, resetText(c.showResetTime, c.rel, c.abs))
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestResetCreditRows(t *testing.T) {
	at := time.Date(2026, 7, 12, 10, 42, 0, 0, time.Local)

	t.Run("extracts rel/abs/title", func(t *testing.T) {
		rc := map[string]any{
			"available": 1,
			"items": []map[string]any{
				{"title": "Full reset (Weekly + 5 hr)", "expiresIn": "1d 0h", "expiresAt": at},
			},
		}
		rows := resetCreditRows(rc)
		if len(rows) != 1 {
			t.Fatalf("rows = %v, want 1", rows)
		}
		if rows[0].rel != "1d 0h" || rows[0].abs != "Jul 12 10:42" || rows[0].title != "Full reset (Weekly + 5 hr)" {
			t.Errorf("row = %+v", rows[0])
		}
	})

	t.Run("empty items -> nil", func(t *testing.T) {
		if r := resetCreditRows(map[string]any{"available": 0, "items": []map[string]any{}}); r != nil {
			t.Errorf("want nil, got %v", r)
		}
		if r := resetCreditRows(map[string]any{"available": 3}); r != nil {
			t.Errorf("missing items key: want nil, got %v", r)
		}
	})

	t.Run("no expiry epoch -> empty rel/abs, title kept", func(t *testing.T) {
		rows := resetCreditRows(map[string]any{"items": []map[string]any{{"title": "Full reset"}}})
		if len(rows) != 1 || rows[0].rel != "" || rows[0].abs != "" || rows[0].title != "Full reset" {
			t.Errorf("rows = %+v", rows)
		}
	})
}

// TestResetRowTitles_ModeSwitch is the core behavior req: the Reset credits parent and
// each submenu child must follow the "Reset as clock time" toggle exactly like
// every other row — relative time left when off, absolute clock when on.
func TestResetRowTitles_ModeSwitch(t *testing.T) {
	rows := []resetRow{
		{rel: "1d 0h", abs: "Jul 12 10:42", title: "Full reset (Weekly + 5 hr)"},
		{rel: "6d 23h", abs: "Jul 18 09:33", title: "Full reset (Weekly + 5 hr)"},
	}

	t.Run("relative mode (toggle off)", func(t *testing.T) {
		parent, children := resetRowTitles(rows, false)
		if parent != "Reset credits: 2  (1d 0h)" {
			t.Errorf("parent = %q", parent)
		}
		want := []string{"1d 0h", "6d 23h"}
		if len(children) != 2 || children[0] != want[0] || children[1] != want[1] {
			t.Errorf("children = %v, want %v", children, want)
		}
	})

	t.Run("clock mode (toggle on)", func(t *testing.T) {
		parent, children := resetRowTitles(rows, true)
		if parent != "Reset credits: 2  (Jul 12 10:42)" {
			t.Errorf("parent = %q", parent)
		}
		want := []string{"Jul 12 10:42", "Jul 18 09:33"}
		if len(children) != 2 || children[0] != want[0] || children[1] != want[1] {
			t.Errorf("children = %v, want %v", children, want)
		}
	})

	t.Run("clock mode falls back to rel when abs unknown", func(t *testing.T) {
		r := []resetRow{{rel: "1d 0h", abs: ""}}
		parent, children := resetRowTitles(r, true)
		if parent != "Reset credits: 1  (1d 0h)" {
			t.Errorf("parent = %q", parent)
		}
		if len(children) != 1 || children[0] != "1d 0h" {
			t.Errorf("children = %v, want [1d 0h]", children)
		}
	})

	t.Run("no time known falls back to title, no parenthetical", func(t *testing.T) {
		r := []resetRow{{title: "Full reset"}}
		parent, children := resetRowTitles(r, false)
		if parent != "Reset credits: 1" {
			t.Errorf("parent = %q, want %q", parent, "Reset credits: 1")
		}
		if len(children) != 1 || children[0] != "Full reset" {
			t.Errorf("children = %v, want [Full reset]", children)
		}
	})

	t.Run("empty rows -> hidden", func(t *testing.T) {
		parent, children := resetRowTitles(nil, false)
		if parent != "" || children != nil {
			t.Errorf("want empty/nil, got parent=%q children=%v", parent, children)
		}
	})
}

func TestSettings_RefreshIntervals(t *testing.T) {
	// Absent (zero value) → built-in defaults, not an implicit fallback.
	var d settings
	if got := d.activeInterval(); got != defaultRefreshActive {
		t.Errorf("default activeInterval = %s, want %s", got, defaultRefreshActive)
	}
	if got := d.idleInterval(); got != defaultRefreshIdle {
		t.Errorf("default idleInterval = %s, want %s", got, defaultRefreshIdle)
	}

	// Positive config values are applied verbatim.
	s := settings{RefreshActiveMinutes: 15, RefreshIdleMinutes: 45}
	if got := s.activeInterval(); got != 15*time.Minute {
		t.Errorf("activeInterval = %s, want 15m", got)
	}
	if got := s.idleInterval(); got != 45*time.Minute {
		t.Errorf("idleInterval = %s, want 45m", got)
	}

	// Zero / negative fall back to the default (never 0-length intervals).
	bad := settings{RefreshActiveMinutes: 0, RefreshIdleMinutes: -5}
	if got := bad.activeInterval(); got != defaultRefreshActive {
		t.Errorf("zero activeInterval = %s, want default %s", got, defaultRefreshActive)
	}
	if got := bad.idleInterval(); got != defaultRefreshIdle {
		t.Errorf("negative idleInterval = %s, want default %s", got, defaultRefreshIdle)
	}
}

func TestSettings_StaleThreshold(t *testing.T) {
	// Default: max(3m, 30m) + 5m = 35m (unchanged from the old hardcoded value).
	var d settings
	if got := d.staleThreshold(); got != 35*time.Minute {
		t.Errorf("default staleThreshold = %s, want 35m", got)
	}
	// Inverted config (active > idle): must trail the larger (active) interval,
	// otherwise a normally-refreshed provider would be flagged stale.
	inv := settings{RefreshActiveMinutes: 60, RefreshIdleMinutes: 10}
	if got := inv.staleThreshold(); got != 65*time.Minute {
		t.Errorf("staleThreshold(active=60,idle=10) = %s, want 65m (max+5m)", got)
	}
	// Longer idle: trails idle.
	slow := settings{RefreshActiveMinutes: 3, RefreshIdleMinutes: 120}
	if got := slow.staleThreshold(); got != 125*time.Minute {
		t.Errorf("staleThreshold(idle=120) = %s, want 125m", got)
	}
}

func TestSettings_RefreshIntervalsRoundTrip(t *testing.T) {
	b, err := json.Marshal(settings{RefreshActiveMinutes: 30, RefreshIdleMinutes: 60})
	if err != nil {
		t.Fatal(err)
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if s.activeInterval() != 30*time.Minute || s.idleInterval() != 60*time.Minute {
		t.Errorf("round-trip lost intervals: active=%s idle=%s", s.activeInterval(), s.idleInterval())
	}

	// A file that omits the keys must default (relative to the built-ins), and
	// omitempty must keep them out of a re-marshaled default config.
	var legacy settings
	if err := json.Unmarshal([]byte(`{"selected":["codex_5h"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.activeInterval() != defaultRefreshActive || legacy.idleInterval() != defaultRefreshIdle {
		t.Error("absent interval keys should resolve to built-in defaults")
	}
	out, _ := json.Marshal(legacy)
	if strings.Contains(string(out), "refreshActiveMinutes") || strings.Contains(string(out), "refreshIdleMinutes") {
		t.Errorf("unset intervals should be omitted from JSON, got %s", out)
	}
}

// ShowResetTime must survive a JSON round-trip so the toggle persists across
// restarts, and its absence in old files must default to false (relative mode).
func TestSettings_ShowResetTimeRoundTrip(t *testing.T) {
	b, err := json.Marshal(settings{Selected: []string{"codex_5h"}, ShowResetTime: true})
	if err != nil {
		t.Fatal(err)
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	if !s.ShowResetTime {
		t.Error("ShowResetTime should survive round-trip")
	}

	var legacy settings
	if err := json.Unmarshal([]byte(`{"selected":["codex_5h"]}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.ShowResetTime {
		t.Error("ShowResetTime should default to false when absent")
	}
}
