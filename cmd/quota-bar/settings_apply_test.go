package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/config"
)

func liveSettingsTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
}

func writeSettingsTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func liveSettingsTestSnapshot(t *testing.T) liveSettingsSnapshot {
	t.Helper()
	s, err := snapshotLiveSettings(7, newQuotaData(), false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLiveSettingsSaveAndRegistrationOnly(t *testing.T) {
	liveSettingsTestHome(t)
	writeSettingsTestFile(t, settingsPath(), `{"selected":["codex_weekly"],"showResetTime":false,"futureNumber":9007199254740993,"keepalive":{"enabled":false,"futureFlag":true}}`)
	writeSettingsTestFile(t, config.Path(), `{"claudeAccounts":[{"key":"claude-2","configDir":"~/account-two","futureFlag":true},{"key":"claude-3","configDir":"~/account-three"}],"futureNumber":9007199254740993,"execPrompt":{"futurePolicy":"retain","accountSettings":{"claude":{"futureFlag":true},"claude-2":{"minLeftPct":17,"futureFlag":true},"claude-3":{"minLeftPct":19},"codex-99":{"minLeftPct":23}}}}`)
	home, _ := os.UserHomeDir()
	accountFile := filepath.Join(home, "account-two", "auth.json")
	writeSettingsTestFile(t, accountFile, `{"placeholder":"must remain byte identical"}`)
	s := liveSettingsTestSnapshot(t)
	d := s.draft
	d.RefreshActiveMinutes, d.RefreshIdleMinutes = 11, 41
	d.ShowResetTime, d.StartAtLogin = true, true
	d.Keepalive.Time = "14:25"
	floor := 12.5
	d.Accounts[0].MinLeftPct = &floor
	d.Accounts[1].ConfigDir = "~/account-new"
	d.Accounts[1].MinLeftPct = nil
	d.Accounts = append(d.Accounts[:2], d.Accounts[3:]...)
	d.Accounts = append(d.Accounts, liveSettingsAccount{Provider: "codex", Key: "codex-2", ConfigDir: "~/codex-two", MinLeftPct: &floor})
	bar, shared, err := persistLiveSettings(s, d, 7)
	if err != nil {
		t.Fatal(err)
	}
	if bar.RefreshActiveMinutes != 11 || bar.RefreshIdleMinutes != 41 || !bar.ShowResetTime || bar.Keepalive.Time != "14:25" {
		t.Fatalf("bar runtime config: %+v", bar)
	}
	resolved, skipped := shared.ResolveAccounts()
	if len(resolved) != 2 || len(skipped) != 0 || resolved[1].ConfigDir != filepath.Join(home, "account-new") {
		t.Fatalf("accounts: %+v %v", resolved, skipped)
	}
	barRoot, err := agenthooks.ReadJSONObject(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	sharedRoot, err := agenthooks.ReadJSONObject(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if barRoot["futureNumber"].(json.Number).String() != "9007199254740993" || sharedRoot["futureNumber"].(json.Number).String() != "9007199254740993" {
		t.Fatal("unknown numeric precision lost")
	}
	if barRoot["keepalive"].(map[string]any)["futureFlag"] != true {
		t.Fatal("unknown keepalive setting lost")
	}
	if sharedRoot["claudeAccounts"].([]any)[0].(map[string]any)["futureFlag"] != true {
		t.Fatal("unknown account setting lost")
	}
	exec := sharedRoot["execPrompt"].(map[string]any)
	floors := exec["accountSettings"].(map[string]any)
	if exec["futurePolicy"] != "retain" || floors["claude"].(map[string]any)["futureFlag"] != true || floors["claude-2"].(map[string]any)["futureFlag"] != true {
		t.Fatal("unrelated account settings lost")
	}
	if _, exists := floors["claude-2"].(map[string]any)["minLeftPct"]; exists {
		t.Fatal("default floor not restored")
	}
	if _, exists := floors["claude-3"]; exists {
		t.Fatal("unregistered account floor remains")
	}
	if _, exists := floors["codex-99"]; !exists {
		t.Fatal("unrelated floor removed")
	}
	content, err := os.ReadFile(accountFile)
	if err != nil || string(content) != `{"placeholder":"must remain byte identical"}` {
		t.Fatalf("account data touched: %s %v", content, err)
	}
	for _, path := range []string{settingsPath(), config.Path()} {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatal("file permissions changed")
		}
	}
	if !isAutoStartEnabled() {
		t.Fatal("login registration missing")
	}
	s = liveSettingsTestSnapshot(t)
	s.draft.StartAtLogin = false
	if _, _, err = persistLiveSettings(s, s.draft, 7); err != nil {
		t.Fatal(err)
	}
	if isAutoStartEnabled() {
		t.Fatal("login registration remains")
	}
}

func TestLiveSettingsRejectsInvalidBeforeWriting(t *testing.T) {
	tests := map[string]func(*liveSettingsDraft){
		"zero refresh":      func(d *liveSettingsDraft) { d.RefreshActiveMinutes = 0 },
		"overflow refresh":  func(d *liveSettingsDraft) { d.RefreshIdleMinutes = math.MaxInt },
		"missing default":   func(d *liveSettingsDraft) { d.Accounts = d.Accounts[1:] },
		"default directory": func(d *liveSettingsDraft) { d.Accounts[0].ConfigDir = "~/changed" },
		"wrong provider":    func(d *liveSettingsDraft) { d.Accounts[0].Provider = "other" },
		"duplicate key":     func(d *liveSettingsDraft) { d.Accounts = append(d.Accounts, d.Accounts[0]) },
		"duplicate default directory": func(d *liveSettingsDraft) {
			d.Accounts = append(d.Accounts, liveSettingsAccount{Provider: "claude", Key: "claude-2", ConfigDir: "~/.claude"})
		},
		"NUL directory": func(d *liveSettingsDraft) {
			d.Accounts = append(d.Accounts, liveSettingsAccount{Provider: "codex", Key: "codex-2", ConfigDir: "invalid\x00directory"})
		},
		"wrong account key": func(d *liveSettingsDraft) {
			d.Accounts = append(d.Accounts, liveSettingsAccount{Provider: "codex", Key: "codex-x", ConfigDir: "~/other"})
		},
		"floor out of range":      func(d *liveSettingsDraft) { v := 101.0; d.Accounts[0].MinLeftPct = &v },
		"floor NaN":               func(d *liveSettingsDraft) { v := math.NaN(); d.Accounts[0].MinLeftPct = &v },
		"unknown selected":        func(d *liveSettingsDraft) { d.Selected = []string{"claude-x_session"} },
		"duplicate selected":      func(d *liveSettingsDraft) { d.Selected = []string{"claude_session", "claude_session"} },
		"invalid keepalive":       func(d *liveSettingsDraft) { d.Keepalive.Time = "25:20" },
		"empty keepalive message": func(d *liveSettingsDraft) { d.Keepalive.Message = " " },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			liveSettingsTestHome(t)
			s := liveSettingsTestSnapshot(t)
			d := s.draft
			change(&d)
			if _, _, err := persistLiveSettings(s, d, 7); err == nil {
				t.Fatal("invalid draft saved")
			}
			for _, path := range []string{settingsPath(), config.Path(), launchAgentPath()} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("validation wrote %s: %v", path, err)
				}
			}
		})
	}
}

