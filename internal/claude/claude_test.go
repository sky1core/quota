package claude

import (
	"strings"
	"testing"
	"time"
)

func TestToRelative_AlreadyRelative(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"4h 30m", "4h 30m"},
		{"in 4h 30m", "4h 30m"},
		{"5m", "5m"},
		{"2d 3h", "2d 3h"},
	}
	for _, tt := range tests {
		got := toRelative(tt.in)
		if got != tt.want {
			t.Errorf("toRelative(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToRelative_StripTimezone(t *testing.T) {
	got := toRelative("4h 30m (Asia/Seoul)")
	if got != "4h 30m" {
		t.Errorf("toRelative with tz = %q, want 4h 30m", got)
	}
}

func TestToRelative_AbsoluteTime(t *testing.T) {
	got := toRelative("5:00pm (Asia/Seoul)")
	if strings.Contains(got, "pm") {
		t.Errorf("should have been converted to relative: %q", got)
	}
	if !strings.Contains(got, "h") && !strings.Contains(got, "m") && !strings.Contains(got, "d") {
		t.Errorf("expected relative time format: %q", got)
	}
}

func TestToRelative_PastDateAdvancesCorrectly(t *testing.T) {
	// A date far in the past (e.g. Jan 1) should not return "0m"
	// It should advance day-by-day until it's in the future
	got := toRelative("Jan 1, 12pm")
	if got == "0m" {
		t.Error("past date should not return 0m")
	}
	if !strings.Contains(got, "d") && !strings.Contains(got, "h") && !strings.Contains(got, "m") {
		t.Errorf("expected relative time format: %q", got)
	}
}

func TestToRelative_Jan1Date(t *testing.T) {
	// "Jan 1, 12pm" should be treated as a date (Jan 1), not as time-only
	// If current date is past Jan 1, should roll over to next year (300+ days)
	got := toRelative("Jan 1, 12pm")
	if got == "0m" {
		t.Error("Jan 1, 12pm should not return 0m")
	}
	now := time.Now()
	jan1 := time.Date(now.Year(), 1, 1, 12, 0, 0, 0, now.Location())
	if jan1.Before(now) {
		// Should have rolled over to next year, expect many days
		if !strings.Contains(got, "d") {
			t.Errorf("past Jan 1 should roll to next year with days, got %q", got)
		}
	}
}

func TestToRelative_DateWithTimezone(t *testing.T) {
	// Should parse timezone and use it for calculation
	got := toRelative("Mar 6, 12pm (Asia/Seoul)")
	if strings.Contains(got, "am") || strings.Contains(got, "pm") {
		t.Errorf("should have been converted: %q", got)
	}
}

func TestToRelative_TimeOnlyFormat(t *testing.T) {
	// Time-only like "3pm" should be treated as today/tomorrow
	got := toRelative("3pm")
	if strings.Contains(got, "pm") {
		t.Errorf("should have been converted: %q", got)
	}
}

func TestToRelative_Unparseable(t *testing.T) {
	// Unparseable strings should be returned as-is
	got := toRelative("something weird")
	if got != "something weird" {
		t.Errorf("unparseable should be returned as-is, got %q", got)
	}
}

func TestFmtDuration_Values(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0m"},
		{5 * time.Minute, "5m"},
		{65 * time.Minute, "1h 5m"},
		{2 * time.Hour, "2h 0m"},
		{25 * time.Hour, "1d 1h"},
		{48*time.Hour + 30*time.Minute, "2d 0h"},
	}
	for _, tt := range tests {
		got := fmtDuration(tt.d)
		if got != tt.want {
			t.Errorf("fmtDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestFmtDuration_Negative(t *testing.T) {
	got := fmtDuration(-5 * time.Minute)
	if got != "0m" {
		t.Errorf("negative duration should return 0m, got %q", got)
	}
}

func TestParseCaptured_Empty(t *testing.T) {
	_, err := parseCaptured("")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseCaptured_NoPercentUsed(t *testing.T) {
	_, err := parseCaptured("no quota data here at all")
	if err == nil {
		t.Error("expected error for input without quota data")
	}
}

func TestParseCaptured_ValidInput(t *testing.T) {
	input := `
Some header text
Current session      40% used
Resets 5:59pm (Asia/Seoul)
all models           20% used
Resets Mar 6, 12pm (Asia/Seoul)
Sonnet only          0% used
Extra usage          35% used
$17.91/$50.00 spent
Resets Mar 6, 12pm (Asia/Seoul)
`
	result, err := parseCaptured(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// session
	session, ok := result["session"].(map[string]any)
	if !ok {
		t.Fatal("missing session")
	}
	if session["used"] != 40 {
		t.Errorf("session used = %v, want 40", session["used"])
	}
	if session["left"] != 60 {
		t.Errorf("session left = %v, want 60", session["left"])
	}
	if _, ok := session["resetsIn"].(string); !ok {
		t.Error("session should have resetsIn")
	}

	// weeklyAll
	weekly, ok := result["weeklyAll"].(map[string]any)
	if !ok {
		t.Fatal("missing weeklyAll")
	}
	if weekly["used"] != 20 {
		t.Errorf("weeklyAll used = %v, want 20", weekly["used"])
	}

	// weeklySonnet
	sonnet, ok := result["weeklySonnet"].(map[string]any)
	if !ok {
		t.Fatal("missing weeklySonnet")
	}
	if sonnet["used"] != 0 {
		t.Errorf("weeklySonnet used = %v, want 0", sonnet["used"])
	}

	// extra
	extra, ok := result["extra"].(map[string]any)
	if !ok {
		t.Fatal("missing extra")
	}
	if extra["used"] != 35 {
		t.Errorf("extra used = %v, want 35", extra["used"])
	}
	if extra["spent"] != "$17.91/$50.00" {
		t.Errorf("extra spent = %v", extra["spent"])
	}
}

func TestParseCaptured_WithANSI(t *testing.T) {
	// parseCaptured doesn't strip ANSI itself, but stripANSI + parseCaptured should work
	raw := "\x1b[32mCurrent session      50% used\x1b[0m"
	cleaned := stripANSI(raw)
	result, err := parseCaptured(cleaned)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	session, ok := result["session"].(map[string]any)
	if !ok {
		t.Fatal("missing session after ANSI strip")
	}
	if session["used"] != 50 {
		t.Errorf("session used = %v, want 50", session["used"])
	}
}

func TestStripANSI(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"\x1b[32mhello\x1b[0m world", "hello world"},
		{"no ansi here", "no ansi here"},
		{"\x1b[1;31mred\x1b[0m", "red"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripANSI(tt.in)
		if got != tt.want {
			t.Errorf("stripANSI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestOperatorPrecedenceFix(t *testing.T) {
	// "Mar 6, 10am" should not be treated as time-only format
	got := toRelative("Mar 6, 10am (Asia/Seoul)")
	if strings.Contains(got, "am") {
		t.Errorf("should have been converted: %q", got)
	}
}

func TestAtoi(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"0", 0},
		{"42", 42},
		{"100abc", 100},
		{"abc", 0},
		{"", 0},
	}
	for _, tt := range tests {
		got := atoi(tt.in)
		if got != tt.want {
			t.Errorf("atoi(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
