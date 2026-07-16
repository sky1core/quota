package codex

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWinToEntry_Nil(t *testing.T) {
	if got := winToEntry(nil); got != nil {
		t.Errorf("expected nil for nil window, got %v", got)
	}
}

func TestWinToEntry_Basic(t *testing.T) {
	w := &rateLimitWindow{UsedPercent: 30}
	got := winToEntry(w)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got["left"] != 70 {
		t.Errorf("left = %v, want 70", got["left"])
	}
	// Unknown reset: the key must be absent (not present-as-nil), otherwise
	// --json would serialize "resetsIn": null, violating the string contract.
	if _, ok := got["resetsIn"]; ok {
		t.Errorf("resetsIn key should be absent without ResetsAt, got %v", got["resetsIn"])
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "resetsIn") {
		t.Errorf("resetsIn must not appear in JSON when unknown: %s", b)
	}
}

func TestWinToEntry_WithResetsAt(t *testing.T) {
	future := time.Now().Add(2*time.Hour + 30*time.Minute).Unix()
	w := &rateLimitWindow{UsedPercent: 10, ResetsAt: &future}
	got := winToEntry(w)
	if got == nil {
		t.Fatal("expected non-nil")
	}
	if got["left"] != 90 {
		t.Errorf("left = %v, want 90", got["left"])
	}
	resetsIn, ok := got["resetsIn"].(string)
	if !ok || resetsIn == "" {
		t.Error("resetsIn should be a non-empty string")
	}
}

func TestWinToEntry_PastResetsAt(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour).Unix()
	w := &rateLimitWindow{UsedPercent: 50, ResetsAt: &past}
	got := winToEntry(w)
	resetsIn, ok := got["resetsIn"].(string)
	if !ok {
		t.Fatal("resetsIn should be a string")
	}
	if resetsIn != "0m" {
		t.Errorf("past resetsAt should be 0m, got %q", resetsIn)
	}
	if _, ok := got["resetsAt"]; ok {
		t.Error("resetsAt key should be absent for a past (already reset) window")
	}
}

func TestWinToEntry_ResetsAtPreserved(t *testing.T) {
	future := time.Now().Add(3 * time.Hour).Unix()
	w := &rateLimitWindow{UsedPercent: 10, ResetsAt: &future}
	got := winToEntry(w)
	at, ok := got["resetsAt"].(time.Time)
	if !ok {
		t.Fatalf("resetsAt should be time.Time, got %T", got["resetsAt"])
	}
	if at.Unix() != future {
		t.Errorf("resetsAt = %d, want %d (exact epoch preserved)", at.Unix(), future)
	}
}

func TestWinToEntry_NoResetsAtKeyWhenNil(t *testing.T) {
	w := &rateLimitWindow{UsedPercent: 30}
	got := winToEntry(w)
	if _, ok := got["resetsAt"]; ok {
		t.Error("resetsAt key should be absent when ResetsAt is nil")
	}
}

func TestWinToEntry_FullUsed(t *testing.T) {
	w := &rateLimitWindow{UsedPercent: 100}
	got := winToEntry(w)
	if got["left"] != 0 {
		t.Errorf("left = %v, want 0", got["left"])
	}
}