func TestLiveSettingsRejectsStaleWindowAndExternalChanges(t *testing.T) {
	for _, kind := range []string{"generation", "bar", "shared", "login"} {
		t.Run(kind, func(t *testing.T) {
			liveSettingsTestHome(t)
			s := liveSettingsTestSnapshot(t)
			gen := uint64(7)
			path := ""
			switch kind {
			case "generation":
				gen++
			case "bar":
				path = settingsPath()
			case "shared":
				path = config.Path()
			case "login":
				path = launchAgentPath()
			}
			if path != "" {
				writeSettingsTestFile(t, path, `{"external":"preserve"}`)
			}
			if _, _, err := persistLiveSettings(s, s.draft, gen); err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("stale save: %v", err)
			}
			if path != "" {
				b, _ := os.ReadFile(path)
				if string(b) != `{"external":"preserve"}` {
					t.Fatal("external edit overwritten")
				}
			}
		})
	}
}

func TestLiveSettingsConcurrentWindowsOnlyOneCommits(t *testing.T) {
	liveSettingsTestHome(t)
	s := liveSettingsTestSnapshot(t)
	first, second := s.draft, s.draft
	first.RefreshActiveMinutes = 17
	second.RefreshActiveMinutes = 19
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, draft := range []liveSettingsDraft{first, second} {
		wg.Add(1)
		go func(d liveSettingsDraft) { defer wg.Done(); _, _, err := persistLiveSettings(s, d, 7); results <- err }(draft)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("commits=%d", successes)
	}
}

