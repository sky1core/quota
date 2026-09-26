package quotacache

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// isolate points HOME at a temp dir so path() (and the cache file) stay off the
// real ~/.config/quota, matching how the rest of the repo isolates config tests.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path()), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPutThenGetHits(t *testing.T) {
	isolate(t)
	Put("claude:/x/.claude", "report-text", time.Time{})
	got, ok := Get("claude:/x/.claude", time.Minute)
	if !ok || got != "report-text" {
		t.Fatalf("want hit report-text, got %q ok=%v", got, ok)
	}
}

func TestGetMissesUnknownKey(t *testing.T) {
	isolate(t)
	if _, ok := Get("nope", time.Minute); ok {
		t.Fatal("unknown key should miss")
	}
}

func TestGetMissesWhenMaxAgeNonPositive(t *testing.T) {
	isolate(t)
	Put("k", "v", time.Time{})
	if _, ok := Get("k", 0); ok {
		t.Fatal("maxAge 0 must disable the cache read")
	}
	if _, ok := Get("k", -time.Second); ok {
		t.Fatal("negative maxAge must disable the cache read")
	}
}

func TestGetMissesWhenStale(t *testing.T) {
	isolate(t)
	// Write an entry aged past any small maxAge without waiting.
	save(map[string]entry{"k": {FetchedAt: time.Now().Add(-10 * time.Minute), Raw: "old"}})
	if _, ok := Get("k", time.Minute); ok {
		t.Fatal("entry older than maxAge must miss")
	}
	if got, ok := Get("k", time.Hour); !ok || got != "old" {
		t.Fatalf("entry within maxAge must hit: got %q ok=%v", got, ok)
	}
}

func TestGetMissesFutureTimestamp(t *testing.T) {
	isolate(t)
	save(map[string]entry{"k": {FetchedAt: time.Now().Add(time.Minute), Raw: "future"}})
	if _, ok := Get("k", time.Hour); ok {
		t.Fatal("future fetchedAt must miss")
	}
	PutWithContext(context.Background(), "k", "refreshed", time.Now(), time.Time{})
	if got, ok := Get("k", time.Minute); !ok || got != "refreshed" {
		t.Fatalf("invalid future entry prevented refresh: %q, hit=%v", got, ok)
	}
}

// TestGetMissesAfterValidUntil pins the reset-boundary rule: past a stored
// entry's validUntil the raw is pre-reset and must not be served, even while
// fresh within maxAge; a future validUntil (or a zero one) does not block.
func TestGetMissesAfterValidUntil(t *testing.T) {
	isolate(t)
	Put("past", "v", time.Now().Add(-time.Second))
	if _, ok := Get("past", time.Hour); ok {
		t.Fatal("entry past its validUntil must miss despite being fresh")
	}
	Put("future", "v2", time.Now().Add(time.Hour))
	if got, ok := Get("future", time.Hour); !ok || got != "v2" {
		t.Fatalf("entry before its validUntil must hit: got %q ok=%v", got, ok)
	}
	Put("zero", "v3", time.Time{})
	if _, ok := Get("zero", time.Hour); !ok {
		t.Fatal("zero validUntil must not invalidate")
	}
}

// TestPutMergesAcrossKeys pins the property that a caller refreshing one account
// never drops another account's entry.
func TestPutMergesAcrossKeys(t *testing.T) {
	isolate(t)
	Put("claude:/a", "aa", time.Time{})
	Put("codex:/b", "bb", time.Time{})
	if got, ok := Get("claude:/a", time.Minute); !ok || got != "aa" {
		t.Fatalf("first entry lost after second Put: got %q ok=%v", got, ok)
	}
	if got, ok := Get("codex:/b", time.Minute); !ok || got != "bb" {
		t.Fatalf("second entry missing: got %q ok=%v", got, ok)
	}
}

// TestPutOverwritesSameKey confirms a re-probe replaces the prior value.
func TestPutOverwritesSameKey(t *testing.T) {
	isolate(t)
	Put("k", "old", time.Time{})
	Put("k", "new", time.Time{})
	if got, _ := Get("k", time.Minute); got != "new" {
		t.Fatalf("want new, got %q", got)
	}
}