func TestBuildOutput_Basic(t *testing.T) {
	future5h := time.Now().Add(3 * time.Hour).Unix()
	futureWk := time.Now().Add(120 * time.Hour).Unix()
	w5h := 300
	wWeekly := 10080
	planType := "pro"
	balance := "100"

	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary:   &rateLimitWindow{UsedPercent: 20, WindowDurationMins: &w5h, ResetsAt: &future5h},
			Secondary: &rateLimitWindow{UsedPercent: 40, WindowDurationMins: &wWeekly, ResetsAt: &futureWk},
			Credits:   &creditsSnapshot{Balance: &balance, HasCredit: true},
			PlanType:  &planType,
		},
	}

	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The windows list is self-describing and labeled from the ACTUAL duration:
	// primary(300min) → label "5h", secondary(10080min) → label "7d", ordered
	// shortest-first regardless of response position.
	ws := windowsOf(t, out)
	if len(ws) != 2 {
		t.Fatalf("windows len = %d, want 2", len(ws))
	}
	if ws[0]["label"] != "5h" || ws[0]["left"] != 80 || ws[0]["windowMins"] != 300 {
		t.Errorf("windows[0] = %v, want label 5h / left 80 / windowMins 300", ws[0])
	}
	if ws[1]["label"] != "7d" || ws[1]["left"] != 60 || ws[1]["windowMins"] != 10080 {
		t.Errorf("windows[1] = %v, want label 7d / left 60 / windowMins 10080", ws[1])
	}
	// Each entry carries its stable slot key (never shown; the label is the truth).
	if ws[0]["key"] != "5h" || ws[1]["key"] != "weekly" {
		t.Errorf("window keys = %v/%v, want 5h/weekly", ws[0]["key"], ws[1]["key"])
	}
	// No legacy top-level window keys.
	for _, legacy := range []string{"fiveHour", "day", "5h", "weekly"} {
		if _, ok := out[legacy]; ok {
			t.Errorf("legacy top-level key %q must not be emitted", legacy)
		}
	}

	credits, ok := out["credits"].(map[string]any)
	if !ok {
		t.Fatal("missing credits")
	}
	if credits["balance"] != "100" {
		t.Errorf("balance = %v, want 100", credits["balance"])
	}
	if credits["hasCredits"] != true {
		t.Error("hasCredits should be true")
	}

	if out["planType"] != "pro" {
		t.Errorf("planType = %v, want pro", out["planType"])
	}
}

func TestBuildOutput_PrefersLimitId(t *testing.T) {
	w5h := 300
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 99, WindowDurationMins: &w5h},
		},
		RateLimitsByLimitId: map[string]rateLimitSnapshot{
			"codex": {
				Primary: &rateLimitWindow{UsedPercent: 15, WindowDurationMins: &w5h},
			},
		},
	}

	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ws := windowsOf(t, out)
	if len(ws) != 1 || ws[0]["label"] != "5h" || ws[0]["left"] != 85 {
		t.Errorf("should use codex limit, got windows=%v", ws)
	}
}

// TestBuildOutput_WeeklyOnly pins the exact real-world case that motivated the
// redesign: Codex sometimes sends only a weekly window, in primary. It must be
// labeled from its actual duration ("7d") — never "5h" from its position — and
// there must be exactly one entry.
func TestBuildOutput_WeeklyOnly(t *testing.T) {
	wWeekly := 10080
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 4, WindowDurationMins: &wWeekly},
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ws := windowsOf(t, out)
	if len(ws) != 1 {
		t.Fatalf("windows = %v, want exactly the weekly entry", ws)
	}
	if ws[0]["label"] != "7d" || ws[0]["windowMins"] != 10080 || ws[0]["left"] != 96 {
		t.Errorf("windows[0] = %v, want label 7d / windowMins 10080 / left 96", ws[0])
	}
}

// TestBuildOutput_SkipsWindowWithoutDuration: a window with no windowDurationMins
// cannot be labeled truthfully and is omitted rather than mislabeled positionally.
func TestBuildOutput_SkipsWindowWithoutDuration(t *testing.T) {
	w5h := 300
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary:   &rateLimitWindow{UsedPercent: 10, WindowDurationMins: &w5h},
			Secondary: &rateLimitWindow{UsedPercent: 40}, // no duration → skipped
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ws := windowsOf(t, out)
	if len(ws) != 1 || ws[0]["windowMins"] != 300 {
		t.Errorf("only the labelable 300min window should be emitted, got %v", ws)
	}
}

