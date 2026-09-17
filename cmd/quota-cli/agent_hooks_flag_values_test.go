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
		{`git --attr-source=HEAD push`, false},
		{`git --attr-source HEAD push`, false},
		{`git --future-option=value push`, false},
		{`gh --future-option=value pr view 123`, false},
		{`nohup git push`, false},
		{`nice git push`, false},
		{`timeout 5 git push`, false},
		{`nohup nice -n 5 timeout -s TERM 5 git push`, false},
		{`timeout 5 git status`, true},
		{`nice -n 5 gh pr create --head feature --title example`, true},
		{`nice -n 5 gh pr create --title example`, false},
		{`gh pr create --head= --title example`, false},
		{`gh pr create -H '' --title example`, false},
		{`gh pr create --head feature --head '' --title example`, false},
		{`git commit --no-t --dry-run --allow-empty -m example`, true},
		{`gh pr checkout 123 --force=false`, true},
		{`gh pr checkout 123 -f --force=false`, true},
		{`gh pr checkout 123 --force=false -f`, false},
		{`gh pr close 12 --delete-branch=false`, true},
		{`gh pr close 12 -d --delete-branch=false`, true},
		{`gh pr close 12 --delete-branch=false -d`, false},
		{`gh pr -R example/repository checkout 123 --force`, false},
		{`gh pr -R example/repository merge 123`, false},
		{`gh pr -d list merge`, false},
		{`gh pr --delete-branch close merge`, false},
		{`gh release --draft edit create --notes example`, false},
		{`gh repo -h view edit --visibility public`, false},
		{`gh repo --allow-forking view edit --visibility public`, false},
		{`gh release --draft view create`, false},
		{`gh pr --web list view`, true},
		{`gh --json number pr view 123`, true},
		{`gh --comments 123 pr view`, true},
		{`gh issue list -h`, true},
		{`gh pr view -h`, true},
		{`gh pr --json merge view 123`, true},
		{`gh pr --branch merge checkout 123`, true},
		{`gh pr --force=false checkout 123`, true},
		{`gh --force=true pr -f=false checkout 123`, true},
		{`gh --force=false pr -f=true checkout 123`, false},
		{`gh --visibility public repo edit`, false},
		{`gh repo --description --visibility edit`, true},
		{`gh release --cleanup-tag delete v1`, false},
		{`gh --notes merge release create v1`, false},
		{`gh alias --shell set example 'pr merge'`, false},
		{`gh extension --help=false exec example --not-real`, false},
		{`gh -X DELETE api repos/example/repository`, false},
		{`gh pr view 123 --not-real`, false},
		{`gh pr --not-real value view 123`, false},
		{`gh pr -- checkout 123 --force`, false},
		{`gh pr view -- merge --force`, true},
		{`gh pr --repo "$repository" view 123`, false},
		{`command env -u EXAMPLE nice -n 5 gh pr -R example/repository merge 123`, false},
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
		{"--enable-secret-scanning", `gh repo edit example/repository --enable-secret-scanning=false`, false},
		{"--enable-secret-scanning-push-protection", `gh repo edit example/repository --enable-secret-scanning-push-protection=false`, false},
		{"--force", `gh pr checkout 12 --force -f`, false},
		{"-f", `gh pr checkout 12 -f --force`, false},
		{"--enable-secret-scanning", `gh --enable-secret-scanning=false repo edit example/repository`, false},
		{"--enable-secret-scanning=false", `gh repo --enable-secret-scanning=false edit --enable-secret-scanning=true`, false},
		{"--force", `gh --force=true pr -f=true checkout 12`, false},
		{"-f", `gh -f=true pr --force=true checkout 12`, false},
		{"--force", `gh --force=true pr checkout 12 -f=false`, true},
		{"--force=false", `gh --force=false pr checkout 12 -f`, false},
		{"--visibility=public", `gh --visibility=public repo edit`, false},
		{"--visibility=public", `gh repo --description --visibility=public edit`, true},
		{"--json", `gh --json number pr view 12`, false},
		{"--force", `gh --title --force pr create`, true},
		{"--force", `gh pr --not-real view 123`, false},
		{"--force", `gh extension exec example --force`, false},
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