func TestDelayedObservationCannotReplaceNewerResponse(t *testing.T) {
	isolate(t)
	older := time.Now().Add(-time.Minute)
	newer := older.Add(time.Second)
	PutWithContext(context.Background(), "account", "10", newer, time.Time{})
	PutWithContext(context.Background(), "other", "55", newer, time.Time{})
	PutWithContext(context.Background(), "account", "80", older, time.Time{})
	if got, ok := Get("account", 75*time.Second); !ok || got != "10" {
		t.Fatalf("delayed response replaced newer quota: %q, hit=%v", got, ok)
	}
	if got := load()["account"].FetchedAt; !got.Equal(newer) {
		t.Fatalf("observation was redated: got %v, want %v", got, newer)
	}
	if got, ok := Get("other", time.Minute); !ok || got != "55" {
		t.Fatalf("other account changed: %q, hit=%v", got, ok)
	}
}

func TestPublicationDoesNotRenewObservationAge(t *testing.T) {
	isolate(t)
	observed := time.Now().Add(-2 * time.Minute)
	PutWithContext(context.Background(), "account", "80", observed, time.Time{})
	if _, ok := Get("account", 75*time.Second); ok {
		t.Fatal("an old observation became fresh when published")
	}
}

func TestConcurrentPutsKeepAll(t *testing.T) {
	isolate(t)
	const n = 20
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Put(fmt.Sprintf("k-%d", i), fmt.Sprintf("v-%d", i), time.Time{})
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if got, ok := Get(fmt.Sprintf("k-%d", i), time.Minute); !ok || got != fmt.Sprintf("v-%d", i) {
			t.Fatalf("key k-%d lost to a concurrent write: got %q ok=%v", i, got, ok)
		}
	}
}

func TestPutWithContextStopsWaitingForBusyLock(t *testing.T) {
	isolate(t)
	lockFile, err := os.OpenFile(path()+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lockFile.Close()
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	tests := []struct {
		name string
		ctx  context.Context
		max  time.Duration
	}{
		{name: "caller-context", ctx: ctx, max: 200 * time.Millisecond},
		{name: "cache-budget", ctx: context.Background(), max: 300 * time.Millisecond},
	}
	for _, tt := range tests {
		start := time.Now()
		PutWithContext(tt.ctx, tt.name, "value", time.Now(), time.Time{})
		if elapsed := time.Since(start); elapsed > tt.max {
			t.Fatalf("%s write waited too long: %s", tt.name, elapsed)
		}
		if got, ok := Get(tt.name, time.Minute); ok || got != "" {
			t.Fatalf("%s write should be skipped, got %q ok=%v", tt.name, got, ok)
		}
	}
}

func TestValidityBoundaryAndOriginalObservationTime(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	fetched := now.Add(-30 * time.Second)
	boundary := now.Add(10 * time.Second)
	PutWithContext(context.Background(), "example", "raw", fetched, boundary)
	raw, validity, ok := GetWithValidity("example", time.Minute)
	if !ok || raw != "raw" || !validity.FetchedAt.Equal(fetched) || !validity.ValidUntil.Equal(boundary) {
		t.Fatalf("observation changed on read: %q %+v %v", raw, validity, ok)
	}
	for _, tc := range []struct {
		name string
		v    Validity
		at   time.Time
		want bool
	}{
		{"before reset", validity, boundary.Add(-time.Nanosecond), true},
		{"at reset", validity, boundary, false},
		{"after reset", validity, boundary.Add(time.Nanosecond), false},
		{"freshness boundary", Validity{FetchedAt: fetched}, fetched.Add(time.Minute), true},
		{"past freshness", Validity{FetchedAt: fetched}, fetched.Add(time.Minute + time.Nanosecond), false},
		{"future observation", Validity{FetchedAt: now.Add(time.Second)}, now, false},
		{"missing observation", Validity{}, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.ValidAt(tc.at, time.Minute); got != tc.want {
				t.Fatalf("valid=%v, want %v", got, tc.want)
			}
		})
	}
}