// TestBuildOutput_DurationOrder: windows are ordered by actual duration
// (shortest first) even when the response carries them in reverse positions, so
// the display order is stable across Codex reshuffles.
func TestBuildOutput_DurationOrder(t *testing.T) {
	w5h, wWeekly := 300, 10080
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary:   &rateLimitWindow{UsedPercent: 4, WindowDurationMins: &wWeekly},
			Secondary: &rateLimitWindow{UsedPercent: 20, WindowDurationMins: &w5h},
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ws := windowsOf(t, out)
	if len(ws) != 2 || ws[0]["windowMins"] != 300 || ws[1]["windowMins"] != 10080 {
		t.Errorf("windows order = %v, want [300 10080]", ws)
	}
}

// TestBuildOutput_DistinctDurationsBothKept is a REGRESSION GUARD: the data must
// never lose a real window to a consumer's slot limit. Two different durations
// are two different windows and are BOTH emitted with their own truthful labels,
// even though they share a slot key — resolving that collision is the job of a
// consumer that has finite slots (quota-bar), not of this producer.
func TestBuildOutput_DistinctDurationsBothKept(t *testing.T) {
	w240, w600 := 240, 600 // both ≤12h → same "5h" slot key, but different windows
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary:   &rateLimitWindow{UsedPercent: 0, WindowDurationMins: &w240},
			Secondary: &rateLimitWindow{UsedPercent: 0, WindowDurationMins: &w600},
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ws := windowsOf(t, out)
	if len(ws) != 2 {
		t.Fatalf("both distinct windows must be emitted, got %v", ws)
	}
	if ws[0]["label"] != "4h" || ws[1]["label"] != "10h" {
		t.Errorf("each window keeps its own truthful label, got %v", ws)
	}
}

// TestBuildOutput_SameDurationDeduped: only the SAME window (identical duration)
// is deduped; the first (primary) wins.
func TestBuildOutput_SameDurationDeduped(t *testing.T) {
	w300a, w300b := 300, 300
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary:   &rateLimitWindow{UsedPercent: 10, WindowDurationMins: &w300a},
			Secondary: &rateLimitWindow{UsedPercent: 90, WindowDurationMins: &w300b},
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ws := windowsOf(t, out)
	if len(ws) != 1 {
		t.Fatalf("windows = %v, want 1 (same duration deduped)", ws)
	}
	if ws[0]["left"] != 90 {
		t.Errorf("first (primary) window must win, got %v", ws[0])
	}
}

// TestBuildOutput_KeysFromVocabulary: every key is a slot address drawn from
// WindowKeys() (keys may repeat — see DistinctDurationsBothKept).
func TestBuildOutput_KeysFromVocabulary(t *testing.T) {
	w5h, wWeekly := 300, 10080
	rr := rateLimitsResponse{RateLimits: rateLimitSnapshot{
		Primary:   &rateLimitWindow{UsedPercent: 20, WindowDurationMins: &w5h},
		Secondary: &rateLimitWindow{UsedPercent: 40, WindowDurationMins: &wWeekly},
	}}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	known := map[string]bool{}
	for _, k := range WindowKeys() {
		known[k] = true
	}
	for _, w := range windowsOf(t, out) {
		if k, _ := w["key"].(string); !known[k] {
			t.Errorf("key %q not in WindowKeys()", k)
		}
	}
}

// TestBuildOutput_NoWindows: when no window is classifiable the windows key is
// omitted entirely (never an empty list), and the output is still valid if
// other fields (credits/planType) are present.
func TestBuildOutput_NoWindows(t *testing.T) {
	planType := "pro"
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Secondary: &rateLimitWindow{UsedPercent: 40}, // no duration → unclassifiable
			PlanType:  &planType,
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := out["windows"]; ok {
		t.Errorf("windows key must be omitted when empty, got %v", out["windows"])
	}
	if out["planType"] != "pro" {
		t.Errorf("planType should survive without windows: %v", out["planType"])
	}
}

// windowsOf extracts the windows list from a buildOutput result.
func windowsOf(t *testing.T, out map[string]any) []map[string]any {
	t.Helper()
	ws, ok := out["windows"].([]map[string]any)
	if !ok {
		t.Fatalf("missing windows list, out keys = %v", keysOf(out))
	}
	return ws
}

