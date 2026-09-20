package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/modelcatalog"
)

func TestExecPromptPreservesSharedCache(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, provider := range []string{"claude", "codex"} {
			for _, exitCode := range []int{0, 23} {
				t.Run(fmt.Sprintf("auto=%t/%s/exit=%d", automatic, provider, exitCode), func(t *testing.T) {
					home := autoPromptTestHome(t)
					binDir := filepath.Join(home, "bin")
					cacheDir := filepath.Join(home, ".config", "quota", "model-cache")
					for _, dir := range []string{binDir, cacheDir} {
						if err := os.MkdirAll(dir, 0700); err != nil {
							t.Fatal(err)
						}
					}
					t.Setenv("PATH", binDir)
					for _, p := range []string{"claude", "codex"} {
						bin := filepath.Join(binDir, p)
						script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then printf 'test-cli-v1\\n'; exit 0; fi\nprintf 'executed:%s\\n'\nexit %d\n", p, exitCode)
						if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
							t.Fatal(err)
						}
						dir := quotaTestAccountDir(t, filepath.Join(home, "."+p))
						id := "fable"
						if p == "codex" {
							id = "code-model"
						}
						target, err := modelTarget(p, dir)
						if err != nil {
							t.Fatal(err)
						}
						snapshot := modelcatalog.Snapshot{SchemaVersion: 1, Provider: p, Binary: bin, ConfigDir: dir, CLIVersion: "test-cli-v1", FetchedAt: time.Now(), Models: []modelcatalog.Model{autoPromptTestModel(id, "high")}}
						envKey := "CLAUDE_CONFIG_DIR"
						if p == "codex" {
							envKey = "CODEX_HOME"
						}
						override := envMap(target.Env)[envKey]
						identity, _ := json.Marshal([]any{p, bin, dir, override})
						sum := sha256.Sum256(identity)
						data, err := json.Marshal(snapshot)
						if err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Join(cacheDir, fmt.Sprintf("%x.json", sum)), data, 0600); err != nil {
							t.Fatal(err)
						}
					}
					settings := map[string]config.ExecPromptAccountSettings{}
					for _, p := range []string{"claude", "codex"} {
						floor := 100.0
						if p == provider {
							floor = 5
						}
						settings[p] = config.ExecPromptAccountSettings{MinLeftPct: &floor}
					}
					if err := config.Save(config.Config{ExecPrompt: &config.ExecPromptConfig{AccountSettings: settings}}); err != nil {
						t.Fatal(err)
					}
					putAutoPromptQuota(t, "claude", filepath.Join(home, ".claude"), "Current week (all models): 20% used\nCurrent session: 20% used\n")
					putAutoPromptQuota(t, "codex", filepath.Join(home, ".codex"), autoPromptCodexQuota(80, 80))
					path := filepath.Join(home, ".config", "quota", "quota-cache.json")
					before, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					beforeInfo, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					args := []string{"--agent=" + provider, "--model", "fable", "prompt"}
					if automatic {
						args = []string{"--model", "fable:high", "--model", "code-model:high", "--", "prompt"}
					}
					encoded, _ := json.Marshal(args)
					cmd := exec.Command(os.Args[0], "-test.run=^TestExecPromptCacheHelper$")
					cmd.Env = append(os.Environ(), "QUOTA_CACHE_HELPER_ARGS="+string(encoded))
					var stdout, stderr bytes.Buffer
					cmd.Stdout = &stdout
					cmd.Stderr = &stderr
					err = cmd.Run()
					got := 0
					if err != nil {
						e, ok := err.(*exec.ExitError)
						if !ok {
							t.Fatal(err)
						}
						got = e.ExitCode()
					}
					if got != exitCode || stdout.String() != "executed:"+provider+"\n" {
						t.Fatalf("exit=%d stdout=%q stderr=%q", got, stdout.String(), stderr.String())
					}
					after, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					afterInfo, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
						t.Fatal("delegation changed shared quota cache")
					}
					t.Setenv("PATH", "")
					if _, err := claude.GetQuotaForConfigDir(time.Second, "", 90*time.Second); err != nil {
						t.Fatalf("bar-equivalent Claude cache read: %v", err)
					}
					if _, err := codex.GetQuotaForHome(time.Second, "", 90*time.Second); err != nil {
						t.Fatalf("bar-equivalent Codex cache read: %v", err)
					}
				})
			}
		}
	}
}

func TestExecPromptCacheHelper(t *testing.T) {
	raw := os.Getenv("QUOTA_CACHE_HELPER_ARGS")
	if raw == "" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatal(err)
	}
	os.Exit(runExecPrompt(args))
}
