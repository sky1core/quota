package keepalive

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConfigDefaultsAndValidation(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"enabled":true}`), &c); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.Time != "12:30" || c.IdleMinutes != 5 || c.ActivityMinutes != 50 || len(c.Weekdays) != 5 {
		t.Fatalf("defaults: %+v", c)
	}
	for _, raw := range []string{`{"weekdays":[]}`, `{"weekdays":["Sun|Mon"]}`, `{"weekdays":["Mon","Mon"]}`, `{"time":"1:30"}`, `{"time":"24:00"}`, `{"idleMinutes":0}`, `{"activityMinutes":-1}`, `{"message":" "}`} {
		var c Config
		if err := json.Unmarshal([]byte(raw), &c); err == nil && c.Validate() == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestScheduleCrossingAndMissedRuns(t *testing.T) {
	c := DefaultConfig()
	c.Enabled = true
	parse := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name, previous, now string
		want                bool
	}{
		{"weekday", "2026-09-09T12:29:55+09:00", "2026-09-09T12:30:05+09:00", true},
		{"weekend", "2026-09-12T12:29:55+09:00", "2026-09-12T12:30:05+09:00", false},
		{"already passed", "2026-09-09T12:30:05+09:00", "2026-09-09T12:30:15+09:00", false},
		{"sleep missed", "2026-09-09T12:29:55+09:00", "2026-09-09T12:35:00+09:00", false},
		{"clock backwards", "2026-09-09T12:30:05+09:00", "2026-09-09T12:29:55+09:00", false},
		{"before schedule", "2026-09-09T12:29:35+09:00", "2026-09-09T12:29:45+09:00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := due(c, parse(tc.previous), parse(tc.now)); got != tc.want {
				t.Fatalf("due=%v", got)
			}
		})
	}
	now := parse("2026-09-09T12:30:00+09:00")
	if due(c, time.Time{}, now) {
		t.Fatal("startup ran a missed schedule")
	}
	c.Enabled = false
	if due(c, now.Add(-time.Second), now) {
		t.Fatal("disabled schedule ran")
	}
}

func TestDisabledServiceDoesNotInspectOrCreateState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewService(nil, path, func() float64 { t.Fatal("disabled service inspected PC"); return 0 })
	now := time.Date(2026, 9, 9, 12, 30, 0, 0, time.UTC)
	if err := s.Configure(DefaultConfig(), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := s.Tick(context.Background(), now); got.Status != "" || got.Error != nil {
		t.Fatalf("disabled result: %+v", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled service wrote state: %v", err)
	}
}

func TestLedgerClaimsOnceAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 9, 12, 30, 0, 0, time.UTC)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := claimDay(path, now); err == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("successful claims: %d", wins.Load())
	}
	if _, err := claimDay(path, now); !errors.Is(err, alreadyAttempted) {
		t.Fatalf("repeat: %v", err)
	}
	if err := updateLedger(path, func(l *ledger) error {
		l.Ignored["test-session"] = ignoredActivity{ID: "automation-id", At: now}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ids, err := claimDay(path, now.Add(24*time.Hour))
	if err != nil || ids["test-session"] != "automation-id" {
		t.Fatalf("ignored activity lost: %v %v", ids, err)
	}
	ids, err = claimDay(path, now.Add(72*time.Hour))
	if err != nil || len(ids) != 0 {
		t.Fatalf("stale activity not pruned: %v %v", ids, err)
	}
}

func TestInvalidLedgerIsPreserved(t *testing.T) {
	for _, raw := range []string{"{", "null", "{}", `{"day":"invalid"}`} {
		p := filepath.Join(t.TempDir(), "state.json")
		os.WriteFile(p, []byte(raw), 0600)
		if _, err := claimDay(p, time.Now()); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		b, _ := os.ReadFile(p)
		if string(b) != raw {
			t.Fatal("damaged state overwritten")
		}
	}
}

func TestPreparedActivitySurvivesWithoutDeliveryResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	now := time.Date(2026, 9, 9, 12, 30, 0, 0, time.UTC)
	if _, err := claimDay(path, now); err != nil {
		t.Fatal(err)
	}
	id, err := prepareActivity(path, "test-session", now)
	if err != nil || id == "" {
		t.Fatalf("prepare: %q %v", id, err)
	}
	ignored, err := claimDay(path, now.Add(24*time.Hour))
	if err != nil || ignored["test-session"] != id {
		t.Fatalf("prepared dispatch lost across restart: %v %v", ignored, err)
	}
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if id, err := prepareActivity(path, "test-session", now); err == nil || id != "" {
		t.Fatal("provided dispatch ID without recording it")
	}
}

func TestActiveOrUnknownPCSkipsAndConsumesSchedule(t *testing.T) {
	for _, idle := range []float64{0, 299, -1, math.NaN(), math.Inf(1)} {
		s := NewService(nil, filepath.Join(t.TempDir(), "state.json"), func() float64 { return idle })
		c := DefaultConfig()
		c.Enabled = true
		now := time.Date(2026, 9, 9, 12, 30, 0, 0, time.UTC)
		if err := s.Configure(c, now.Add(-10*time.Second)); err != nil {
			t.Fatal(err)
		}
		r := s.Tick(context.Background(), now)
		if r.Status != "Skipped: PC active or idle time unavailable" {
			t.Fatalf("%v: %+v", idle, r)
		}
		if _, err := claimDay(s.statePath, now); !errors.Is(err, alreadyAttempted) {
			t.Fatalf("active skip can retry: %v", err)
		}
	}
}

func TestReceiptUsageSeparatesCompletionFromCache(t *testing.T) {
	var result Result
	c := Candidate{Account: "example"}
	for _, receipt := range []Receipt{
		{Cache: &CacheUsage{InputTokens: 100, CachedTokens: 90}},
		{Confirmed: true},
		{Confirmed: true, Cache: &CacheUsage{InputTokens: 100, CachedTokens: 90}},
		{Confirmed: true, Cache: &CacheUsage{InputTokens: 100, CachedTokens: 0}},
		{Confirmed: true, Cache: &CacheUsage{InputTokens: 0, CachedTokens: 0}},
		{Confirmed: true, Cache: &CacheUsage{InputTokens: 10, CachedTokens: 11}},
	} {
		result.addReceipt(c, receipt)
	}
	if result.Confirmed != 5 || result.Unconfirmed != 1 || result.CacheHits != 1 || result.CacheMisses != 1 || result.CacheUnknown != 3 || len(result.CacheDetails) != 5 {
		t.Fatalf("invalid cache classification: %+v", result)
	}
}

func TestCodexUsageMissingPriorBaselineIsUnknown(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	records := codexTestRecords(at)
	next := codexTestRecords(at)[1:]
	for _, row := range next {
		p := row["payload"].(map[string]any)
		if _, ok := p["turn_id"]; ok {
			p["turn_id"] = codexTestClient
		}
		if p["type"] == "token_count" {
			p["info"] = map[string]any{"total_token_usage": map[string]int{"input_tokens": 100, "cached_input_tokens": 90}}
		}
	}
	records = append(records, next...)
	s, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || !s.idle() || s.cacheUsage() != nil {
		t.Fatalf("unknown prior usage was attributed to current input: %+v %v", s.cacheUsage(), err)
	}
}

func TestCacheUsageMalformedDuplicateDoesNotReuseValue(t *testing.T) {
	for _, raw := range []string{`{"n":80,"n":null}`, `{"n":80,"n":-1}`, `{"n":80,"n":9223372036854775808}`, `{"n":null,"n":80}`} {
		var v struct {
			N cacheToken `json:"n"`
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil || v.N.valid {
			t.Fatalf("malformed duplicate accepted: %s %+v %v", raw, v, err)
		}
	}
}

func TestCodexPartialUsageSnapshotIsUnknown(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	records := codexTestRecords(at)
	codexTestPayload(records, 6)["info"] = map[string]any{"total_token_usage": map[string]int{"input_tokens": 100, "cached_input_tokens": 90}}
	missing := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": nil}}}
	records = append(records[:7], missing, records[7])
	s, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || !s.idle() || s.cacheUsage() != nil {
		t.Fatalf("partial snapshot counted as full usage: %+v %v", s.cacheUsage(), err)
	}
}
