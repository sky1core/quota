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
