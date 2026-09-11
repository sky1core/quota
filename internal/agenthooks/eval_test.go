package agenthooks

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestGitHubHistoryGuardPresetTestsPass(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	results := RunPolicyTests([]Policy{policy})
	for _, result := range results {
		if !result.Passed {
			t.Fatalf("%s: got decision=%s rule=%s want decision=%s rule=%s error=%s", result.Name, result.Got, result.RuleID, result.Want, findPresetTestRule(policy, result.Name), result.Error)
		}
	}
}

func TestRunPolicyTestsSkipsDisabledPolicies(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policy.Enabled = false
	if results := RunPolicyTests([]Policy{policy}); len(results) != 0 {
		t.Fatalf("results = %+v, want none", results)
	}
}

func TestCommandFromHookEventAcceptsCommandAndCmd(t *testing.T) {
	for _, key := range []string{"command", "cmd"} {
		b, err := json.Marshal(map[string]any{
			"tool_input": map[string]any{key: "git push origin main"},
		})
		if err != nil {
			t.Fatal(err)
		}
		got, err := CommandFromHookEvent(b)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if got != "git push origin main" {
			t.Fatalf("%s: command = %q", key, got)
		}
	}
}

func TestParseShellInvocationsNormalizesWrappers(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    [][]string
	}{
		{name: "absolute command", command: filepath.Join(string(filepath.Separator), "usr", "bin", "git") + " push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "dashed git helper", command: filepath.Join(string(filepath.Separator), "usr", "libexec", "git-core", "git-push") + " origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env wrapper", command: "env FOO=bar git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env path wrapper", command: "env -P /usr/bin git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env inline path wrapper", command: "env -P/usr/bin git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env inline unset wrapper", command: "env -uPATH git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "env grouped unset wrapper", command: "env -iuPATH git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo wrapper", command: "sudo -u nobody git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo env assignment wrapper", command: "sudo FOO=bar git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo chdir wrapper", command: "sudo -D /tmp git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "sudo inline user wrapper", command: "sudo -unobody git push origin main", want: [][]string{{"git", "push", "origin", "main"}}},
		{name: "nested shell", command: "sh -c 'git push origin main && gh pr merge 7'", want: [][]string{{"git", "push", "origin", "main"}, {"gh", "pr", "merge", "7"}}},
		{name: "gh inline repo option", command: "gh -Rcli/cli pr merge 7", want: [][]string{{"gh", "pr", "merge", "7"}}},
		{name: "quoted literal", command: `gh pr create --title "hello world"`, want: [][]string{{"gh", "pr", "create", "--title", "hello world"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseShellInvocations(tt.command)
			if err != nil {
				t.Fatal(err)
			}
			var argvs [][]string
			for _, inv := range got {
				argvs = append(argvs, inv.Argv)
			}
			if !reflect.DeepEqual(argvs, tt.want) {
				t.Fatalf("argvs = %#v, want %#v", argvs, tt.want)
			}
		})
	}
}

func TestGhConfigWrapperCanChangeCommandDispatch(t *testing.T) {
	tests := []string{
		"command sudo GH_CONFIG_DIR=/tmp/ghcfg gh pr view 12",
		"env FOO=bar sudo --preserve-env=GH_CONFIG_DIR gh pr view 12",
		"exec sudo -E gh pr view 12",
	}
	for _, command := range tests {
		invocations, err := ParseShellInvocations(command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if len(invocations) != 1 || !invocations[0].DynamicCommand {
			t.Fatalf("%s: argv = %q dynamic=%v dynamicCommand=%v reason=%q, want one dynamic command", command, invocations[0].Argv, invocations[0].Dynamic, invocations[0].DynamicCommand, invocations[0].DynamicReason)
		}
	}
}

func TestNormalizeEnvSudoWrapper(t *testing.T) {
	norm, _, _ := normalizeArgv([]string{"env", "FOO=bar", "sudo", "--preserve-env=GH_CONFIG_DIR", "gh", "pr", "view", "12"})
	if !reflect.DeepEqual(norm, []string{"gh", "pr", "view", "12"}) {
		t.Fatalf("norm = %q", norm)
	}
}

func TestParseShellInvocationsMarksExpansionDynamic(t *testing.T) {
	tests := []struct {
		command        string
		dynamicCommand bool
	}{
		{`g{i..i}t push origin main`, true},
		{`g{i,j}it push origin main`, true},
		{`/usr/bin/g[i]t push origin main`, true},
		{`git pu*sh origin main`, false},
	}
	for _, tt := range tests {
		invocations, err := ParseShellInvocations(tt.command)
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		if len(invocations) == 0 || !invocations[0].Dynamic {
			t.Fatalf("%s: want dynamic invocation, got %#v", tt.command, invocations)
		}
		if invocations[0].DynamicCommand != tt.dynamicCommand {
			t.Fatalf("%s: dynamicCommand = %v, want %v", tt.command, invocations[0].DynamicCommand, tt.dynamicCommand)
		}
	}
}

func TestRemovedGitSubcommandsFailClosed(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	policies := []Policy{policy}
	// Optional/separately-packaged tools and subcommands first shipped after git
	// 2.30 were removed from the known list: they may be absent, so a pre-existing
	// alias could shadow them, and they must fall back to the fail-closed deny.
	for _, sub := range []string{"svn", "scalar", "replay", "repo", "refs", "diagnose", "gui", "gitk", "p4", "send-email", "backfill", "last-modified"} {
		decision, err := EvaluateCommand(policies, "git "+sub+" origin main")
		if err != nil {
			t.Fatalf("git %s: %v", sub, err)
		}
		if decision.Allowed {
			t.Fatalf("git %s: decision = %+v, want deny (unknown subcommand fail-closed)", sub, decision)
		}
	}
	// Guaranteed builtins/core commands (git 2.30) stay recognized and are not
	// treated as an unknown alias dispatch.
	for _, sub := range []string{"maintenance", "status", "bugreport", "commit-tree", "multi-pack-index"} {
		decision, err := EvaluateCommand(policies, "git "+sub+" -h")
		if err != nil {
			t.Fatalf("git %s: %v", sub, err)
		}
		if !decision.Allowed {
			t.Fatalf("git %s: decision = %+v, want allow", sub, decision)
		}
	}
}

func TestReflogSelectorStaysLiteral(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		{`git show HEAD@{u}`, []string{"git", "show", "HEAD@{u}"}},
		{`git stash drop stash@{0}`, []string{"git", "stash", "drop", "stash@{0}"}},
		{`git show HEAD@\{u\}`, []string{"git", "show", "HEAD@{u}"}},
	}
	for _, tt := range tests {
		invocations, err := ParseShellInvocations(tt.command)
		if err != nil {
			t.Fatalf("%s: %v", tt.command, err)
		}
		if len(invocations) != 1 || invocations[0].Dynamic {
			t.Fatalf("%s: reflog selector should stay literal, got %#v", tt.command, invocations)
		}
		if !reflect.DeepEqual(invocations[0].Argv, tt.want) {
			t.Fatalf("%s: argv = %#v, want %#v", tt.command, invocations[0].Argv, tt.want)
		}
	}
}

func TestQuotedExpansionMetaStaysLiteral(t *testing.T) {
	invocations, err := ParseShellInvocations(`git show 'HEAD@{u}'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(invocations) != 1 || invocations[0].Dynamic {
		t.Fatalf("quoted metacharacters should stay literal, got %#v", invocations)
	}
	if want := []string{"git", "show", "HEAD@{u}"}; !reflect.DeepEqual(invocations[0].Argv, want) {
		t.Fatalf("argv = %#v, want %#v", invocations[0].Argv, want)
	}
}

func TestEvaluateAllowRuleDoesNotShortCircuitCompound(t *testing.T) {
	policies := []Policy{{
		Version: PolicyVersion,
		ID:      "compound",
		Enabled: true,
		Rules: []Rule{
			{ID: "allow-git-status", Effect: EffectAllow, Match: Match{Argv: exactArgs("git", "status")}},
			{ID: "deny-git-push", Effect: EffectDeny, Match: Match{Argv: exactArgs("git", "push")}},
		},
	}}
	for _, command := range []string{
		`git status && git push origin main`,
		`git status; git push origin main`,
	} {
		decision, err := EvaluateCommand(policies, command)
		if err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		if decision.Allowed || decision.RuleID != "deny-git-push" {
			t.Fatalf("%s: decision = %+v, want deny-git-push", command, decision)
		}
	}
	allow, err := EvaluateCommand(policies, `git status && git status --short`)
	if err != nil {
		t.Fatal(err)
	}
	if !allow.Allowed {
		t.Fatalf("all-allow compound = %+v, want allow", allow)
	}
}

func TestGitCommitFlagValuesThroughHookEvent(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		command string
		allowed bool
	}{
		{`git commit -m --amend`, true},
		{`git commit -m--amend`, true},
		{`git commit -qam--amend`, true},
		{`git commit --message --amend`, true},
		{`git commit --message=--amend`, true},
		{`git commit --mess --amend`, true},
		{`git commit -F --amend`, true},
		{`git commit --template --amend`, true},
		{`git commit -S--amend -m message`, true},
		{`git commit -m message -- --amend`, true},
		{`git commit --verbose -- --amend`, true},
		{`git commit --no-message -m message`, true},
		{`git commit --no-amend -m message`, true},
		{`git commit --no-am -m message`, true},
		{`git commit --amend --no-amend -m message`, true},
		{`git commit --amen --no-am -m message`, true},
		{`git commit --amend --no-amend --amend -m message`, false},
		{`git commit --amend -m --no-amend`, false},
		{`git commit --verbose=1 -m message`, false},
		{`git commit --status=true -m message`, false},
		{`git commit --allow-empty=true -m message`, false},
		{`git commit --amend -m message`, false},
		{`git commit -m -- --amend`, false},
		{`git commit --mess -- --amend`, false},
		{`git commit -qm -- --amend`, false},
		{`git commit -m-- --amend`, false},
		{`git commit -m --amend --amend`, false},
		{`git commit --message=--amend --amend`, false},
		{`git commit --no-message --amend`, false},
		{`git commit --no-message --amen`, false},
		{`git commit -S --amend`, false},
		{`git commit -u --amend`, false},
		{`git commit --unknown-option -- --amend`, false},
		{`git commit --m -- --amend`, false},
		{`git commit -m --amend && git push origin main`, false},
		{`git commit -m --amend; git commit --amend -m message`, false},
		{`command git -C . commit -m --amend`, true},
		{`sh -c 'git commit -m -- --amend'`, false},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"command": tt.command}})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := EvaluateHookEvent([]Policy{policy}, input)
			if err != nil || decision.Allowed != tt.allowed {
				t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, tt.allowed)
			}
		})
	}
}

func TestEvaluateCommandEmptyCommitArgs(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	wrappers := []string{
		``, `command -- `, `builtin command `, `exec -a '' `,
		`env -u '' `, `sudo -p '' `,
		`command exec -a '' env FOO= sudo -p '' `,
	}
	for _, wrapper := range wrappers {
		for _, args := range []string{
			`--allow-empty-message -m ""`,
			`--allow-empty-message --message ''`,
			`--allow-empty-message -qm ''""`,
			`--allow-empty-message --mess ""`,
			`--allow-empty-message -m $''`,
			`--allow-empty-message --message=`,
			`-m message`,
			`-m --amend`,
		} {
			for _, amend := range []bool{false, true} {
				command := wrapper + `git -C '' commit ` + args
				if amend {
					command += ` --amend`
				}
				t.Run(command, func(t *testing.T) {
					decision, err := EvaluateCommand([]Policy{policy}, command)
					if err != nil || decision.Allowed == amend {
						t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, !amend)
					}
					if amend && decision.RuleID != "deny-git-commit-amend" {
						t.Fatalf("decision = %+v, want amend rule", decision)
					}
				})
			}
		}
	}
	for _, tt := range []struct {
		command string
		allowed bool
	}{
		{`git commit --allow-empty-message -m ""`, true},
		{`git commit --allow-empty-message -m "" --amend`, false},
		{`git-commit --allow-empty-message -m "" --amend`, false},
		{`git commit -m '' -- --amend`, true},
		{`git commit -m '' --amend --no-amend`, true},
		{`git commit -m '' --no-amend --amend`, false},
		{`git commit --author '' --amend`, false},
		{`git commit --trailer '' --amend`, false},
		{`git commit -F '' --amend`, false},
		{`sh -c 'git commit -m "" --amend'`, false},
		{`sh -c '' 'git commit --amend'`, true},
		{`env -S 'git commit -m' '' --amend`, false},
		{`env -S 'git commit --allow-empty-message -m' ''`, true},
		{`env -S 'git commit -m' 'message --amend'`, true},
		{`command env -S 'git commit -m' '' --amend`, false},
		{`eval 'git commit -m' '' --amend`, true},
		{`command eval 'git commit -m' '' --amend`, true},
		{`'' git commit --amend`, true},
		{`command '' git commit --amend`, true},
		{`env '' git commit --amend`, true},
		{`exec -a '' '' git commit --amend`, true},
		{`sudo -p '' '' git commit --amend`, true},
	} {
		t.Run(tt.command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, tt.command)
			if err != nil || decision.Allowed != tt.allowed {
				t.Fatalf("decision = %+v, err = %v, want allowed=%v", decision, err, tt.allowed)
			}
		})
	}
}

func TestEvaluateHookEventDeniesProtectedCommand(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git push origin main"}}`)
	decision, err := EvaluateHookEvent([]Policy{policy}, input)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed || decision.RuleID != "deny-git-push" {
		t.Fatalf("decision = %+v, want deny-git-push", decision)
	}
}

func findPresetTestRule(policy Policy, name string) string {
	for _, test := range policy.Tests {
		if test.Name == name {
			return test.RuleID
		}
	}
	return ""
}

func TestStaticEmptyArgumentRemainsVisibleInDecision(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	result, err := EvaluateCommand([]Policy{policy}, `git commit --allow-empty-message -m "" --amend`)
	if err != nil || result.Allowed || len(result.Command) != 6 || result.Command[4] != "" {
		t.Fatalf("decision=%+v err=%v", result, err)
	}
}

func TestEnvSplitPreservesAssignmentsAndLiteralMessages(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		allowed bool
	}{
		{`env -S '' FOO=bar git push origin main`, false},
		{`command env --split-string='' FOO=bar git commit -m '' --amend`, false},
		{`env -iS '' FOO=bar git push origin main`, false},
		{`env -S '' GIT_CONFIG_GLOBAL=fixture git status`, false},
		{`env -S 'git commit -m' 'subject
body'`, true},
		{"env -S 'git commit -m' 'subject\tbody'", true},
		{`env -S 'git commit -m' 'subject --amend'`, true},
		{`env -S '' FOO=bar git commit -m ''`, true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got, err := EvaluateCommand([]Policy{policy}, tc.command)
			if err != nil || got.Allowed != tc.allowed {
				t.Fatalf("decision = %+v, err=%v, want allowed=%v", got, err, tc.allowed)
			}
		})
	}
}

func TestRecursiveShellParsingPreservesStartupEnvironment(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		command string
		allowed bool
	}{
		{`BASH_ENV=fixture command env -S 'bash -c true'`, false},
		{`BASH_ENV=fixture env --split-string='bash -c true'`, false},
		{`env BASH_ENV=fixture -S 'bash -c true'`, false},
		{`BASH_ENV=fixture command eval 'bash -c true'`, false},
		{`ENV=fixture env -S 'sh -c true'`, false},
		{`ZDOTDIR=fixture command env -S 'zsh -c true'`, false},
		{`BASH_ENV=fixture env -S 'env -S "bash -c true"'`, false},
		{`BASH_ENV=fixture env -S 'echo safe'`, true},
		{`env -S 'bash -c true'`, true},
	} {
		t.Run(tc.command, func(t *testing.T) {
			got, err := EvaluateCommand([]Policy{policy}, tc.command)
			if err != nil || got.Allowed != tc.allowed {
				t.Fatalf("decision=%+v error=%v, want allowed=%v", got, err, tc.allowed)
			}
		})
	}
}
