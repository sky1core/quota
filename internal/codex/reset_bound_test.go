package codex

import (
	"testing"
	"time"
)

func TestCacheValidUntilIncludesPastWindowReset(t *testing.T) {
	past := time.Now().Add(-time.Minute).Unix()
	future := time.Now().Add(time.Hour).Unix()

	// Soonest window already reset: the past epoch must be the bound, not zero.
	rr := rateLimitsResponse{RateLimits: rateLimitSnapshot{
		Primary:   &rateLimitWindow{UsedPercent: 40, ResetsAt: &past},
		Secondary: &rateLimitWindow{UsedPercent: 10, ResetsAt: &future},
	}}
	if got, want := cacheValidUntil(rr), time.Unix(past, 0); got.IsZero() || !got.Equal(want) {
		t.Fatalf("past-epoch window must bound the entry: want %v got %v", want, got)
	}

	// No resetsAt anywhere -> zero bound (only maxAge applies).
	rr2 := rateLimitsResponse{RateLimits: rateLimitSnapshot{
		Primary: &rateLimitWindow{UsedPercent: 5},
	}}
	if got := cacheValidUntil(rr2); !got.IsZero() {
		t.Fatalf("no resetsAt must yield zero bound, got %v", got)
	}

	// The per-limit "codex" snapshot is the one bounded (matches buildOutput).
	rr3 := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{Primary: &rateLimitWindow{ResetsAt: &future}},
		RateLimitsByLimitId: map[string]rateLimitSnapshot{
			"codex": {Primary: &rateLimitWindow{ResetsAt: &past}},
		},
	}
	if got, want := cacheValidUntil(rr3), time.Unix(past, 0); !got.Equal(want) {
		t.Fatalf("selectSnapshot must prefer the codex entry: want %v got %v", want, got)
	}
}

func TestCacheValidUntilIncludesAvailableResetCreditExpiry(t *testing.T) {
	windowReset := time.Now().Add(2 * time.Hour).Unix()
	creditExpiry := time.Now().Add(time.Hour).Unix()
	expiredCredit := time.Now().Add(-time.Hour).Unix()
	rr := rateLimitsResponse{
		RateLimits: rateLimitSnapshot{Primary: &rateLimitWindow{ResetsAt: &windowReset}},
		ResetCredits: &resetCreditsSnapshot{Credits: []resetCreditEntry{
			{Status: "available", ExpiresAt: &creditExpiry},
			{Status: "used", ExpiresAt: &expiredCredit},
		}},
	}
	if got, want := cacheValidUntil(rr), time.Unix(creditExpiry, 0); !got.Equal(want) {
		t.Fatalf("available reset-credit expiry must bound cache: want %v got %v", want, got)
	}
}
