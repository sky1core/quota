package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/keepalive"
)

func TestKeepaliveTicks_QuiesceAwaitsCleanup(t *testing.T) {
	kt := newKeepaliveTicks(context.Background())
	ctx, done, ok := kt.begin()
	if !ok {
		t.Fatal("begin refused with no handover in progress")
	}
	cleanupDone := make(chan struct{})
	go func() {
		defer done()
		<-ctx.Done() // cancelled by quiesce
		time.Sleep(20 * time.Millisecond)
		close(cleanupDone)
	}()

	if !kt.quiesce(2 * time.Second) {
		t.Fatal("quiesce timed out while cleanup was quick")
	}
	select {
	case <-cleanupDone:
	default:
		t.Fatal("quiesce released the caller before the tick finished its cleanup")
	}
	if _, _, ok := kt.begin(); ok {
		t.Fatal("a new tick started while the handover holds keepalive blocked")
	}
}

func TestKeepaliveNeedsAwakeOnlyBeforeScheduledAttempt(t *testing.T) {
	cfg := keepalive.DefaultConfig()
	cfg.Enabled = true
	scheduled := time.Date(2026, 9, 23, 12, 30, 0, 0, time.FixedZone("local", 9*3600))
	for _, tc := range []struct {
		name    string
		at      time.Time
		running bool
		want    bool
	}{
		{"before activity window", scheduled.Add(-51 * time.Minute), false, false},
		{"activity window starts", scheduled.Add(-50 * time.Minute), false, true},
		{"before schedule", scheduled.Add(-time.Second), false, true},
		{"scheduled attempt", scheduled, true, true},
		{"long attempt", scheduled.Add(6 * time.Minute), true, true},
		{"attempt completed", scheduled, false, false},
		{"after schedule", scheduled.Add(time.Minute), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attemptedDay := ""
			if tc.name == "attempt completed" {
				attemptedDay = scheduled.Format("2006-01-02")
			}
			if got := keepaliveNeedsAwake(cfg, tc.at, tc.running, attemptedDay); got != tc.want {
				t.Fatalf("want %v, got %v", tc.want, got)
			}
		})
	}
	cfg.Enabled = false
	if keepaliveNeedsAwake(cfg, scheduled.Add(-time.Minute), false, "") {
		t.Fatal("disabled feature requested wakefulness")
	}
	cfg.Enabled = true
	cfg.Weekdays = []string{"Tue"}
	if keepaliveNeedsAwake(cfg, scheduled.Add(-time.Minute), false, "") {
		t.Fatal("unscheduled weekday requested wakefulness")
	}
	if keepaliveAttemptStarting(cfg, scheduled) {
		t.Fatal("unscheduled weekday started an attempt")
	}
	cfg.Weekdays = []string{"Wed"}
	if !keepaliveAttemptStarting(cfg, scheduled) || keepaliveAttemptStarting(cfg, scheduled.Add(31*time.Second)) {
		t.Fatal("scheduled attempt window is incorrect")
	}
}

func TestKeepaliveAwakeWindowCoversPendingTickAndMidnight(t *testing.T) {
	cfg := keepalive.DefaultConfig()
	cfg.Enabled = true
	scheduled := time.Date(2030, 1, 2, 12, 30, 0, 0, time.UTC)
	if !keepaliveNeedsAwake(cfg, scheduled.Add(10*time.Second), false, "") {
		t.Error("sleep protection ended before the scheduled tick could start")
	}
	cfg.Time, cfg.Weekdays = "00:20", []string{"Wed"}
	if !keepaliveNeedsAwake(cfg, time.Date(2030, 1, 1, 23, 30, 0, 0, time.UTC), false, "2030-01-01") {
		t.Error("sleep protection missed the activity window before midnight")
	}
}

func TestKeepaliveSleepProtectionUsesPersistedAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	service := keepalive.NewService(nil, path, func() float64 { return 0 })
	cfg := keepalive.DefaultConfig()
	cfg.Enabled = true
	scheduled := time.Date(2030, 1, 2, 12, 30, 0, 0, time.UTC)
	if err := service.Configure(cfg, scheduled.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if result := service.Tick(context.Background(), scheduled); result.Error != nil || result.Status != "Skipped: PC active or idle time unavailable" {
		t.Fatalf("schedule was not claimed: %+v", result)
	}
	cfg.Time = "13:00"
	before := scheduled.Add(10 * time.Minute)
	if err := service.Configure(cfg, before); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]*keepalive.Service{
		"reconfigured": service,
		"restarted":    keepalive.NewService(nil, path, nil),
	} {
		t.Run(name, func(t *testing.T) {
			day, err := s.LastAttemptDay()
			if err != nil || day != "2030-01-02" {
				t.Fatalf("persisted attempt lost: %q %v", day, err)
			}
			if keepaliveNeedsAwake(cfg, before, false, day) {
				t.Fatal("completed schedule requested sleep protection after settings change or restart")
			}
			if keepaliveNeedsAwake(cfg, before.AddDate(0, 0, -1), false, day) {
				t.Fatal("clock moving backwards revived a consumed schedule")
			}
			if !keepaliveNeedsAwake(cfg, before.AddDate(0, 0, 1), false, day) {
				t.Fatal("previous attempt suppressed the next day's protection")
			}
		})
	}
}

