package codex

import (
	"encoding/json"
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
	if got["resetsIn"] != nil {
		t.Errorf("resetsIn should be nil without ResetsAt, got %v", got["resetsIn"])
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
	futureDay := time.Now().Add(12 * time.Hour).Unix()
	planType := "pro"
	balance := "100"

	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary:   &rateLimitWindow{UsedPercent: 20, ResetsAt: &future5h},
			Secondary: &rateLimitWindow{UsedPercent: 40, ResetsAt: &futureDay},
			Credits:   &creditsSnapshot{Balance: &balance, HasCredit: true},
			PlanType:  &planType,
		},
	}

	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fiveHour, ok := out["fiveHour"].(map[string]any)
	if !ok {
		t.Fatal("missing fiveHour")
	}
	if fiveHour["left"] != 80 {
		t.Errorf("fiveHour left = %v, want 80", fiveHour["left"])
	}

	day, ok := out["day"].(map[string]any)
	if !ok {
		t.Fatal("missing day")
	}
	if day["left"] != 60 {
		t.Errorf("day left = %v, want 60", day["left"])
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
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 99},
		},
		RateLimitsByLimitId: map[string]rateLimitSnapshot{
			"codex": {
				Primary:   &rateLimitWindow{UsedPercent: 15},
				Secondary: &rateLimitWindow{UsedPercent: 35},
			},
		},
	}

	out, err := buildOutput(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fiveHour := out["fiveHour"].(map[string]any)
	if fiveHour["left"] != 85 {
		t.Errorf("should use codex limit, got left=%v", fiveHour["left"])
	}
}

func TestBuildOutput_Empty(t *testing.T) {
	rr := rateLimitsResponse{}
	_, err := buildOutput(rr)
	if err == nil {
		t.Error("expected error for empty response")
	}
}

func TestBuildOutput_NilCreditsBalance(t *testing.T) {
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 10},
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
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{
			Primary: &rateLimitWindow{UsedPercent: 10},
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
