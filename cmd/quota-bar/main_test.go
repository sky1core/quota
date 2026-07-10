package main

import (
	"encoding/json"
	"testing"
)

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
