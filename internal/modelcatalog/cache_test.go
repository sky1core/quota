package modelcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestModelCacheSeparatesAccountEnvironment(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			envKey := "CLAUDE_CONFIG_DIR"
			if provider == "codex" {
				envKey = "CODEX_HOME"
			}
			target := Target{Provider: provider, Binary: "/bin/provider", ConfigDir: "/accounts/default", Env: []string{"PATH=/bin"}}
			dir := t.TempDir()
			paths := map[string]bool{}
			for i, env := range [][]string{target.Env, {envKey + "="}, {envKey + "=" + target.ConfigDir}} {
				selected := target
				selected.Env = env
				path := filepath.Join(dir, cacheKey(selected)+".json")
				if paths[path] {
					t.Fatalf("account environment %d reuses another environment's cache", i)
				}
				paths[path] = true
				if err := writeSnapshot(path, Snapshot{ConfigDir: target.ConfigDir, Models: []Model{{ID: "example"}}}); err != nil {
					t.Fatal(err)
				}
			}
			for path := range paths {
				if _, err := readSnapshot(path); err != nil {
					t.Fatal(err)
				}
			}
			unrelated := target
			unrelated.Env = []string{"PATH=/other/bin", "UNRELATED=changed"}
			if cacheKey(unrelated) != cacheKey(target) {
				t.Fatal("unrelated environment changed account identity")
			}
			inherited := target
			inherited.Env = []string{envKey + "=" + target.ConfigDir}
			repeated := inherited
			repeated.Env = append([]string{envKey + "=/old/account"}, inherited.Env...)
			if cacheKey(inherited) != cacheKey(repeated) {
				t.Fatal("cache does not use the last account environment entry like os/exec")
			}
			legacyIdentity, err := json.Marshal([]string{target.Provider, target.Binary, target.ConfigDir})
			if err != nil {
				t.Fatal(err)
			}
			legacySum := sha256.Sum256(legacyIdentity)
			legacyKey := hex.EncodeToString(legacySum[:])
			for _, selected := range []Target{target, inherited} {
				if cacheKey(selected) == legacyKey {
					t.Fatal("cache reuses a legacy catalog without account environment identity")
				}
			}
		})
	}
}

func TestSnapshotFreshness(t *testing.T) {
	now := time.Now()
	target := Target{Provider: "claude", Binary: "/bin/provider", ConfigDir: "/accounts/one"}
	base := Snapshot{SchemaVersion: 1, Provider: target.Provider, Binary: target.Binary, ConfigDir: target.ConfigDir,
		CLIVersion: "1.0", FetchedAt: now, Models: []Model{{ID: "example"}}}
	for _, tc := range []struct {
		name   string
		change func(*Snapshot)
		want   bool
	}{
		{"new", func(s *Snapshot) {}, true},
		{"before expiry", func(s *Snapshot) { s.FetchedAt = now.Add(-3*time.Hour + time.Nanosecond) }, true},
		{"at expiry", func(s *Snapshot) { s.FetchedAt = now.Add(-3 * time.Hour) }, false},
		{"future", func(s *Snapshot) { s.FetchedAt = now.Add(time.Second) }, false},
		{"missing time", func(s *Snapshot) { s.FetchedAt = time.Time{} }, false},
		{"version change", func(s *Snapshot) { s.CLIVersion = "2.0" }, false},
		{"account change", func(s *Snapshot) { s.ConfigDir = "/accounts/two" }, false},
		{"binary change", func(s *Snapshot) { s.Binary = "/other/provider" }, false},
		{"provider change", func(s *Snapshot) { s.Provider = "codex" }, false},
		{"schema change", func(s *Snapshot) { s.SchemaVersion = 2 }, false},
		{"empty models", func(s *Snapshot) { s.Models = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := base
			tc.change(&s)
			if got := s.fresh(target, "1.0", now); got != tc.want {
				t.Fatalf("fresh = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestSnapshotAtomicReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	first := Snapshot{FetchedAt: time.Now().UTC(), Models: []Model{{ID: "example-old"}}}
	second := first
	second.Models = []Model{{ID: "example-new", SupportedEfforts: []string{"low", "high"}}, {ID: "example-empty", SupportedEfforts: []string{}}}
	for _, want := range []Snapshot{first, second} {
		if err := writeSnapshot(path, want); err != nil {
			t.Fatal(err)
		}
		got, err := readSnapshot(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("snapshot mismatch: %#v", got)
		}
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if stat.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o", stat.Mode().Perm())
		}
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("temporary files remain: %v", files)
	}
}

func TestCacheLockCancellationAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.lock")
	unlock, err := lockCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = lockCache(ctx, path)
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting lock = %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	unlock2, err := lockCache(ctx2, path)
	if err != nil {
		t.Fatal(err)
	}
	unlock2()
}

func TestUnavailableCLIDoesNotReuseOrReplaceCache(t *testing.T) {
	dir := t.TempDir()
	target := Target{Provider: "codex", Binary: filepath.Join(dir, "missing-cli"), ConfigDir: dir, Env: []string{}}
	path := filepath.Join(dir, cacheKey(target)+".json")
	snapshot := Snapshot{SchemaVersion: 1, Provider: target.Provider, Binary: target.Binary, ConfigDir: target.ConfigDir,
		CLIVersion: "1.0", FetchedAt: time.Now().UTC(), Models: []Model{{ID: "example"}}}
	if err := writeSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, force := range []bool{false, true} {
		if _, err := (Cache{Dir: dir}).Get(context.Background(), target, force); err == nil {
			t.Fatal("used cache without verifying CLI version")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatal("failed query replaced existing cache")
		}
	}
}

func TestMalformedModelCacheEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	for _, data := range []string{
		`{"models":[{}]}`, `{"models":[null]}`, `{"models":[]}`, `{"models":null}`,
		`{"models":[{"id":" "}]}`, `{"models":[{"id":"example","supportedEfforts":[null]}]}`,
		`{"models":[{"id":"example","supportedEfforts":[" "]}]}`,
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSnapshot(path); err == nil {
			t.Fatalf("accepted %s", data)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(after) != data {
			t.Fatal("invalid cache was modified")
		}
	}
}
