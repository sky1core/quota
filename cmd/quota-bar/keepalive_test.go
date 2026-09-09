package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/keepalive"
)

func TestKeepaliveDisableSurvivesFailedSave(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(settingsPath(), 0700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 29, 59, 0, time.UTC)
	c := keepalive.DefaultConfig()
	c.Enabled = true
	s := keepalive.NewService(nil, filepath.Join(t.TempDir(), "state.json"), func() float64 { t.Fatal("disabled service ran"); return 0 })
	if err := s.Configure(c, now); err != nil {
		t.Fatal(err)
	}
	current := settings{Keepalive: &c}
	next := c
	next.Enabled = false
	got, err := changeKeepalive(s, current, next, now)
	if err == nil || got.Keepalive.Enabled {
		t.Fatalf("failed save did not leave runtime disabled: %+v %v", got, err)
	}
	if result := s.Tick(context.Background(), now.Add(time.Second)); result.Status != "" || result.Error != nil {
		t.Fatalf("disabled service ran after save failure: %+v", result)
	}
}

func TestKeepaliveSettingsPersistWithoutLosingPreferences(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := settingsPath()
	os.MkdirAll(filepath.Dir(p), 0700)
	original := `{"selected":["codex_weekly"],"showResetTime":true,"refreshIdleMinutes":17,"futurePreference":"preserve","keepalive":{"enabled":false,"time":"13:15","weekdays":["Sat"],"idleMinutes":8,"activityMinutes":40,"message":"Reply OK"}}`
	if err := os.WriteFile(p, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	s := loadSettings()
	if s.Keepalive == nil {
		t.Fatal("keepalive not loaded")
	}
	s.Keepalive.Enabled = true
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	got := loadSettings()
	if !got.Keepalive.Enabled || got.Keepalive.Time != "13:15" || got.Keepalive.IdleMinutes != 8 || got.Keepalive.Message != "Reply OK" || !got.ShowResetTime || got.RefreshIdleMinutes != 17 || got.Selected[0] != "codex_weekly" {
		t.Fatalf("settings lost: %+v", got)
	}
	var raw map[string]any
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &raw)
	if raw["futurePreference"] != "preserve" {
		t.Fatal("unknown setting lost")
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm() != 0600 {
		t.Fatal("mode changed")
	}
}

func TestKeepaliveMissingIsOff(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if s := loadSettings(); s.Keepalive != nil {
		t.Fatal("new installation enabled keepalive")
	}
	if keepalive.DefaultConfig().Enabled {
		t.Fatal("default enabled")
	}
}

func TestKeepaliveDisplaySeparatesRepliesAndCache(t *testing.T) {
	r := keepalive.Result{Status: "Checked", Selected: 4, Confirmed: 3, Unconfirmed: 1, CacheHits: 1, CacheMisses: 1, CacheUnknown: 1, CacheDetails: []string{"example: 90/100 input tokens cached", "other: cache usage unavailable"}}
	title, detail := keepaliveResultText(r)
	if !strings.Contains(title, "3/4 replies") || !strings.Contains(title, "1 reused, 1 miss, 1 unknown") || !strings.Contains(detail, "90/100") || !strings.Contains(detail, "unavailable") {
		t.Fatalf("ambiguous cache result: %s / %s", title, detail)
	}
	r.Error = errors.New("example failure")
	title, _ = keepaliveResultText(r)
	if !strings.Contains(title, "see log") {
		t.Fatal("failure hidden")
	}
}
