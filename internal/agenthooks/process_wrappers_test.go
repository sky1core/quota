package agenthooks

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestProcessWrappersThroughHookEvent(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, wrapper := range []string{
		"", "nohup", "/usr/bin/nohup --", "nice", "nice -n 5", "nice -n5", "nice -5",
		"nice --adjustment=5 --", "timeout 5", "timeout --signal TERM --kill-after 1 5",
		"timeout -vsTERM -k1 5", "timeout --foreground --preserve-status -- 5",
		"nohup nice -n 5 timeout 5", "command env EXAMPLE=value nohup timeout 5 nice",
		"sudo -u example nohup nice timeout 5",
	} {
		for _, tc := range []struct {
			command string
			allow   bool
		}{
			{`git push`, false},
			{`git --attr-source=HEAD push`, false},
			{`git reset --hard`, false},
			{`git commit --amend`, false},
			{`git status --short`, true},
			{`git diff --stat`, true},
			{`git -C . -c color.ui=false status`, true},
			{`git -c include.path=example status`, false},
			{`sudo GIT_CONFIG_GLOBAL=example git status`, false},
			{`sudo --preserve-env=GIT_CONFIG_GLOBAL git status`, false},
			{`git commit --no-t --dry-run --allow-empty -m example`, true},
			{`gh pr create --head feature --title example`, true},
			{`gh pr create --title example`, false},
			{`gh pr checkout 123 --force=false`, true},
			{`gh pr checkout 123 -f=false --force`, false},
			{`echo git push`, false},
			{`custom-tool git push`, false},
			{`"$cmd" push`, false},
			{`git "$subcommand"`, false},
			{`sh -c 'git push'`, false},
			{`sh -c 'git status'`, true},
			{`sh -c "$script"`, false},
			{`env -S 'git push'`, false},
			{`env -S 'git status'`, true},
			{`env - git push origin main`, false},
			{`env -- - git push origin main`, false},
			{`env -i - git push origin main`, false},
			{`env - git status`, true},
			{`env - -u EXAMPLE git status`, true},
			{`env --help`, true},
			{`env --version`, true},
			{`env -- - GIT_CONFIG_GLOBAL=example git status`, false},
			{`env -- - GH_CONFIG_DIR=example gh pr view 123`, false},
			{`env -- - BASH_ENV=example bash -c true`, false},
			{`env GIT_CONFIG_GLOBAL=example git status`, false},
			{`env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.example GIT_CONFIG_VALUE_0=status git status`, false},
			{`env GH_CONFIG_DIR=example gh pr view 123`, false},
			{`env GH_CONFIG_DIR=example sh -c 'gh pr view 123'`, false},
			{`env BASH_ENV=example bash -c 'git status'`, false},
			{`env -S 'BASH_ENV=example bash -c "git status"'`, false},
			{`sudo --preserve-env=GH_CONFIG_DIR gh pr view 123`, false},
			{`exec -l bash -c 'git status'`, false},
			{`env EXAMPLE=value git status`, true},
			{`env EXAMPLE=value --unset=IGNORED git push origin main`, false},
			{`env -- --unset=IGNORED git push origin main`, false},
			{`env EXAMPLE=value --unset=IGNORED git status`, true},
			{`env EXAMPLE=value --unset=IGNORED GIT_CONFIG_GLOBAL=example git status`, false},
			{`env EXAMPLE=value --unset=IGNORED GH_CONFIG_DIR=example gh pr view 123`, false},
			{`env EXAMPLE=value --unset=IGNORED BASH_ENV=example bash -c true`, false},
			{`env -- EXAMPLE=value git status`, true},
			{`env -- GIT_CONFIG_GLOBAL=example git status`, false},
			{`env -- GH_CONFIG_DIR=example gh pr view 123`, false},
			{`env -- BASH_ENV=example bash -c 'git status'`, false},
			{`env -u GIT_CONFIG_GLOBAL git status`, true},
			{`env -u GH_CONFIG_DIR gh pr view 123`, true},
			{`env -u BASH_ENV bash -c 'git status'`, true},
		} {
			t.Run(wrapper+"/"+tc.command, func(t *testing.T) {
				input, err := json.Marshal(map[string]any{"tool_input": map[string]string{"command": wrapper + " " + tc.command}})
				if err != nil {
					t.Fatal(err)
				}
				decision, err := EvaluateHookEvent([]Policy{policy}, input)
				if err != nil || decision.Allowed != tc.allow {
					t.Fatalf("decision=%+v err=%v want allowed=%v", decision, err, tc.allow)
				}
				if (tc.command == "git push" || tc.command == "git --attr-source=HEAD push") && decision.RuleID != "deny-git-push" {
					t.Fatalf("expected push rule, got %+v", decision)
				}
			})
		}
	}
}

func TestWrapperOptionConsumption(t *testing.T) {
	for _, tc := range []struct {
		command string
		argv    []string
	}{
		{`nice -n git echo push`, []string{"echo", "push"}},
		{`timeout -s git -k push 5 echo git push`, []string{"echo", "git", "push"}},
		{`timeout --signal=git --kill-after=push 5 echo git push`, []string{"echo", "git", "push"}},
		{`timeout git echo push`, []string{"echo", "push"}},
		{`nohup echo timeout 5 git push`, []string{"echo", "timeout", "5", "git", "push"}},
		{`timeout 5 nice -n 1 git status`, []string{"git", "status"}},
		{`git --attr-source push status`, []string{"git", "status"}},
		{`git --attr-source=push status`, []string{"git", "status"}},
		{`nice -n GH_CONFIG_DIR=example gh pr view 123`, []string{"gh", "pr", "view", "123"}},
		{`timeout -s GIT_CONFIG_GLOBAL=example 5 git status`, []string{"git", "status"}},
		{`env EXAMPLE=value -- git push`, []string{"--", "git", "push"}},
		{`env EXAMPLE=value - git push`, []string{"-", "git", "push"}},
		{`nice --help git push`, []string{"nice", "--help", "git", "push"}},
	} {
		t.Run(tc.command, func(t *testing.T) {
			inv, err := ParseShellInvocations(tc.command)
			if err != nil || len(inv) != 1 || inv[0].Dynamic || !reflect.DeepEqual(inv[0].Argv, tc.argv) {
				t.Fatalf("invocations=%+v err=%v want argv=%q", inv, err, tc.argv)
			}
		})
	}
}

func TestGlobalAndWrapperNormalizationFailsClosed(t *testing.T) {
	policy, err := Preset(PresetGitHubHistoryGuard)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`git --unknown=value push`, `git --unknown push status`, `git --unknown status`,
		`git --attr-source`, `git -C`, `gh --unknown=value pr view 123`, `gh --repo`,
		`nohup git --unknown=value push`, `timeout --unknown 5 git push`, `nice --unknown git push`,
		`nice -n`, `timeout -s`, `timeout --signal`,
		`env --unknown=value git push`, `env --argv0=example git push`, `env -u`,
		`nohup "$cmd"`, `nice -n "$priority" git status`, `timeout "$duration" git status`,
		`env GIT_CONFIG_GLOBAL=example nohup nice timeout 5 git status`,
		`env GH_CONFIG_DIR=example nohup nice timeout 5 gh pr view 123`,
		`env BASH_ENV=example nohup nice timeout 5 bash -c 'git status'`,
	} {
		t.Run(command, func(t *testing.T) {
			decision, err := EvaluateCommand([]Policy{policy}, command)
			if err != nil || decision.Allowed || decision.Reason == "" {
				t.Fatalf("decision=%+v err=%v want rejection", decision, err)
			}
		})
	}
}
