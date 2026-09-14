package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/agenthooks"
)

func TestAgentHooksEvalOptionValues(t *testing.T) {
	policyDir := filepath.Join(t.TempDir(), "policies")
	var stdout, stderr bytes.Buffer
	if code := runAgentHooks([]string{"init", "--preset", agenthooks.PresetGitHubHistoryGuard, "--policy-dir", policyDir}, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit %d: %s", code, stderr.String())
	}
	cases := []struct {
		command string
		allow   bool
	}{
		{`git tag -a audit-tag -m feature`, true},
		{`git tag -a audit-tag -mfeature`, true},
		{`git tag -a audit-tag -mdocs`, true},
		{`git tag -a audit-tag -m --delete`, true},
		{`git tag -amfeature audit-tag`, true},
		{`git tag --list --format --delete`, true},
		{`git branch --format --delete`, true},
		{`git branch --list --end-of-options`, true},
		{`git branch --list --end-of-options --delete`, true},
		{`git branch --delete --end-of-options feature`, false},
		{`git tag -m --end-of-options --delete audit-tag`, false},
		{`git tag -a audit-tag -m feature -f`, false},
		{`git tag -a audit-tag -m --delete --force`, false},
		{`git tag -d audit-tag`, false},
		{`git tag --delete audit-tag`, false},
		{`git branch -m old-name new-name`, false},
		{`git branch -c old-name new-name`, false},
		{`git switch -C existing`, false},
		{`git checkout -B existing`, false},
		{`git reset --hard`, false},
		{`git commit -m "" --amend`, false},
		{`git commit -m --amend`, true},
	}
	for _, runtime := range []string{"claude", "codex"} {
		for _, tc := range cases {
			t.Run(runtime+"/"+tc.command, func(t *testing.T) {
				input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"command": tc.command}})
				if err != nil {
					t.Fatal(err)
				}
				for _, outputJSON := range []bool{false, true} {
					args := []string{"--policy-dir", policyDir, "--runtime", runtime}
					if outputJSON {
						args = append(args, "--json")
					}
					var out, errOut bytes.Buffer
					code := agentHooksEval(args, strings.NewReader(string(input)), &out, &errOut)
					wantCode := 2
					if tc.allow {
						wantCode = 0
					}
					if code != wantCode {
						t.Fatalf("json=%v: exit %d, want %d; stdout=%s stderr=%s", outputJSON, code, wantCode, out.String(), errOut.String())
					}
					if outputJSON {
						var decision agenthooks.Decision
						if err := json.Unmarshal(out.Bytes(), &decision); err != nil {
							t.Fatal(err)
						}
						if decision.Allowed != tc.allow {
							t.Fatalf("JSON decision disagrees with exit: %+v", decision)
						}
					} else if !tc.allow && errOut.Len() == 0 {
						t.Fatal("denial did not explain the failure on stderr")
					}
				}
			})
		}
	}
}

func TestAgentHooksEvalCustomFlagPolicies(t *testing.T) {
	for _, tc := range []struct {
		flag    string
		command string
		allow   bool
	}{
		{"--delete", `git branch --delete --end-of-options feature`, false},
		{"--delete", `git branch --delete --unknown feature`, false},
		{"--delete", `git branch --list --end-of-options --delete`, true},
		{"--delete", `git branch --format --delete`, true},
		{"--visibility=public", `gh repo edit --visibility=public`, false},
		{"--visibility=public", `gh repo edit --visibility=private`, true},
		{"--visibility=public", `gh repo edit --description --visibility=public`, true},
	} {
		t.Run(tc.flag+"/"+tc.command, func(t *testing.T) {
			policy := agenthooks.Policy{
				Version: agenthooks.PolicyVersion, ID: "custom-flag", Enabled: true,
				Rules: []agenthooks.Rule{{ID: "deny-flag", Effect: agenthooks.EffectDeny,
					Match: agenthooks.Match{HasFlag: []string{tc.flag}}}},
			}
			policyDir := t.TempDir()
			if _, err := agenthooks.SavePolicy(policyDir, policy, false); err != nil {
				t.Fatal(err)
			}
			input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"command": tc.command}})
			if err != nil {
				t.Fatal(err)
			}
			for _, runtime := range []string{"claude", "codex"} {
				var out, errOut bytes.Buffer
				code := agentHooksEval([]string{"--policy-dir", policyDir, "--runtime", runtime, "--json"}, strings.NewReader(string(input)), &out, &errOut)
				wantCode := 2
				if tc.allow {
					wantCode = 0
				}
				var decision agenthooks.Decision
				if err := json.Unmarshal(out.Bytes(), &decision); err != nil {
					t.Fatalf("runtime=%s exit=%d: %v; stderr=%s", runtime, code, err, errOut.String())
				}
				if code != wantCode || decision.Allowed != tc.allow {
					t.Fatalf("runtime=%s exit=%d decision=%+v; want exit=%d allowed=%v", runtime, code, decision, wantCode, tc.allow)
				}
			}
		})
	}
}