func TestKeepaliveTicks_TimeoutAbortsAndResumes(t *testing.T) {
	kt := newKeepaliveTicks(context.Background())
	_, done, ok := kt.begin()
	if !ok {
		t.Fatal("begin refused with no handover in progress")
	}
	release := make(chan struct{})
	go func() {
		defer done()
		<-release // stuck: ignores cancellation until the test lets it go
	}()

	if kt.quiesce(100 * time.Millisecond) {
		t.Fatal("quiesce must time out on a tick that never unwinds")
	}
	if _, _, ok := kt.begin(); ok {
		t.Fatal("new starts must stay blocked after a timed-out quiesce until resume")
	}

	kt.resume(context.Background())
	ctx2, done2, ok := kt.begin()
	if !ok {
		t.Fatal("resume must unblock new ticks for the retry")
	}
	if ctx2.Err() != nil {
		t.Fatalf("resumed context must be live, got %v", ctx2.Err())
	}
	done2()
	close(release) // let the stuck tick finish so its goroutine and done() unwind
}

func TestKeepaliveTicks_ParentCancelPropagates(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	kt := newKeepaliveTicks(parent)
	ctx, done, ok := kt.begin()
	if !ok {
		t.Fatal("begin refused")
	}
	defer done()
	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent cancel did not reach the tick context")
	}
}

func TestKeepaliveTicksRetryStillWaitsForPreviousRun(t *testing.T) {
	k := newKeepaliveTicks(context.Background())
	_, oldDone, _ := k.begin()
	if k.quiesce(0) {
		t.Fatal("unfinished run allowed handover")
	}
	k.resume(context.Background())
	_, newDone, _ := k.begin()
	newDone()
	if k.quiesce(0) {
		t.Fatal("previous run was lost after resume")
	}
	oldDone()
	if !k.quiesce(time.Second) {
		t.Fatal("completed runs blocked handover")
	}
	k.resume(context.Background())
	_, done, ok := k.begin()
	if !ok {
		t.Fatal("new generation could not start")
	}
	if k.quiesce(0) {
		t.Fatal("previous completion released a new run")
	}
	done()
	if !k.quiesce(time.Second) {
		t.Fatal("new generation did not drain")
	}
}

func TestKeepaliveTicksConcurrentStartAndQuiesce(t *testing.T) {
	for range 100 {
		k := newKeepaliveTicks(context.Background())
		start := make(chan struct{})
		finished := make(chan struct{})
		go func() {
			defer close(finished)
			<-start
			ctx, done, ok := k.begin()
			if ok {
				<-ctx.Done()
				done()
			}
		}()
		close(start)
		if !k.quiesce(time.Second) {
			t.Fatal("concurrent start escaped cancellation")
		}
		<-finished
		k.mu.Lock()
		active := k.active
		k.mu.Unlock()
		if active != 0 {
			t.Fatal("handover released before all runs finished")
		}
	}
}

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

func TestKeepaliveQuickOffIgnoresUnrelatedSettings(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(fmt.Sprint(broken), func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			now := time.Date(2026, 9, 9, 12, 29, 59, 0, time.UTC)
			next := keepalive.DefaultConfig()
			next.Enabled = true
			service := keepalive.NewService(nil, filepath.Join(t.TempDir(), "state.json"), func() float64 { t.Fatal("stopped service queried idle state"); return 0 })
			if err := service.Configure(next, now); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(settingsPath()), 0700); err != nil {
				t.Fatal(err)
			}
			original := `{"selected":["obsolete_item"],"futureNumber":9007199254740993,"keepalive":{"enabled":true,"time":"invalid","future":true}}`
			if broken {
				original = "invalid JSON"
			}
			if err := os.WriteFile(settingsPath(), []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			current := stopKeepalive(service, settings{Keepalive: &next})
			err := persistKeepaliveOff()
			if (err != nil) != broken {
				t.Fatalf("save: %v", err)
			}
			if current.Keepalive.Enabled {
				t.Fatal("runtime remained enabled")
			}
			if got := service.Tick(context.Background(), now.Add(time.Second)); got.Status != "" || got.Error != nil {
				t.Fatalf("stopped service ran: %+v", got)
			}
			saved, _ := os.ReadFile(settingsPath())
			if broken {
				if string(saved) != original {
					t.Fatal("corrupt source overwritten")
				}
			} else {
				for _, want := range []string{`9007199254740993`, `"obsolete_item"`, `"invalid"`, `"enabled": false`, `"future": true`} {
					if !strings.Contains(string(saved), want) {
						t.Fatalf("lost field %s: %s", want, saved)
					}
				}
			}
		})
	}
}
