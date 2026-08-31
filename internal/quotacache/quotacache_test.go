package quotacache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

func TestDeleteRemovesOnlyRequestedKey(t *testing.T) {
	isolate(t)
	Put("claude:/a", "aa", time.Time{})
	Put("codex:/b", "bb", time.Time{})

	Delete("claude:/a")

	if _, ok := Get("claude:/a", time.Minute); ok {
		t.Fatal("deleted key should miss")
	}
	if got, ok := Get("codex:/b", time.Minute); !ok || got != "bb" {
		t.Fatalf("unrelated key should remain: got %q ok=%v", got, ok)
	}
}

// TestConcurrentPutsKeepAll is the reason the write path holds a lock: without
// serializing the read-modify-write, two Puts that read the same file and each
// add their key would clobber each other on save (lost update). With the lock,
// every distinct key survives. Run under -race, it also guards the write path
// against data races.
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