func TestLiveSettingsRollbackWhenSecondJSONCannotSave(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires permission enforcement")
	}
	liveSettingsTestHome(t)
	writeSettingsTestFile(t, settingsPath(), `{"selected":[],"future":"retain"}`)
	s := liveSettingsTestSnapshot(t)
	dir := filepath.Join(t.TempDir(), "protected")
	sharedPath := filepath.Join(dir, "config.json")
	writeSettingsTestFile(t, sharedPath, `{"future":"retain"}`)
	writeSettingsTestFile(t, sharedPath+".lock", "")
	var err error
	s.shared, err = readSettingsFile(sharedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	s.draft.StartAtLogin = true
	_, _, err = persistLiveSettings(s, s.draft, 7)
	if err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("missing rollback: %v", err)
	}
	current, err := agenthooks.ReadJSONObject(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	original, _ := decodeSettingsObject(s.bar.content)
	if settingsObjectHash(current) != settingsObjectHash(original) {
		t.Fatalf("bar not restored: %+v", current)
	}
	b, _ := os.ReadFile(sharedPath)
	if !bytes.Equal(b, s.shared.content) {
		t.Fatal("shared config changed")
	}
	if isAutoStartEnabled() {
		t.Fatal("login registration not rolled back")
	}
}

func TestLiveSettingsSnapshotReadsDiskAndRejectsCorruption(t *testing.T) {
	liveSettingsTestHome(t)
	writeSettingsTestFile(t, settingsPath(), `{"refreshActiveMinutes":23,"showResetTime":true,"selected":[]}`)
	s, err := snapshotLiveSettings(7, newQuotaData(), false)
	if err != nil {
		t.Fatal(err)
	}
	if s.draft.RefreshActiveMinutes != 23 || !s.draft.ShowResetTime {
		t.Fatal("snapshot used stale runtime fields")
	}
	writeSettingsTestFile(t, config.Path(), `{"invalid":`)
	if _, err = snapshotLiveSettings(7, newQuotaData(), false); err == nil {
		t.Fatal("corrupt JSON accepted")
	}
	b, _ := os.ReadFile(config.Path())
	if string(b) != `{"invalid":` {
		t.Fatal("corrupt file modified")
	}
}

func TestLiveSettingsDirectoryAliasesRejected(t *testing.T) {
	liveSettingsTestHome(t)
	home, _ := os.UserHomeDir()
	target := filepath.Join(home, "account")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "alias")); err != nil {
		t.Fatal(err)
	}
	s := liveSettingsTestSnapshot(t)
	s.draft.Accounts = append(s.draft.Accounts,
		liveSettingsAccount{Provider: "claude", Key: "claude-2", ConfigDir: "~/account/missing"},
		liveSettingsAccount{Provider: "claude", Key: "claude-3", ConfigDir: "~/alias/missing"})
	if _, _, err := persistLiveSettings(s, s.draft, 7); err == nil {
		t.Fatal("symlink directory alias accepted")
	}
}

