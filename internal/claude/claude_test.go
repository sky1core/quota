package claude

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/quotacache"
)

func TestGetQuotaForConfigDirUsesSharedCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")
	configDir := t.TempDir()
	quotacache.Put(claudeCacheKey(configDir), "Current session: 12% used\n", time.Now().Add(time.Hour))

	result, err := GetQuotaForConfigDir(time.Second, configDir, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	session, ok := windowByKey(result, "session")
	if !ok || session["left"] != 88 {
		t.Fatalf("cached session = %v, want left 88", session)
	}
}

func TestInvalidateCacheForConfigDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	configDir := t.TempDir()
	quotacache.Put(claudeCacheKey(configDir), "Current session: 12% used\n", time.Now().Add(time.Hour))

	InvalidateCacheForConfigDir(configDir)

	if _, ok := quotacache.Get(claudeCacheKey(configDir), time.Minute); ok {
		t.Fatal("invalidated Claude cache entry should miss")
	}
}

func TestEarliestReset(t *testing.T) {
	later := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Hour)
	result := map[string]any{"windows": []map[string]any{
		{"key": "session", "resetsAt": later},
		{"key": "weekly_all", "resetsAt": earlier},
		{"key": "extra_1", "resetsIn": "1h"},
	}}

	if got := earliestReset(result); !got.Equal(earlier) {
		t.Fatalf("earliestReset = %v, want %v", got, earlier)
	}
}

func TestEarliestResetReturnsZeroWithoutAbsoluteReset(t *testing.T) {
	result := map[string]any{"windows": []map[string]any{
		{"key": "session", "resetsIn": "1h"},
		{"key": "weekly_all"},
	}}

	if got := earliestReset(result); !got.IsZero() {
		t.Fatalf("earliestReset = %v, want zero", got)
	}
}

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

func TestParseReset_AbsoluteHasAt(t *testing.T) {
	rel, at, hasAt := parseReset("Mar 6, 10am (Asia/Seoul)")
	if !hasAt {
		t.Fatal("absolute reset should yield hasAt=true")
	}
	if rel == "" {
		t.Error("relative should be non-empty")
	}
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	seoul := at.In(loc)
	if seoul.Hour() != 10 || seoul.Minute() != 0 {
		t.Errorf("reset instant = %v, want 10:00 in Asia/Seoul", seoul)
	}
}

func TestParseReset_RelativeNoAt(t *testing.T) {
	if _, _, hasAt := parseReset("4h 30m (Asia/Seoul)"); hasAt {
		t.Error("already-relative input should not yield an absolute instant")
	}
}

func TestParseReset_UnparseableNoAt(t *testing.T) {
	rel, _, hasAt := parseReset("something weird")
	if hasAt {
		t.Error("unparseable input should not yield an absolute instant")
	}
	if rel != "something weird" {
		t.Errorf("unparseable relative = %q, want passthrough", rel)
	}
}

func TestParseUsage_ResetsAt(t *testing.T) {
	input := "Current session: 40% used · resets Mar 6 at 12pm (Asia/Seoul)\n"
	result, err := parseUsage(input)
	if err != nil {
		t.Fatal(err)
	}
	session, ok := windowByKey(result, "session")
	if !ok {
		t.Fatal("missing session")
	}
	if _, ok := session["resetsAt"].(time.Time); !ok {
		t.Errorf("session should carry resetsAt time.Time, got %T", session["resetsAt"])
	}
}