// TestWindowLabel pins that the label is the ACTUAL duration, never a bucket:
// a 600-minute window is "10h", not "5h". This is the whole point of the fix.
func TestWindowLabel(t *testing.T) {
	cases := map[int]string{
		300:   "5h",     // 5 hours
		240:   "4h",     // 4 hours — must NOT be "5h"
		600:   "10h",    // 10 hours — must NOT be "5h"
		60:    "1h",     // 1 hour
		90:    "1h 30m", // mixed
		45:    "45m",    // minutes only
		1440:  "1d",     // 1 day
		10080: "7d",     // 7 days (weekly)
		43200: "30d",    // 30 days (monthly)
		1500:  "1d 1h",  // day + hour
		0:     "0m",     // degenerate
	}
	for mins, want := range cases {
		if got := windowLabel(mins); got != want {
			t.Errorf("windowLabel(%d) = %q, want %q", mins, got, want)
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestBuildOutput_Empty(t *testing.T) {
	rr := rateLimitsResponse{}
	_, err := buildOutput(rr)
	if err == nil {
		t.Error("expected error for empty response")
	}
}

func TestBuildOutput_NilCreditsBalance(t *testing.T) {
	w5h := 300
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 10, WindowDurationMins: &w5h},
			Credits: &creditsSnapshot{Balance: nil, HasCredit: false},
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	credits := out["credits"].(map[string]any)
	if credits["balance"] != "" {
		t.Errorf("nil balance should be empty string, got %v", credits["balance"])
	}
}

func TestBuildResetCredits_Nil(t *testing.T) {
	if got := buildResetCredits(nil); got != nil {
		t.Errorf("nil snapshot should yield nil, got %v", got)
	}
}

func TestBuildResetCredits_NoneAvailable(t *testing.T) {
	exp := time.Now().Add(24 * time.Hour).Unix()
	rc := &resetCreditsSnapshot{
		AvailableCount: 0,
		Credits: []resetCreditEntry{
			{Title: "Full reset", Status: "used", ExpiresAt: &exp},
			{Title: "Full reset", Status: "expired", ExpiresAt: &exp},
		},
	}
	if got := buildResetCredits(rc); got != nil {
		t.Errorf("no available grants should yield nil, got %v", got)
	}
}

func TestBuildResetCredits_SortedByExpiry(t *testing.T) {
	soon := time.Now().Add(24 * time.Hour).Unix()
	later := time.Now().Add(72 * time.Hour).Unix()
	rc := &resetCreditsSnapshot{
		AvailableCount: 2,
		Credits: []resetCreditEntry{
			// Deliberately out of order: later grant listed first.
			{Title: "Full reset (Weekly + 5 hr)", Status: "available", ExpiresAt: &later},
			{Title: "Full reset (Weekly + 5 hr)", Status: "available", ExpiresAt: &soon},
			// Filtered out — not usable.
			{Title: "used one", Status: "used", ExpiresAt: &soon},
		},
	}
	out := buildResetCredits(rc)
	if out == nil {
		t.Fatal("expected non-nil")
	}
	if out["available"] != 2 {
		t.Errorf("available = %v, want 2", out["available"])
	}
	items, ok := out["items"].([]map[string]any)
	if !ok {
		t.Fatalf("items type = %T, want []map[string]any", out["items"])
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2 (used status filtered out)", len(items))
	}
	// Soonest expiry must come first.
	at0 := items[0]["expiresAt"].(time.Time)
	if at0.Unix() != soon {
		t.Errorf("items[0] expiresAt = %d, want soonest %d", at0.Unix(), soon)
	}
	if _, ok := items[0]["expiresIn"].(string); !ok {
		t.Error("expiresIn should be a string")
	}
	if items[0]["title"] != "Full reset (Weekly + 5 hr)" {
		t.Errorf("title = %v", items[0]["title"])
	}
}

func TestBuildResetCredits_CountIgnoresAvailableCount(t *testing.T) {
	// availableCount from the response is deliberately inconsistent with the
	// number of available-status grants. We must report len(items) (=1), never
	// the divergent availableCount (0), so the count can't contradict the list.
	exp := time.Now().Add(24 * time.Hour).Unix()
	rc := &resetCreditsSnapshot{
		AvailableCount: 0,
		Credits: []resetCreditEntry{
			{Title: "Full reset", Status: "available", ExpiresAt: &exp},
		},
	}
	out := buildResetCredits(rc)
	if out == nil {
		t.Fatal("expected non-nil (one usable grant)")
	}
	if out["available"] != 1 {
		t.Errorf("available = %v, want 1 (len items), not response availableCount 0", out["available"])
	}
	if items := out["items"].([]map[string]any); len(items) != 1 {
		t.Errorf("items len = %d, want 1", len(items))
	}
}

func TestBuildOutput_WithResetCredits(t *testing.T) {
	exp := time.Now().Add(24 * time.Hour).Unix()
	w5h := 300
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 10, WindowDurationMins: &w5h},
		},
		ResetCredits: &resetCreditsSnapshot{
			AvailableCount: 1,
			Credits:        []resetCreditEntry{{Title: "Full reset", Status: "available", ExpiresAt: &exp}},
		},
	}
	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rc, ok := out["resetCredits"].(map[string]any)
	if !ok {
		t.Fatal("missing resetCredits")
	}
	if rc["available"] != 1 {
		t.Errorf("available = %v, want 1", rc["available"])
	}
}