func TestLiveSettingsDecodeRequiresCompletePayload(t *testing.T) {
	liveSettingsTestHome(t)
	s := liveSettingsTestSnapshot(t)
	b, err := json.Marshal(s.draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeLiveSettings(string(b)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`null`, `{}`, string(b) + `{}`, strings.Replace(string(b), `"showResetTime":false,`, "", 1), strings.Replace(string(b), `"showResetTime":false`, `"unknown":false`, 1)} {
		if _, err = decodeLiveSettings(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestLiveSettingsRollbackRejectsConcurrentEdit(t *testing.T) {
	liveSettingsTestHome(t)
	writeSettingsTestFile(t, settingsPath(), `{"selected":[],"original":true}`)
	original, err := readSettingsFile(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	expected, _ := decodeSettingsObject([]byte(`{"selected":["codex_weekly"],"original":true}`))
	writeSettingsTestFile(t, settingsPath(), `{"selected":[],"external":true}`)
	if err = rollbackSettingsObject(original, expected); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("rollback overwrote external edit: %v", err)
	}
	b, _ := os.ReadFile(settingsPath())
	if string(b) != `{"selected":[],"external":true}` {
		t.Fatal("external content changed")
	}
}

func TestLiveSettingsRejectsAliasedJSONFiles(t *testing.T) {
	liveSettingsTestHome(t)
	writeSettingsTestFile(t, settingsPath(), `{"selected":[]}`)
	if err := os.Symlink(settingsPath(), config.Path()); err != nil {
		t.Fatal(err)
	}
	s := liveSettingsTestSnapshot(t)
	if _, _, err := persistLiveSettings(s, s.draft, 7); err == nil || !strings.Contains(err.Error(), "separate") {
		t.Fatalf("aliased JSON files accepted: %v", err)
	}
}

func TestLiveSettingsRequiresExplicitKeepaliveFields(t *testing.T) {
	liveSettingsTestHome(t)
	s := liveSettingsTestSnapshot(t)
	b, _ := json.Marshal(s.draft)
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{nil, map[string]any{}, map[string]any{"enabled": false}} {
		root["keepalive"] = value
		b, _ := json.Marshal(root)
		if _, err := decodeLiveSettings(string(b)); err == nil {
			t.Fatal("incomplete keepalive payload accepted")
		}
	}
}

func TestBarOperationGateProtectsSettingsAndRefreshHandover(t *testing.T) {
	var gate barOperationGate
	if !gate.begin(barOperationRefresh) {
		t.Fatal("refresh refused")
	}
	if gate.begin(barOperationRefresh) {
		t.Fatal("overlapping refresh allowed")
	}
	if !gate.begin(barOperationSettings) {
		t.Fatal("settings cannot interrupt in-flight refresh")
	}
	if gate.begin(barOperationHandover) {
		t.Fatal("handover interrupted active work")
	}
	gate.end(barOperationRefresh)
	if gate.begin(barOperationHandover) {
		t.Fatal("handover interrupted settings transaction")
	}
	gate.end(barOperationSettings)
	if !gate.begin(barOperationHandover) {
		t.Fatal("idle handover refused")
	}
	for _, operation := range []uint32{barOperationSettings, barOperationRefresh, barOperationHandover} {
		if gate.begin(operation) {
			t.Fatal("operation entered during handover")
		}
	}
	gate.end(barOperationHandover)
	if !gate.begin(barOperationSettings) {
		t.Fatal("failed handover did not release settings")
	}
}

func TestBarOperationGateConcurrentHandoverCannotOverlapWork(t *testing.T) {
	var gate barOperationGate
	var active sync.Mutex
	working := 0
	handingOver := false
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			operation := []uint32{barOperationSettings, barOperationRefresh, barOperationHandover}[worker%3]
			for attempt := 0; attempt < 500; attempt++ {
				if !gate.begin(operation) {
					continue
				}
				active.Lock()
				if operation == barOperationHandover {
					if working != 0 || handingOver {
						t.Error("handover overlapped work")
					}
					handingOver = true
				} else {
					if handingOver {
						t.Error("work overlapped handover")
					}
					working++
				}
				active.Unlock()
				active.Lock()
				if operation == barOperationHandover {
					handingOver = false
				} else {
					working--
				}
				active.Unlock()
				gate.end(operation)
			}
		}(worker)
	}
	wg.Wait()
}

func TestLiveSettingsRollbackRestoresMissingFile(t *testing.T) {
	liveSettingsTestHome(t)
	before, err := readSettingsFile(settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	writeSettingsTestFile(t, settingsPath(), `{"selected":["codex_weekly"]}`)
	expected, _ := decodeSettingsObject([]byte(`{"selected":["codex_weekly"]}`))
	if err = rollbackSettingsObject(before, expected); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(settingsPath()); !os.IsNotExist(err) {
		t.Fatalf("new settings file remains after rollback: %v", err)
	}
}

func TestUnrelatedSaveDoesNotUndoUnsavedKeepaliveStop(t *testing.T) {
	liveSettingsTestHome(t)
	writeSettingsTestFile(t, settingsPath(), `{"selected":[],"keepalive":{"enabled":true}}`)
	snapshot, err := snapshotLiveSettings(1, newQuotaData(), true)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.draft.Keepalive.Enabled {
		t.Fatal("unsaved stop was replaced by disk enabled state")
	}
	snapshot.draft.ShowResetTime = true
	saved, _, err := persistLiveSettings(snapshot, snapshot.draft, 1)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Keepalive.Enabled {
		t.Fatal("unrelated save re-enabled keepalive")
	}
	after, err := snapshotLiveSettings(2, newQuotaData(), false)
	if err != nil {
		t.Fatal(err)
	}
	if after.draft.Keepalive.Enabled {
		t.Fatal("stop was not persisted")
	}
	after.draft.Keepalive.Enabled = true
	saved, _, err = persistLiveSettings(after, after.draft, 2)
	if err != nil || !saved.Keepalive.Enabled {
		t.Fatalf("explicit enable failed: %+v %v", saved, err)
	}
}

func TestSettingsSaveDoesNotWaitForAnotherWriter(t *testing.T) {
	for _, operation := range []string{"settings", "keepalive-off"} {
		t.Run(operation, func(t *testing.T) {
			liveSettingsTestHome(t)
			snap := liveSettingsTestSnapshot(t)
			path := snap.shared.target
			if operation == "keepalive-off" {
				path = snap.bar.target
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			done := make(chan error, 1)
			go func() {
				if operation == "keepalive-off" {
					done <- persistKeepaliveOff()
					return
				}
				_, _, err := persistLiveSettings(snap, snap.draft, 7)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, syscall.EWOULDBLOCK) {
					t.Fatalf("expected busy result: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("settings blocked on another writer")
			}
		})
	}
}
