// Package quotacache is a small on-disk cache of each account's last successful
// quota probe, shared by quota-cli and quota-bar so each consumer can reuse a
// result within its freshness window.
//
// It stores the provider's PRE-PARSE raw output, not the parsed result: the
// parsed shape carries Go types (time.Time, int, []map[string]any) that a JSON
// round-trip would turn into string/float64/[]any and break every consumer.
// Consumers re-parse the raw on read; when a provider only reports relative
// reset text, maxAge is the only freshness bound.
package quotacache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type entry struct {
	FetchedAt time.Time `json:"fetchedAt"`
	// ValidUntil is the first instant when the stored provider data may change.
	// Zero means only maxAge bounds the entry.
	ValidUntil time.Time `json:"validUntil,omitempty"`
	Raw        string    `json:"raw"`
}

func path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "quota-cache.json")
}

// Get returns the cached raw output for key when a stored entry exists and is no
// older than maxAge. A non-positive maxAge disables the read (always a miss), so
// a caller that must probe live passes 0. Reads take no lock: Put replaces the
// file atomically, so a reader always sees a whole file, old or new, never a
// partial one.
func Get(key string, maxAge time.Duration) (string, bool) {
	if maxAge <= 0 {
		return "", false
	}
	e, ok := load()[key]
	now := time.Now()
	if !ok || e.FetchedAt.After(now) || now.Sub(e.FetchedAt) > maxAge {
		return "", false
	}
	// Expire at the provider's first recorded data-change boundary.
	if !e.ValidUntil.IsZero() && !now.Before(e.ValidUntil) {
		return "", false
	}
	return e.Raw, true
}

// Put stores raw under key with the current time, merging into the existing file
// so a caller that refreshed one account never drops another account's entry.
// validUntil is the probe's first data-change instant (zero when unknown); past
// it the entry expires regardless of maxAge. Only successful probes are stored
// (the caller's contract). Write failures are swallowed: the cache is an
// optimization, never a source of truth, so a lost write just means the next
// reader probes live.
func Put(key, raw string, validUntil time.Time) {
	p := path()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	unlock, err := lock(p + ".lock")
	if err != nil {
		return
	}
	defer unlock()
	m := load()
	m[key] = entry{FetchedAt: time.Now(), ValidUntil: validUntil, Raw: raw}
	save(m)
}

func load() map[string]entry {
	b, err := os.ReadFile(path())
	if err != nil {
		return map[string]entry{}
	}
	var m map[string]entry
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return map[string]entry{}
	}
	return m
}

func save(m map[string]entry) {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return
	}
	p := path()
	tmp := p + ".tmp"
	// 0o600: the raw holds each account's /usage report (skill names, request
	// counts, usage patterns), so keep it readable by this user only.
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

// lock takes an exclusive cross-process lock so concurrent Put calls (e.g.
// quota-bar refreshing while quota-cli runs) serialize their read-modify-write
// instead of clobbering each other. The lock is a sidecar file, held only for
// the brief merge, never across a probe.
func lock(lockPath string) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