func TestResetCreditsParsing(t *testing.T) {
	raw := `{
		"rateLimits": {"primary": {"usedPercent": 20}},
		"rateLimitResetCredits": {
			"availableCount": 2,
			"credits": [
				{"id": "x", "resetType": "codexRateLimits", "status": "available", "grantedAt": 1781228522, "expiresAt": 1783820522, "title": "Full reset (Weekly + 5 hr)"},
				{"id": "y", "status": "available", "expiresAt": 1784334796, "title": "Full reset (Weekly + 5 hr)"}
			]
		}
	}`
	var rr rateLimitsResponse
	if err := json.Unmarshal([]byte(raw), &rr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rr.ResetCredits == nil {
		t.Fatal("ResetCredits should not be nil")
	}
	if rr.ResetCredits.AvailableCount != 2 {
		t.Errorf("availableCount = %d, want 2", rr.ResetCredits.AvailableCount)
	}
	if len(rr.ResetCredits.Credits) != 2 {
		t.Fatalf("credits len = %d, want 2", len(rr.ResetCredits.Credits))
	}
	c0 := rr.ResetCredits.Credits[0]
	if c0.Status != "available" || c0.ExpiresAt == nil || *c0.ExpiresAt != 1783820522 {
		t.Errorf("credit[0] parsed wrong: %+v", c0)
	}
}

func TestRateLimitsResponseParsing(t *testing.T) {
	raw := `{
		"rateLimits": {
			"primary": {"usedPercent": 20, "windowDurationMins": 300, "resetsAt": 1709139600},
			"secondary": {"usedPercent": 40, "windowDurationMins": 1440, "resetsAt": 1709226000},
			"credits": {"balance": "100", "hasCredits": true, "unlimited": false},
			"planType": "pro"
		}
	}`
	var rr rateLimitsResponse
	if err := json.Unmarshal([]byte(raw), &rr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rr.RateLimits.Primary == nil {
		t.Fatal("primary should not be nil")
	}
	if rr.RateLimits.Primary.UsedPercent != 20 {
		t.Errorf("primary used = %d, want 20", rr.RateLimits.Primary.UsedPercent)
	}
	if rr.RateLimits.Secondary.UsedPercent != 40 {
		t.Errorf("secondary used = %d, want 40", rr.RateLimits.Secondary.UsedPercent)
	}
	if *rr.RateLimits.Credits.Balance != "100" {
		t.Errorf("balance = %v", *rr.RateLimits.Credits.Balance)
	}
	if *rr.RateLimits.PlanType != "pro" {
		t.Errorf("planType = %v", *rr.RateLimits.PlanType)
	}
}
