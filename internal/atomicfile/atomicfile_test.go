package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLockedKeepsTheLockAtTheGivenPath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "tree", "AGENTS.md")
	lock := filepath.Join(dir, "locks", "target.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"first\n", "second\n"} {
		if err := SaveLocked(target, []byte(body), 0o644, lock); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != body {
			t.Fatalf("target not written: %q %v", got, err)
		}
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("lock missing at the given path: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "AGENTS.md" {
		t.Fatalf("target directory has extra entries: %v", entries)
	}
	if err := SaveLocked(target, []byte("x"), 0o644, ""); err == nil {
		t.Fatal("empty lock path accepted")
	}
}
