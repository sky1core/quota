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

func TestFmtLine_NoResets(t *testing.T) {
	it := item{label: "Sonnet", left: "100%"}
	got := fmtLine(it)
	if !strings.Contains(got, "Sonnet") || !strings.Contains(got, "100%") {
		t.Errorf("unexpected: %q", got)
	}
	// Should just be label + left, nothing else fancy
	if strings.Contains(got, "at ") || strings.Contains(got, "$") {
		t.Errorf("should not have decoration: %q", got)
	}
}

func TestFormatResetAt(t *testing.T) {
	// Built in Local so FormatReset's .Local() is a no-op and the date/time
	// are stable regardless of the test machine's timezone.
	tm := time.Date(2026, 7, 6, 15, 4, 0, 0, time.Local)
	if got := FormatResetAt(tm); got != "Jul 6 15:04" {
		t.Errorf("FormatResetAt = %q, want %q", got, "Jul 6 15:04")
	}
}

func TestFmtLine_ResetsAtIsExact(t *testing.T) {
	// With resetsAt present, the absolute time is taken verbatim from it
	// (a fixed 2026 date), NOT reconstructed from "5d 15h" (which would be
	// now-relative). So the literal fixed date must appear.
	at := time.Date(2026, 7, 6, 15, 4, 0, 0, time.Local)
	it := item{label: "Weekly", left: "83%", resets: "5d 15h", resetAt: at, hasAt: true}
	got := fmtLine(it)
	if !strings.Contains(got, "at Jul 6 15:04") {
		t.Errorf("fmtLine should use resetsAt exactly (at Jul 6 15:04): %q", got)
	}
	if !strings.Contains(got, "5d 15h") {
		t.Errorf("fmtLine should keep the relative string too: %q", got)
	}
}

func TestText_ResetsAtFormatted(t *testing.T) {
	at := time.Date(2026, 7, 6, 15, 4, 0, 0, time.Local)
	payload := map[string]any{
		"claude": map[string]any{
			"session": map[string]any{"left": 80, "resetsIn": "2h", "resetsAt": at},
		},
	}
	got := Text(payload)
	if !strings.Contains(got, "at Jul 6 15:04") {
		t.Errorf("Text should render resetsAt: %q", got)
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
			"session":   map[string]any{"left": 80, "resetsIn": "2h"},
			"weeklyAll": map[string]any{"left": 85, "resetsIn": "5d"},
			"extras": []map[string]any{
				{"label": "Fable", "left": 100},
			},
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
	// All three claude rows present; extras use their on-screen label
	for _, lbl := range []string{"Session", "Weekly", "Fable"} {
		if !strings.Contains(got, lbl) {
			t.Errorf("missing claude row %q", lbl)
		}
	}
	if !strings.Contains(got, "100%") {
		t.Error("missing extras value")
	}
}

func TestText_MultipleClaudeAccounts(t *testing.T) {
	payload := map[string]any{
		"claude": map[string]any{
			"session": map[string]any{"left": 90, "resetsIn": "3h"},
		},
		"claude-2": map[string]any{
			"session": map[string]any{"left": 50},
			"extras":  []map[string]any{{"label": "Fable", "left": 80}},
		},
	}
	got := Text(payload)

	c1 := strings.Index(got, "Claude\n")
	c2 := strings.Index(got, "Claude 2\n")
	if c1 < 0 {
		t.Fatalf("missing default Claude header: %q", got)
	}
	if c2 < 0 {
		t.Fatalf("missing Claude 2 header: %q", got)
	}
	if c1 > c2 {
		t.Errorf("default Claude must come before Claude 2 (c1=%d c2=%d)", c1, c2)
	}
	if !strings.Contains(got, "Fable") || !strings.Contains(got, "80%") {
		t.Errorf("missing claude-2 Fable extra: %q", got)
	}
	if !strings.Contains(got, "90%") || !strings.Contains(got, "50%") {
		t.Errorf("missing per-account session values: %q", got)
	}
}

func TestClaudeAccountKeys_Order(t *testing.T) {
	payload := map[string]any{
		"claude-10": map[string]any{},
		"claude-2":  map[string]any{},
		"claude":    map[string]any{},
		"codex":     map[string]any{}, // must be excluded
		"errors":    []any{},          // non-map, excluded
	}
	got := claudeAccountKeys(payload)
	want := []string{"claude", "claude-2", "claude-10"} // numeric, not lexical
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestClaudeLabel(t *testing.T) {
	cases := map[string]string{
		"claude":    "Claude",
		"claude-2":  "Claude 2",
		"claude-10": "Claude 10",
	}
	for k, want := range cases {
		if got := claudeLabel(k); got != want {
			t.Errorf("claudeLabel(%q) = %q, want %q", k, got, want)
		}
	}
}

func TestText_ExtrasWithoutLabelSkipped(t *testing.T) {
	payload := map[string]any{
		"claude": map[string]any{
			"session": map[string]any{"left": 80},
			"extras": []map[string]any{
				{"left": 55}, // no label → skipped
			},
		},
	}
	got := Text(payload)
	if strings.Contains(got, "55%") {
		t.Errorf("label-less extra should be skipped: %q", got)
	}
}
