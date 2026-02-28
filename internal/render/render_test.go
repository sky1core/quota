package render

import (
	"strings"
	"testing"
	"time"
)

func TestFmtLine_Basic(t *testing.T) {
	it := item{label: "Session", left: "80%"}
	got := fmtLine(it)
	if !strings.Contains(got, "Session") || !strings.Contains(got, "80%") {
		t.Errorf("unexpected: %q", got)
	}
	// No resets info should be present
	if strings.Contains(got, "(") {
		t.Errorf("should not contain parentheses without resets: %q", got)
	}
}

func TestFmtLine_WithResets(t *testing.T) {
	it := item{label: "Weekly", left: "83%", resets: "5d 15h"}
	got := fmtLine(it)
	if !strings.Contains(got, "5d 15h") {
		t.Errorf("missing resets: %q", got)
	}
	if !strings.Contains(got, "at ") {
		t.Errorf("missing absolute time: %q", got)
	}
}

func TestFmtLine_WithExtra(t *testing.T) {
	it := item{label: "Extra", left: "65%", resets: "1h", extra: "$17/$50"}
	got := fmtLine(it)
	if !strings.Contains(got, "$17/$50") {
		t.Errorf("missing extra: %q", got)
	}
	if !strings.Contains(got, "65%") {
		t.Errorf("missing percentage: %q", got)
	}
}

func TestFmtLine_NoResets_NoExtra(t *testing.T) {
	it := item{label: "Sonnet", left: "100%"}
	got := fmtLine(it)
	if !strings.Contains(got, "Sonnet") || !strings.Contains(got, "100%") {
		t.Errorf("unexpected: %q", got)
	}
	// Should just be label + left, nothing else fancy
	if strings.Contains(got, "at ") || strings.Contains(got, "$") {
		t.Errorf("should not have extra info: %q", got)
	}
}

func TestEndTime_Hours(t *testing.T) {
	end, ok := endTime("2h 30m")
	if !ok {
		t.Fatal("expected ok")
	}
	// Should be today's time in HH:MM format (no date)
	if !strings.Contains(end, ":") {
		t.Errorf("unexpected format: %q", end)
	}
	// Should not contain month name
	for _, m := range []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"} {
		if strings.Contains(end, m) {
			t.Errorf("hours-only should not have date: %q", end)
			break
		}
	}
}

func TestEndTime_Days(t *testing.T) {
	end, ok := endTime("5d 3h")
	if !ok {
		t.Fatal("expected ok")
	}
	// Should include month/day since > 24h
	expected := time.Now().Add(5*24*time.Hour + 3*time.Hour)
	if !strings.Contains(end, expected.Format("Jan")) {
		t.Errorf("expected month name in %q", end)
	}
}

func TestEndTime_MinutesOnly(t *testing.T) {
	end, ok := endTime("45m")
	if !ok {
		t.Fatal("expected ok")
	}
	if !strings.Contains(end, ":") {
		t.Errorf("expected HH:MM format: %q", end)
	}
}

func TestEndTime_Invalid(t *testing.T) {
	_, ok := endTime("unknown")
	if ok {
		t.Error("expected not ok for unparseable string")
	}
}

func TestEndTime_Empty(t *testing.T) {
	_, ok := endTime("")
	if ok {
		t.Error("expected not ok for empty string")
	}
}

func TestText_NoData(t *testing.T) {
	got := Text(map[string]any{})
	if !strings.Contains(got, "(no data)") {
		t.Errorf("expected no data message: %q", got)
	}
	// Should have both Claude and Codex sections
	if !strings.Contains(got, "Claude") {
		t.Error("missing Claude header")
	}
	if !strings.Contains(got, "Codex") {
		t.Error("missing Codex header")
	}
}

func TestText_WithClaude(t *testing.T) {
	payload := map[string]any{
		"claude": map[string]any{
			"session": map[string]any{"left": 80, "resetsIn": "2h"},
		},
	}
	got := Text(payload)
	if !strings.Contains(got, "Session") || !strings.Contains(got, "80%") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestText_WithCodex(t *testing.T) {
	payload := map[string]any{
		"codex": map[string]any{
			"fiveHour": map[string]any{"left": 90, "resetsIn": "3h"},
			"day":      map[string]any{"left": 70},
		},
	}
	got := Text(payload)
	if !strings.Contains(got, "5h") || !strings.Contains(got, "90%") {
		t.Errorf("missing 5h data: %q", got)
	}
	if !strings.Contains(got, "Day") || !strings.Contains(got, "70%") {
		t.Errorf("missing day data: %q", got)
	}
}

func TestText_WithErrors(t *testing.T) {
	payload := map[string]any{
		"errors": []any{"claude: timeout", "codex: not found"},
	}
	got := Text(payload)
	if !strings.Contains(got, "Errors") {
		t.Error("missing Errors section")
	}
	if !strings.Contains(got, "claude: timeout") {
		t.Error("missing claude error")
	}
	if !strings.Contains(got, "codex: not found") {
		t.Error("missing codex error")
	}
}

func TestText_TimestampFormat(t *testing.T) {
	got := Text(map[string]any{})
	if !strings.Contains(got, "Generated: ") {
		t.Errorf("missing Generated: prefix in timestamp: %q", got)
	}
	// Extract the timestamp part and verify RFC3339 format
	idx := strings.Index(got, "Generated: ")
	if idx < 0 {
		t.Fatal("no Generated: found")
	}
	ts := strings.TrimSpace(got[idx+len("Generated: "):])
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("timestamp not RFC3339: %q, err: %v", ts, err)
	}
}

func TestText_FullPayload(t *testing.T) {
	payload := map[string]any{
		"claude": map[string]any{
			"session":      map[string]any{"left": 80, "resetsIn": "2h"},
			"weeklyAll":    map[string]any{"left": 85, "resetsIn": "5d"},
			"weeklySonnet": map[string]any{"left": 100},
			"extra":        map[string]any{"left": 65, "resetsIn": "6d", "spent": "$17/$50"},
		},
		"codex": map[string]any{
			"fiveHour": map[string]any{"left": 90, "resetsIn": "3h"},
			"day":      map[string]any{"left": 70, "resetsIn": "5d"},
		},
	}
	got := Text(payload)
	// All sections present
	if !strings.Contains(got, "Claude") {
		t.Error("missing Claude")
	}
	if !strings.Contains(got, "Codex") {
		t.Error("missing Codex")
	}
	// No "(no data)" when data is provided
	if strings.Contains(got, "(no data)") {
		t.Error("should not show (no data) when data exists")
	}
	// Extra info present
	if !strings.Contains(got, "$17/$50") {
		t.Error("missing extra spent info")
	}
}
