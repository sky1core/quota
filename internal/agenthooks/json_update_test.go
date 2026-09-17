package agenthooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestJSONUpdatePreservesNumbersAndNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte("{\n  \"large\": 9007199254740993, \"max\": 9223372036854775807, \"fraction\": 0.123456789123456789\n}\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	root, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	content, _ := os.ReadFile(path)
	backups, _ := filepath.Glob(path + ".bak.*")
	if !bytes.Equal(content, original) || !before.ModTime().Equal(after.ModTime()) || len(backups) != 0 {
		t.Fatal("no-op changed file or backups")
	}
	if root["large"] != json.Number("9007199254740993") {
		t.Fatalf("lost precision: %v", root)
	}
	root, err = UpdateJSONObjectWithBackup(path, func(root map[string]any) error { root["new"] = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]json.Number{"large": "9007199254740993", "max": "9223372036854775807", "fraction": "0.123456789123456789"} {
		if root[key] != want {
			t.Fatalf("%s: %v", key, root[key])
		}
	}
	backups, _ = filepath.Glob(path + ".bak.*")
	if len(backups) != 1 {
		t.Fatalf("backups=%v", backups)
	}
	backup, _ := os.ReadFile(backups[0])
	if !bytes.Equal(backup, original) {
		t.Fatal("backup differs from original")
	}
}

func TestJSONUpdatePreservesFileMode(t *testing.T) {
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"keep":true}`), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error { root["new"] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode changed: %v", info.Mode().Perm())
	}

	fresh := filepath.Join(t.TempDir(), "settings.json")
	if _, err := UpdateJSONObjectWithBackup(fresh, func(root map[string]any) error { root["new"] = true; return nil }); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("new file mode under umask 077: %v", info.Mode().Perm())
	}
}

func TestExclusiveTempNeverWiderThanRequestedMode(t *testing.T) {
	previous := syscall.Umask(0o022)
	defer syscall.Umask(previous)
	dir := t.TempDir()
	for _, perm := range []os.FileMode{0o600, 0o640, 0o644} {
		f, err := createExclusiveTemp(dir, ".settings.json.tmp-", perm)
		if err != nil {
			t.Fatal(err)
		}
		info, err := f.Stat()
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got&^perm != 0 {
			t.Fatalf("temp for %v created wider: %v", perm, got)
		}
	}
}

func TestJSONUpdateFailureLeavesOriginalUntouched(t *testing.T) {
	for _, original := range []string{`{"keep":true}`, `{"broken":`, `[]`, `null`, `{} {}`} {
		t.Run(original, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error { root["changed"] = true; return errors.New("update refused") })
			if err == nil {
				t.Fatal("expected failure")
			}
			actual, _ := os.ReadFile(path)
			backups, _ := filepath.Glob(path + ".bak.*")
			if string(actual) != original || len(backups) != 0 {
				t.Fatal("failed update modified configuration")
			}
		})
	}
}

func TestJSONUpdateFailsWhenTargetChangesDuringUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"userSetting":"before"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error {
		if err := os.WriteFile(path, []byte("{\"userSetting\":\"new-user-value\"}\n"), 0o600); err != nil {
			return err
		}
		root["quotaSetting"] = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "changed during update") {
		t.Fatalf("err = %v, want concurrent change failure", err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != "{\"userSetting\":\"new-user-value\"}\n" {
		t.Fatalf("concurrent edit was overwritten: %q", actual)
	}
	backups, _ := filepath.Glob(path + ".bak.*")
	if len(backups) != 0 {
		t.Fatalf("backup created despite failed update: %v", backups)
	}
}

func TestJSONUpdateFailsWhenTargetAppearsDuringUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	_, err := UpdateJSONObjectWithBackup(path, func(root map[string]any) error {
		if err := os.WriteFile(path, []byte("{\"userSetting\":\"new-user-value\"}\n"), 0o600); err != nil {
			return err
		}
		root["quotaSetting"] = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "appeared during update") {
		t.Fatalf("err = %v, want concurrent create failure", err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != "{\"userSetting\":\"new-user-value\"}\n" {
		t.Fatalf("concurrent create was overwritten: %q", actual)
	}
}
