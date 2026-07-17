package claude

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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

func TestParseCaptured_ResetsAt(t *testing.T) {
	input := `
Current session      40% used
Resets Mar 6, 12pm (Asia/Seoul)
`
	result, err := parseCaptured(input)
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
`
	result, err := parseCaptured(input)
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

func TestParseCaptured_WithANSI(t *testing.T) {
	// parseCaptured doesn't strip ANSI itself, but stripANSI + parseCaptured should work
	raw := "\x1b[32mCurrent session      50% used\x1b[0m"
	cleaned := stripANSI(raw)
	result, err := parseCaptured(cleaned)
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

func TestParseCaptured_UsageCommand(t *testing.T) {
	// Actual output from `/usage` command (captured 2026-03-05)
	input := `  Settings:  Status   Config   Usage  (←/→ or tab to cycle)


  Current session
  █████▌                                             11% used
  Resets 11pm (Asia/Seoul)

  Current week (all models)
  █████████████████████████████████████████▌         83% used
  Resets 12pm (Asia/Seoul)

  Current week (Sonnet only)
  ▌                                                  1% used
  Resets Mar 11, 7pm (Asia/Seoul)

  Esc to cancel`
	result, err := parseCaptured(input)
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

func TestParseCaptured_UsageFableCapture(t *testing.T) {
	// Actual output from `/usage` (captured 2026-07-02): third row label
	// changed from "Current week (Sonnet only)" to "Current week (Fable)".
	// Includes the full screen to prove the bottom "% of usage" section
	// does not produce false matches.
	input := `
 ▐▛███▜▌   Claude Code v2.1.198
▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   Settings  Status   Config   Usage   Stats

   Session

   Total cost:            $0.0000
   Total duration (API):  0s
   Total duration (wall): 1s
   Total code changes:    0 lines added, 0 lines removed
   Usage:                 0 input, 0 output, 0 cache read, 0 cache write

   Current session
   ██                                                 4% used
   Resets 12:09pm (Asia/Seoul)

   Current week (all models)
   ▌                                                  1% used
   Resets Jul 6 at 11:59am (Asia/Seoul)

   Current week (Fable)
   █                                                  2% used
   Resets Jul 6 at 11:59am (Asia/Seoul)

   What's contributing to your limits usage?
   Approximate, based on local sessions on this machine — does not include other devices or claude.ai

   Last 24h · these are independent characteristics of your usage, not a breakdown

   20% of your usage came from /donas
    Heavy skills can be scoped down or run with a cheaper model via skill
    frontmatter.

   Skills                  % of usage
   /donas                         20%

   d to day · w to week

   Esc to cancel`
	result, err := parseCaptured(input)
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

func TestParseCaptured_MultipleExtras(t *testing.T) {
	input := `
   Current session
   ██                                                 4% used
   Resets 12:09pm (Asia/Seoul)

   Current week (all models)
   ▌                                                  1% used
   Resets Jul 6 at 11:59am (Asia/Seoul)

   Current week (Fable)
   █                                                  2% used
   Resets Jul 6 at 11:59am (Asia/Seoul)

   Current week (Opus)
   █████                                              10% used

   Esc to cancel`
	result, err := parseCaptured(input)
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
	if extras[0]["label"] != "Fable" || extras[1]["label"] != "Opus" {
		t.Errorf("extras order = %v, %v; want Fable, Opus", extras[0]["label"], extras[1]["label"])
	}
	if extras[1]["used"] != 10 {
		t.Errorf("extras[1] used = %v, want 10", extras[1]["used"])
	}
	if _, ok := extras[1]["resetsIn"]; ok {
		t.Error("extras[1] has no Resets row, resetsIn should be absent")
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

func TestClaudeSessionArgs_ConfigDirOrder(t *testing.T) {
	args := claudeSessionArgs("sess", "/safe", "/cfgdir", "/bin/claude")

	idx := func(v string) int {
		for i, a := range args {
			if a == v {
				return i
			}
		}
		return -1
	}
	lastU := -1
	for i, a := range args {
		if a == "-u" {
			lastU = i
		}
	}
	envIdx := idx("env")
	cfgIdx := idx("CLAUDE_CONFIG_DIR=/cfgdir")
	binIdx := idx("/bin/claude")

	if envIdx < 0 || lastU < 0 || cfgIdx < 0 || binIdx < 0 {
		t.Fatalf("missing expected args: %v", args)
	}
	// env < all -u options < CLAUDE_CONFIG_DIR= < claude binary (macOS env rule)
	if !(envIdx < lastU && lastU < cfgIdx && cfgIdx < binIdx) {
		t.Errorf("wrong order: env=%d lastU=%d cfg=%d bin=%d (%v)", envIdx, lastU, cfgIdx, binIdx, args)
	}
	if args[len(args)-1] != "/bin/claude" {
		t.Errorf("claude binary must be last, got %q", args[len(args)-1])
	}
}

func TestClaudeSessionArgs_NoConfigDir(t *testing.T) {
	args := claudeSessionArgs("sess", "/safe", "", "/bin/claude")
	for _, a := range args {
		if strings.HasPrefix(a, "CLAUDE_CONFIG_DIR") {
			t.Errorf("must not inject CLAUDE_CONFIG_DIR when configDir empty: %v", args)
		}
	}
	if args[len(args)-1] != "/bin/claude" {
		t.Errorf("claude binary must be last, got %q", args[len(args)-1])
	}
}

func TestClaudeSessionArgs_PrintsSessionID(t *testing.T) {
	args := claudeSessionArgs("sess", "/safe", "", "/bin/claude")
	joined := strings.Join(args, " ")
	// Creation must print the session ID (-P -F '#{session_id}'): every later
	// tmux command targets that ID, because name targets fall back to prefix
	// matching and can silently resolve to a sibling account's session.
	if !strings.Contains(joined, "-P -F #{session_id}") {
		t.Errorf("new-session args must request the session id, got: %v", args)
	}
}

// TestScreenComplete pins the settle gate: a frame whose usage bars are drawn
// but whose Resets lines are still loading must not be treated as final —
// parsing it is exactly how rows intermittently lost their reset times.
func TestScreenComplete(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "every bar has its Resets line",
			text: "Current session\n██ 8% used\nResets 5:50pm (Asia/Seoul)\n\n" +
				"Current week (all models)\n█ 2% used\nResets Jul 20 at 12pm (Asia/Seoul)\n",
			want: true,
		},
		{
			name: "last bar still missing its Resets line",
			text: "Current session\n██ 8% used\nResets 5:50pm (Asia/Seoul)\n\n" +
				"Current week (all models)\n█ 2% used\n",
			want: false,
		},
		{
			name: "next block header rendered before this bar's Resets",
			text: "Current session\n██ 8% used\nCurrent week (all models)\n█ 2% used\nResets Jul 20 at 12pm\n",
			want: false,
		},
		{
			name: "no bars at all is vacuously complete",
			text: "Claude Code\nloading...\n",
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := screenComplete(c.text); got != c.want {
				t.Errorf("screenComplete = %v, want %v\n%s", got, c.want, c.text)
			}
		})
	}
}

// requireTmux creates a real tmux session running the production new-session
// argument list (with a harmless idle command standing in for the claude
// binary) and returns its session ID. Skips when tmux is unavailable.
func requireTmux(t *testing.T, ctx context.Context, name string) string {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	id, err := createClaudeSession(ctx, os.Environ(), name, t.TempDir(), "", "cat")
	if err != nil {
		t.Fatalf("createClaudeSession(%q): %v", name, err)
	}
	if !strings.HasPrefix(id, "$") {
		t.Fatalf("session id must be ID-shaped ($n), got %q", id)
	}
	// Self-cleaning on every exit path, including t.Fatal before the test's own
	// kills; killing an already-dead session is a no-op.
	t.Cleanup(func() { killTmuxSession(id) })
	return id
}

// TestKillTmuxSession_DoesNotKillSiblingSession is a REGRESSION GUARD for the
// cross-account kill: the default account's session name used to be a strict
// prefix of the other account's ("quota-1" vs "quota-1-<hash>"), and cleanup
// targeted the NAME — so once the default session had already ended, tmux
// prefix matching redirected its cleanup onto the sibling's live session and
// killed the other account's fetch mid-capture (intermittent missing reset
// times / vanished rows). Cleanup must be a no-op when its own session is
// gone, whatever other sessions exist.
func TestKillTmuxSession_DoesNotKillSiblingSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	base := fmt.Sprintf("quotatest-%d-%08x", os.Getpid(), time.Now().UnixNano()&0xffffffff)
	idA := requireTmux(t, ctx, base)
	idB := requireTmux(t, ctx, base+"-aaaaaaaa") // name extends A's name, like the old per-account naming

	// Simulate the bug window: A's session has already ended when A's cleanup runs.
	if err := exec.Command("tmux", "kill-session", "-t", idA).Run(); err != nil {
		t.Fatalf("pre-kill of session A failed: %v", err)
	}
	killTmuxSession(idA)

	if err := exec.Command("tmux", "has-session", "-t", idB).Run(); err != nil {
		t.Fatalf("sibling session was killed: cleanup crossed session boundaries")
	}

	// Positive path: cleanup does kill its own live session.
	killTmuxSession(idB)
	if err := exec.Command("tmux", "has-session", "-t", idB).Run(); err == nil {
		t.Fatalf("killTmuxSession left its own session running")
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

// TestParseCaptured_ExtraRowsNotCapped is a REGRESSION GUARD: the parser reports
// every per-model row Claude shows. quota-bar's finite menu is quota-bar's
// problem — a slot limit must never delete a real row from the data (quota-cli).
func TestParseCaptured_ExtraRowsNotCapped(t *testing.T) {
	input := `
Current session      10% used
Current week (all models)   20% used
Current week (Fable)   30% used
Current week (Opus)    40% used
Current week (Sonnet)  50% used
Current week (Haiku)   60% used
`
	result, err := parseCaptured(input)
	if err != nil {
		t.Fatal(err)
	}
	extras, ok := extraWindows(result)
	if !ok {
		t.Fatal("missing per-model rows")
	}
	// Four model rows — more than the consumer slot vocabulary (extraSlotVocab=3).
	want := []string{"Fable", "Opus", "Sonnet", "Haiku"}
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
