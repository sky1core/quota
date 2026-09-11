package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/config"
)

func writeAccountUpdateConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
		"unknown":{"id":9007199254740993,"decimal":1.234567890123456789,"exponent":1e+80},
		"claudeAccounts":[{"key":"claude-2","configDir":"~/claude-2","extra":{"id":9007199254740993}}],
		"codexAccounts":[{"key":"codex-2","home":"~/codex-2","extra":{"decimal":1.234567890123456789}}],
		"execPrompt":{"extra":{"id":9007199254740993},"accountSettings":{
			"claude":{"minLeftPct":12.34567890123456789,"extra":{"id":9007199254740993}},
			"claude-3":{"minLeftPct":25,"extra":true},
			"codex-3":{"minLeftPct":25,"extra":true}
		}}
	}`
	if err := os.WriteFile(config.Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAccountUpdateConfig(t *testing.T) map[string]any {
	t.Helper()
	root, err := agenthooks.ReadJSONObject(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestAccountMutationsPreserveUnknownJSON(t *testing.T) {
	writeAccountUpdateConfig(t)
	before := readAccountUpdateConfig(t)
	for _, provider := range []string{"claude", "codex"} {
		if code := runAccount([]string{"add", provider + "-3", "~/" + provider + "-3"}); code != 0 {
			t.Fatalf("%s add = %d", provider, code)
		}
		if code := runAccount([]string{"rm", provider + "-3"}); code != 0 {
			t.Fatalf("%s rm = %d", provider, code)
		}
		delete(before["execPrompt"].(map[string]any)["accountSettings"].(map[string]any), provider+"-3")
	}
	if got := readAccountUpdateConfig(t); !reflect.DeepEqual(got, before) {
		t.Fatalf("unrelated JSON changed:\ngot  %#v\nwant %#v", got, before)
	}
	info, err := os.Stat(config.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions changed: %v, %v", info, err)
	}
}

func TestAccountRemovePreservesExecPromptExtrasWithoutSettings(t *testing.T) {
	writeAccountUpdateConfig(t)
	_, err := agenthooks.UpdateJSONObjectWithBackup(config.Path(), func(root map[string]any) error {
		root["execPrompt"].(map[string]any)["accountSettings"] = map[string]any{"claude-2": map[string]any{"minLeftPct": 25}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if code := accountRemove([]string{"claude-2"}); code != 0 {
		t.Fatalf("remove = %d", code)
	}
	execPrompt := readAccountUpdateConfig(t)["execPrompt"].(map[string]any)
	if len(execPrompt) != 1 || execPrompt["extra"].(map[string]any)["id"] != json.Number("9007199254740993") {
		t.Fatalf("execPrompt extras changed: %#v", execPrompt)
	}
}

func TestAccountMutationsWaitForSettingsAndReadLatest(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, operation := range []string{"add", "rm"} {
			t.Run(provider+"/"+operation, func(t *testing.T) {
				writeAccountUpdateConfig(t)
				locked := make(chan struct{})
				release := make(chan struct{})
				settingsDone := make(chan error, 1)
				go func() {
					_, err := agenthooks.UpdateJSONObjectWithBackup(config.Path(), func(root map[string]any) error {
						close(locked)
						<-release
						field, dirField := provider+"Accounts", "configDir"
						if provider == "codex" {
							dirField = "home"
						}
						root[field] = append(root[field].([]any), map[string]any{"key": provider + "-3", dirField: "~/" + provider + "-3"})
						root["settingsChange"] = json.Number("9007199254740995")
						return nil
					})
					settingsDone <- err
				}()
				<-locked
				done := make(chan int, 1)
				started := make(chan struct{})
				go func() {
					close(started)
					args := []string{operation, provider + "-3"}
					if operation == "add" {
						args = append(args, "~/another")
					}
					done <- runAccount(args)
				}()
				<-started
				select {
				case code := <-done:
					close(release)
					<-settingsDone
					t.Fatalf("CLI finished before settings released lock: %d", code)
				case <-time.After(100 * time.Millisecond):
				}
				close(release)
				if err := <-settingsDone; err != nil {
					t.Fatal(err)
				}
				wantCode, wantRows := 0, 1
				if operation == "add" {
					wantCode, wantRows = 1, 2
				}
				if code := <-done; code != wantCode {
					t.Fatalf("CLI used stale accounts: exit=%d want=%d", code, wantCode)
				}
				root := readAccountUpdateConfig(t)
				if root["settingsChange"] != json.Number("9007199254740995") || len(root[provider+"Accounts"].([]any)) != wantRows {
					t.Fatalf("settings update lost: %#v", root)
				}
			})
		}
	}
}

func TestAccountConcurrentCLIAndSettingsWriters(t *testing.T) {
	writeAccountUpdateConfig(t)
	before := readAccountUpdateConfig(t)
	const writers = 16
	start := make(chan struct{})
	errs := make(chan error, writers*3)
	var wg sync.WaitGroup
	for i := range writers {
		for _, provider := range []string{"claude", "codex"} {
			wg.Go(func() {
				<-start
				key := fmt.Sprintf("%s-%d", provider, i+10)
				if code := runAccount([]string{"add", key, "~/" + key}); code != 0 {
					errs <- fmt.Errorf("add %s: %d", key, code)
					return
				}
				if i%2 == 0 {
					if code := runAccount([]string{"rm", key}); code != 0 {
						errs <- fmt.Errorf("rm %s: %d", key, code)
					}
				}
			})
		}
		wg.Go(func() {
			<-start
			_, err := agenthooks.UpdateJSONObjectWithBackup(config.Path(), func(root map[string]any) error {
				root[fmt.Sprintf("setting-%d", i)] = json.Number("9007199254740993")
				return nil
			})
			if err != nil {
				errs <- err
			}
		})
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	root := readAccountUpdateConfig(t)
	for i := range writers {
		if root[fmt.Sprintf("setting-%d", i)] != json.Number("9007199254740993") {
			t.Errorf("settings writer %d lost", i)
		}
	}
	for _, field := range []string{"unknown", "execPrompt"} {
		if !reflect.DeepEqual(root[field], before[field]) {
			t.Errorf("%s changed: %#v", field, root[field])
		}
	}
	for _, provider := range []string{"claude", "codex"} {
		rows := root[provider+"Accounts"].([]any)
		if len(rows) != 1+writers/2 || !reflect.DeepEqual(rows[0], before[provider+"Accounts"].([]any)[0]) {
			t.Errorf("%s rows lost: %#v", provider, rows)
		}
		keys := map[string]bool{}
		for _, row := range rows {
			keys[row.(map[string]any)["key"].(string)] = true
		}
		for i := range writers {
			key := fmt.Sprintf("%s-%d", provider, i+10)
			if keys[key] != (i%2 != 0) {
				t.Errorf("%s presence=%v", key, keys[key])
			}
		}
	}
}

func TestAccountConcurrentDuplicateDirectory(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			writeAccountUpdateConfig(t)
			start := make(chan struct{})
			results := make(chan int, 12)
			for i := range 12 {
				go func() {
					<-start
					dir := "~/shared"
					if i%2 == 0 {
						dir = filepath.Join(os.Getenv("HOME"), "shared")
					}
					results <- accountAdd([]string{fmt.Sprintf("%s-%d", provider, i+10), dir})
				}()
			}
			close(start)
			successes := 0
			for range 12 {
				if <-results == 0 {
					successes++
				}
			}
			if successes != 1 || len(readAccountUpdateConfig(t)[provider+"Accounts"].([]any)) != 2 {
				t.Fatalf("duplicate directory accepted: successes=%d", successes)
			}
		})
	}
}

func TestAccountAddRejectsCanonicalDuplicatesAndPreservesSpelling(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, kind := range []string{"default", "environment default", "registered"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				home := autoPromptTestHome(t)
				base := filepath.Join(home, "."+provider)
				if kind == "environment default" {
					base = filepath.Join(home, "inherited")
					env := "CLAUDE_CONFIG_DIR"
					if provider == "codex" {
						env = "CODEX_HOME"
					}
					t.Setenv(env, base)
				}
				if kind == "registered" {
					base = filepath.Join(home, "existing")
				}
				if err := os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(base, filepath.Join(home, "alias")); err != nil {
					t.Fatal(err)
				}
				if kind == "registered" {
					if code := accountAdd([]string{provider + "-2", base}); code != 0 {
						t.Fatal(code)
					}
				}
				before, _ := os.ReadFile(config.Path())
				if code := accountAdd([]string{provider + "-3", "~/alias"}); code == 0 {
					t.Fatal("duplicate registered")
				}
				after, _ := os.ReadFile(config.Path())
				if string(before) != string(after) {
					t.Fatal("rejected registration changed config")
				}
				t.Chdir(home)
				if code := accountAdd([]string{provider + "-9", "new-account"}); code != 0 {
					t.Fatal(code)
				}
				cfg, err := config.Load()
				if err != nil {
					t.Fatal(err)
				}
				if provider == "claude" {
					if cfg.ClaudeAccounts[len(cfg.ClaudeAccounts)-1].ConfigDir != "new-account" {
						t.Fatal("CLI spelling changed")
					}
					accounts, skipped := cfg.ResolveAccounts()
					if len(skipped) != 0 || accounts[len(accounts)-1].ConfigDir != filepath.Join(home, "new-account") {
						t.Fatalf("resolution %v %v", accounts, skipped)
					}
				} else {
					if cfg.CodexAccounts[len(cfg.CodexAccounts)-1].Home != "new-account" {
						t.Fatal("CLI spelling changed")
					}
					accounts, skipped := cfg.ResolveCodexAccounts()
					if len(skipped) != 0 || accounts[len(accounts)-1].Home != filepath.Join(home, "new-account") {
						t.Fatalf("resolution %v %v", accounts, skipped)
					}
				}
			})
		}
	}
}