func TestParseUsage_Empty(t *testing.T) {
	_, err := parseUsage("")
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestParseUsage_NoPercentUsed(t *testing.T) {
	_, err := parseUsage("no quota data here at all")
	if err == nil {
		t.Error("expected error for input without quota data")
	}
}

func TestParseUsage_ValidInput(t *testing.T) {
	input := `
Some header text
Current session: 40% used · resets 5:59pm (Asia/Seoul)
Current week (all models): 20% used · resets Mar 6 at 12pm (Asia/Seoul)
Current week (Sonnet only): 0% used
`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// session
	session, ok := windowByKey(result, "session")
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
	weekly, ok := windowByKey(result, "weekly_all")
	if !ok {
		t.Fatal("missing weeklyAll")
	}
	if weekly["used"] != 20 {
		t.Errorf("weeklyAll used = %v, want 20", weekly["used"])
	}

	// third row is a dynamic extra with its on-screen label
	extras, ok := extraWindows(result)
	if !ok {
		t.Fatal("missing extras")
	}
	if len(extras) != 1 {
		t.Fatalf("extras len = %d, want 1", len(extras))
	}
	if extras[0]["label"] != "Sonnet only" {
		t.Errorf("extras[0] label = %v, want Sonnet only", extras[0]["label"])
	}
	if extras[0]["used"] != 0 {
		t.Errorf("extras[0] used = %v, want 0", extras[0]["used"])
	}
}

// TestParseUsage_WithANSI pins that escape sequences do not take the fetch down:
// parseUsage strips them itself, so a styled report degrades to nothing worse
// than plain text rather than failing every row match at once.
func TestParseUsage_WithANSI(t *testing.T) {
	raw := "\x1b[32mCurrent session: 50% used · resets 5:59pm (Asia/Seoul)\x1b[0m"
	result, err := parseUsage(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	session, ok := windowByKey(result, "session")
	if !ok {
		t.Fatal("missing session after ANSI strip")
	}
	if session["used"] != 50 {
		t.Errorf("session used = %v, want 50", session["used"])
	}
	if _, ok := session["resetsIn"].(string); !ok {
		t.Error("session should have resetsIn after ANSI strip")
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

func TestParseUsage_UsageCommand(t *testing.T) {
	input := `You are currently using your subscription to power your Claude Code usage

Current session: 11% used · resets 11pm (Asia/Seoul)
Current week (all models): 83% used · resets 12pm (Asia/Seoul)
Current week (Sonnet only): 1% used · resets Mar 11 at 7pm (Asia/Seoul)`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	session, ok := windowByKey(result, "session")
	if !ok {
		t.Fatal("missing session")
	}
	if session["used"] != 11 || session["left"] != 89 {
		t.Errorf("session: used=%v left=%v", session["used"], session["left"])
	}
	weekly, ok := windowByKey(result, "weekly_all")
	if !ok {
		t.Fatal("missing weeklyAll")
	}
	if weekly["used"] != 83 {
		t.Errorf("weeklyAll used=%v, want 83", weekly["used"])
	}
	extras, ok := extraWindows(result)
	if !ok {
		t.Fatal("missing extras")
	}
	if len(extras) != 1 {
		t.Fatalf("extras len = %d, want 1", len(extras))
	}
	if extras[0]["label"] != "Sonnet only" {
		t.Errorf("extras[0] label = %v, want Sonnet only", extras[0]["label"])
	}
	if extras[0]["used"] != 1 {
		t.Errorf("extras[0] used=%v, want 1", extras[0]["used"])
	}
	if _, ok := extras[0]["resetsIn"].(string); !ok {
		t.Error("extras[0] should have resetsIn")
	}
}

// TestParseUsage_FullReport runs the parser over a full-shaped
// `claude -p "/usage"` report, including the whole "What's contributing"
// section. That section is the parser's main false-match hazard —
// it is full of percentages, in every shape the report uses — so keeping it here
// is what proves the row pattern separates quota rows from the rest.
//
// The section's line SHAPES are what matter and are reproduced faithfully: a
// bare "NN% of …" line, a "label: /name NN%" line, and a "label · N x · N y"
// header. The names and counts are placeholders — this is a public repository,
// and a real report names the machine's own skills and subagents.
func TestParseUsage_FullReport(t *testing.T) {
	input := `You are currently using your subscription to power your Claude Code usage

Current session: 4% used · resets Jul 30 at 12:09pm (Asia/Seoul)
Current week (all models): 1% used · resets Aug 6 at 11:59am (Asia/Seoul)
Current week (Fable): 2% used · resets Aug 6 at 11:59am (Asia/Seoul)

What's contributing to your limits usage?
Approximate, based on local sessions on this machine — does not include other devices or claude.ai. Behaviors are independent characteristics, not a breakdown.

Last 24h · 100 requests · 10 sessions
  73% of your usage came from subagent-heavy sessions
  57% of your usage was at >150k context
  17% of your usage came from sessions active for 8+ hours
  16% of your usage was while 4+ sessions ran in parallel
  Top skills: /skill-one 2%, /skill-two 1%
  Top subagents: agent-one 11%, agent-two 4%

Last 7d · 500 requests · 50 sessions
  78% of your usage came from subagent-heavy sessions
  68% of your usage was at >150k context
  Top skills: /skill-one 1%, /skill-two 1%
  Top subagents: agent-one 12%, agent-two 7%`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	session, ok := windowByKey(result, "session")
	if !ok {
		t.Fatal("missing session")
	}
	if session["used"] != 4 || session["left"] != 96 {
		t.Errorf("session: used=%v left=%v, want 4/96", session["used"], session["left"])
	}
	if r, ok := session["resetsIn"].(string); !ok || strings.Contains(r, "pm") {
		t.Errorf("session resetsIn should be relative, got %v", session["resetsIn"])
	}

	weekly, ok := windowByKey(result, "weekly_all")
	if !ok {
		t.Fatal("missing weeklyAll")
	}
	if weekly["used"] != 1 {
		t.Errorf("weeklyAll used=%v, want 1", weekly["used"])
	}

	extras, ok := extraWindows(result)
	if !ok {
		t.Fatal("missing extras")
	}
	if len(extras) != 1 {
		t.Fatalf("extras len = %d, want 1 (bottom section must not match)", len(extras))
	}
	if extras[0]["label"] != "Fable" {
		t.Errorf("extras[0] label = %v, want Fable", extras[0]["label"])
	}
	if extras[0]["used"] != 2 || extras[0]["left"] != 98 {
		t.Errorf("extras[0]: used=%v left=%v, want 2/98", extras[0]["used"], extras[0]["left"])
	}
	if _, ok := extras[0]["resetsIn"].(string); !ok {
		t.Error("extras[0] should have resetsIn")
	}
}

// TestParseUsage_NonQuotaRowsRejected is a REGRESSION GUARD against the quiet
// failure: a line from the "What's contributing" section that happens to read
// "<something>: N% used …" must not become a window. Those numbers are shares of
// usage, not quota — surfacing one as a model row would put a wrong percentage
// in front of the user with nothing to signal it. Only rows naming a current
// window ("Current …") are quota.
func TestParseUsage_NonQuotaRowsRejected(t *testing.T) {
	input := `Current session: 4% used · resets Jul 30 at 12:09pm (Asia/Seoul)
Current week (all models): 36% used · resets Aug 3 at 12pm (Asia/Seoul)

What's contributing to your limits usage?
Last 24h: 73% used by subagent-heavy sessions
Top skills: 12% used
Peak context: 57% used`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if extras, ok := extraWindows(result); ok {
		t.Errorf("non-quota lines became windows: %v", extras)
	}
	ws, _ := result["windows"].([]map[string]any)
	if len(ws) != 2 {
		t.Fatalf("windows = %d, want exactly the 2 quota rows: %v", len(ws), ws)
	}
}

func TestParseUsage_SessionUsageSummaryOnlyError(t *testing.T) {
	input := `Total cost:            $0.0000
Total duration (API):  0s
Total duration (wall): 0s
Total code changes:    0 lines added, 0 lines removed
Usage:                 0 input, 0 output, 0 cache read, 0 cache write`

	_, err := parseUsage(input)
	if err == nil {
		t.Fatal("session usage summary without quota rows must fail")
	}
	if !strings.Contains(err.Error(), "only session usage summary was returned") {
		t.Fatalf("error = %q", err)
	}
}

func TestParseUsage_MultipleExtras(t *testing.T) {
	// The last row has no reset clause — a window Claude has not started yet.
	// It must still be reported, just without resetsIn.
	input := `
Current session: 4% used · resets 12:09pm (Asia/Seoul)
Current week (all models): 1% used · resets Aug 6 at 11:59am (Asia/Seoul)
Current week (Fable): 2% used · resets Aug 6 at 11:59am (Asia/Seoul)
Current week (Model B): 10% used`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	extras, ok := extraWindows(result)
	if !ok {
		t.Fatal("missing extras")
	}
	if len(extras) != 2 {
		t.Fatalf("extras len = %d, want 2", len(extras))
	}
	// Screen order preserved
	if extras[0]["label"] != "Fable" || extras[1]["label"] != "Model B" {
		t.Errorf("extras order = %v, %v; want Fable, Model B", extras[0]["label"], extras[1]["label"])
	}
	if extras[1]["used"] != 10 {
		t.Errorf("extras[1] used = %v, want 10", extras[1]["used"])
	}
	if _, ok := extras[1]["resetsIn"]; ok {
		t.Error("extras[1] has no Resets row, resetsIn should be absent")
	}
}

func TestParseUsage_WeeklyOnlyNoSessionNoErrors(t *testing.T) {
	input := `Current week (all models): 20% used · resets Mar 6 at 12pm (Asia/Seoul)
Current week (Fable): 5% used`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := windowByKey(result, "session"); ok {
		t.Error("no session row was present; session window must be absent")
	}
	if _, ok := result["windowErrors"]; ok {
		t.Errorf("absent session is not malformed, must not carry windowErrors: %v", result["windowErrors"])
	}
	if _, ok := windowByKey(result, "weekly_all"); !ok {
		t.Error("weekly window must be preserved")
	}
}

func TestParseUsage_MalformedPercentAlongsideValid(t *testing.T) {
	input := `Current session: N/A% used · resets 5pm (Asia/Seoul)
Current week (all models): 20% used · resets Mar 6 at 12pm (Asia/Seoul)`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := windowByKey(result, "session"); ok {
		t.Error("unreadable session must not become a window")
	}
	if _, ok := windowByKey(result, "weekly_all"); !ok {
		t.Error("valid weekly row must be preserved")
	}
	we, ok := result["windowErrors"].([]string)
	if !ok || len(we) != 1 {
		t.Fatalf("windowErrors = %v, want one entry for the malformed session row", result["windowErrors"])
	}
	if !strings.Contains(we[0], "Session") {
		t.Errorf("windowError should name the row label, got %q", we[0])
	}
}

func TestParseUsage_OutOfRangePercent(t *testing.T) {
	input := `Current session: 150% used
Current week (all models): 20% used`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := windowByKey(result, "session"); ok {
		t.Error("out-of-range session must not become a window")
	}
	we, ok := result["windowErrors"].([]string)
	if !ok || len(we) != 1 {
		t.Fatalf("windowErrors = %v, want one out-of-range diagnostic", result["windowErrors"])
	}
}

func TestParseUsage_OverflowPercent(t *testing.T) {
	input := "Current session: 999999999999999999999% used\nCurrent week (all models): 10% used"
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := windowByKey(result, "session"); ok {
		t.Error("overflowing percent must not become a window")
	}
	we, _ := result["windowErrors"].([]string)
	if len(we) != 1 {
		t.Fatalf("windowErrors = %v, want one entry", result["windowErrors"])
	}
}

func TestParseUsageReportsEachMalformedAggregateRow(t *testing.T) {
	input := `Current session: N/A% used
Current week (all models): ??% used
Current week (Fable): 10% used`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	we, ok := result["windowErrors"].([]string)
	if !ok || len(we) != 2 {
		t.Fatalf("windowErrors = %v, want two diagnostics", result["windowErrors"])
	}
	extras, ok := extraWindows(result)
	if !ok || len(extras) != 1 || extras[0]["label"] != "Fable" {
		t.Errorf("valid Fable row must survive, got %v", extras)
	}
}

func TestParseUsage_ZeroUsageNoDiagnostic(t *testing.T) {
	input := "Current session: 0% used\nCurrent week (all models): 0% used"
	result, err := parseUsage(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := result["windowErrors"]; ok {
		t.Errorf("0%% usage is valid, must not carry windowErrors: %v", result["windowErrors"])
	}
	session, ok := windowByKey(result, "session")
	if !ok || session["left"] != 100 || session["used"] != 0 {
		t.Errorf("session = %v, want used 0 / left 100", session)
	}
}

func TestParseUsage_AllMalformedErrors(t *testing.T) {
	input := `Current session: N/A% used
Current week (all models): ??% used`
	if _, err := parseUsage(input); err == nil {
		t.Fatal("no valid quota rows must remain a parse error")
	}
}

// windowByKey finds a window in the parsed self-describing list by its key.
func windowByKey(result map[string]any, key string) (map[string]any, bool) {
	ws, ok := result["windows"].([]map[string]any)
	if !ok {
		return nil, false
	}
	for _, w := range ws {
		if w["key"] == key {
			return w, true
		}
	}
	return nil, false
}

// extraWindows returns the per-model rows (keys "extra_N") in order.
func extraWindows(result map[string]any) ([]map[string]any, bool) {
	ws, ok := result["windows"].([]map[string]any)
	if !ok {
		return nil, false
	}
	var out []map[string]any
	for _, w := range ws {
		if k, _ := w["key"].(string); strings.HasPrefix(k, "extra_") {
			out = append(out, w)
		}
	}
	return out, len(out) > 0
}

// TestWindowLabel pins that the label comes from Claude's own /usage text and is
// never a hardcoded vocabulary: if Claude renames the period, the label follows.
func TestWindowLabel(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Current session", "Session"},
		{"Current week (all models)", "Week"},
		{"Current week (Fable)", "Fable"},
		{"Current week (Sonnet only)", "Sonnet only"},
		// Period rename must flow straight through — this is the whole point.
		{"Current 5 days (all models)", "5 days"},
		{"Current 5 days (Fable)", "Fable"},
		{"Current month (all models)", "Month"},
		// Degenerate/unknown text passes through rather than being renamed.
		{"Some other row", "Some other row"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := windowLabel(tt.in); got != tt.want {
			t.Errorf("windowLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestWindowKeys_Finite: the vocabulary consumers pre-allocate slots from.
func TestWindowKeys_Finite(t *testing.T) {
	want := []string{"session", "weekly_all", "extra_1", "extra_2", "extra_3"}
	got := WindowKeys()
	if len(got) != len(want) {
		t.Fatalf("WindowKeys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("WindowKeys() = %v, want %v", got, want)
		}
	}
}

// TestFetchEnv_ConfigDirReplacesInherited is a REGRESSION GUARD for measuring
// the wrong account: when a config dir is requested, an inherited
// CLAUDE_CONFIG_DIR must be removed, not merely followed by the new one. Two
// assignments for the same name in an environment is a coin flip resolved by
// the OS, and losing that flip reports the caller's own account's numbers under
// the other account's key.
func TestFetchEnv_ConfigDirReplacesInherited(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/inherited")

	env := fetchEnv("/wanted")

	var got []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "CLAUDE_CONFIG_DIR=") {
			got = append(got, kv)
		}
	}
	if len(got) != 1 {
		t.Fatalf("CLAUDE_CONFIG_DIR assignments = %v, want exactly one", got)
	}
	if got[0] != "CLAUDE_CONFIG_DIR=/wanted" {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want /wanted", got[0])
	}
}

// TestFetchEnv_NoConfigDirKeepsInherited pins the other half of the contract:
// an empty configDir means "the caller's default account", which is exactly the
// inherited CLAUDE_CONFIG_DIR. Dropping it here would silently switch the
// default account to ~/.claude.
func TestFetchEnv_NoConfigDirKeepsInherited(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/inherited")

	env := fetchEnv("")

	found := false
	for _, kv := range env {
		if kv == "CLAUDE_CONFIG_DIR=/inherited" {
			found = true
		}
	}
	if !found {
		t.Error("empty configDir must leave the inherited CLAUDE_CONFIG_DIR in place")
	}
}

// TestFetchEnv_ScrubsAccountOverrides pins that the probe reads the logged-in
// subscription account: a custom endpoint or auth token in the caller's
// environment would otherwise decide whose quota gets reported, and CLAUDECODE
// makes the CLI treat this as a nested session.
func TestFetchEnv_ScrubsAccountOverrides(t *testing.T) {
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("ANTHROPIC_API_HOST", "https://example.invalid")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "secret")
	t.Setenv("ANTHROPIC_BASE_URL", "https://example.invalid")
	t.Setenv("CLAUDE_API_KEY", "secret")
	t.Setenv("CLAUDE_CODE_API_BASE_URL", "https://example.invalid")
	t.Setenv("CLAUDE_CODE_OAUTH_REFRESH_TOKEN", "secret")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "secret")
	t.Setenv("PATH_MARKER_FOR_TEST", "kept")

	env := fetchEnv("")

	for _, kv := range env {
		for _, banned := range []string{
			"CLAUDECODE=", "ANTHROPIC_API_HOST=", "ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_BASE_URL=",
			"CLAUDE_API_KEY=", "CLAUDE_CODE_API_BASE_URL=", "CLAUDE_CODE_OAUTH_REFRESH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=",
		} {
			if strings.HasPrefix(kv, banned) {
				t.Errorf("%s must be scrubbed from the probe environment", strings.TrimSuffix(banned, "="))
			}
		}
	}
	kept := false
	for _, kv := range env {
		if kv == "PATH_MARKER_FOR_TEST=kept" {
			kept = true
		}
	}
	if !kept {
		t.Error("unrelated environment variables must be passed through")
	}
}

// TestUsageText_Success pins that the report is read out of the CLI's JSON
// envelope rather than off raw stdout.
func TestUsageText_Success(t *testing.T) {
	out := []byte(`{"is_error":false,"subtype":"success","num_turns":0,"result":"Current session: 3% used"}`)
	got, err := usageText(out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "Current session: 3% used" {
		t.Errorf("usageText = %q", got)
	}
}

// TestUsageText_IsError pins that the CLI's own verdict is honored. The failure
// this guards is silent: an error envelope carries no usage rows, so treating it
// as a report would surface as an unexplained parse failure instead of the
// reason the CLI gave (e.g. a logged-out account).
func TestUsageText_IsError(t *testing.T) {
	out := []byte(`{"is_error":true,"subtype":"error_during_execution","result":"Invalid API key"}`)
	_, err := usageText(out)
	if err == nil {
		t.Fatal("expected error for is_error=true envelope")
	}
	if !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("error should carry the CLI's message, got: %v", err)
	}
}

// TestUsageText_NotJSON pins that unreadable output fails loudly with a preview,
// so a CLI that stops emitting JSON is diagnosable from the error alone.
func TestUsageText_NotJSON(t *testing.T) {
	_, err := usageText([]byte("command not found: claude"))
	if err == nil {
		t.Fatal("expected error for non-JSON output")
	}
	if !strings.Contains(err.Error(), "command not found") {
		t.Errorf("error should preview the raw output, got: %v", err)
	}
}

// TestParseUsage_ExtraRowsNotCapped is a REGRESSION GUARD: the parser reports
// every per-model row Claude shows. quota-bar's finite menu is quota-bar's
// problem — a slot limit must never delete a real row from the data (quota-cli).
func TestParseUsage_ExtraRowsNotCapped(t *testing.T) {
	input := `
Current session: 10% used
Current week (all models): 20% used
Current week (Fable): 30% used
Current week (Model B): 40% used
Current week (Model C): 50% used
Current week (Model D): 60% used
`
	result, err := parseUsage(input)
	if err != nil {
		t.Fatal(err)
	}
	extras, ok := extraWindows(result)
	if !ok {
		t.Fatal("missing per-model rows")
	}
	// Four model rows — more than the consumer slot vocabulary (extraSlotVocab=3).
	want := []string{"Fable", "Model B", "Model C", "Model D"}
	if len(extras) != len(want) {
		t.Fatalf("parser must not cap model rows: got %d, want %d (%v)", len(extras), len(want), extras)
	}
	for i, lbl := range want {
		if extras[i]["label"] != lbl {
			t.Errorf("extras[%d] label = %v, want %q", i, extras[i]["label"], lbl)
		}
		if extras[i]["key"] != fmt.Sprintf("extra_%d", i+1) {
			t.Errorf("extras[%d] key = %v, want extra_%d", i, extras[i]["key"], i+1)
		}
	}
}
