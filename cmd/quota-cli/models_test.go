package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/modelcatalog"
)

func TestModelsOptions(t *testing.T) {
	opts, err := parseModelOptions([]string{"refresh", "--agent=codex", "--account=codex", "--json"}, io.Discard)
	if err != nil || !opts.refresh || !opts.jsonOut || opts.agent != "codex" || opts.account != "codex" {
		t.Fatalf("options = %+v, %v", opts, err)
	}
	for _, args := range [][]string{{"--agent=other"}, {"refresh", "extra"}, {"--unknown"}, {"--agent=claude", "refresh"}} {
		if _, err := parseModelOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runModels([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("help exit %d", code)
	}
}

func TestModelAccountSelection(t *testing.T) {
	accounts, dirs, err := modelAccounts(config.Config{}, modelOptions{agent: "all"})
	if err != nil || len(accounts) != 2 || len(dirs) != 2 {
		t.Fatalf("accounts = %v, %v, %v", accounts, dirs, err)
	}
	accounts, _, err = modelAccounts(config.Config{}, modelOptions{agent: "codex", account: "codex"})
	if err != nil || len(accounts) != 1 || accounts[0].Provider != "codex" {
		t.Fatalf("accounts = %v, %v", accounts, err)
	}
	if _, _, err := modelAccounts(config.Config{}, modelOptions{agent: "claude", account: "codex"}); err == nil {
		t.Fatal("accepted mismatched account")
	}
}

type modelProvider struct {
	name       string
	envKey     string
	secretKey  string
	extraKey   string
	extraField func(dir string) config.Config
	env        func(base []string, dir string) []string
	setEnv     func(dir string) map[string]string
	unsetEnv   func(dir string) []string
}

func modelProviders() []modelProvider {
	return []modelProvider{
		{
			name:      "claude",
			envKey:    "CLAUDE_CONFIG_DIR",
			secretKey: "ANTHROPIC_API_KEY",
			extraKey:  "claude-2",
			extraField: func(dir string) config.Config {
				return config.Config{ClaudeAccounts: []config.ClaudeAccount{{Key: "claude-2", ConfigDir: dir}}}
			},
			env:      claude.EnvForConfigDir,
			setEnv:   selectAgentClaudeSetEnv,
			unsetEnv: selectAgentClaudeUnsetEnv,
		},
		{
			name:      "codex",
			envKey:    "CODEX_HOME",
			secretKey: "OPENAI_API_KEY",
			extraKey:  "codex-2",
			extraField: func(dir string) config.Config {
				return config.Config{CodexAccounts: []config.CodexAccount{{Key: "codex-2", Home: dir}}}
			},
			env:      codex.EnvForHome,
			setEnv:   selectAgentCodexSetEnv,
			unsetEnv: selectAgentCodexUnsetEnv,
		},
	}
}

func targetForAccount(t *testing.T, cfg config.Config, provider, account string) (modelcatalog.Target, string) {
	t.Helper()
	results, dirs, err := modelAccounts(cfg, modelOptions{agent: provider, account: account})
	if err != nil {
		t.Fatalf("modelAccounts(%s, %q): %v", provider, account, err)
	}
	for i := range results {
		if results[i].Provider == provider && (account == "" || results[i].Account == account) {
			target, err := modelTarget(provider, dirs[i])
			if err != nil {
				t.Fatalf("modelTarget(%s, %q): %v", provider, dirs[i], err)
			}
			return target, dirs[i]
		}
	}
	t.Fatalf("no %s account for %q in %v", provider, account, results)
	return modelcatalog.Target{}, ""
}

func TestModelTargetUsesQuotaDefaultAccount(t *testing.T) {
	bin := t.TempDir()
	for _, p := range modelProviders() {
		if err := os.WriteFile(filepath.Join(bin, p.name), []byte("placeholder"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range modelProviders() {
		t.Run(p.name, func(t *testing.T) {
			defaultCases := []struct {
				name      string
				setEnv    bool
				inherited string
			}{
				{name: "unspecified", setEnv: false},
				{name: "explicit-empty", setEnv: true, inherited: ""},
				{name: "inherited-absolute", setEnv: true, inherited: filepath.Join(t.TempDir(), "inherited")},
			}
			for _, tc := range defaultCases {
				t.Run("default/"+tc.name, func(t *testing.T) {
					home := t.TempDir()
					t.Setenv("HOME", home)
					t.Setenv("PATH", bin)
					t.Setenv("ANTHROPIC_API_KEY", "test-not-a-real-key")
					t.Setenv("OPENAI_API_KEY", "test-not-a-real-key")
					t.Setenv(p.envKey, "")
					if tc.setEnv {
						t.Setenv(p.envKey, tc.inherited)
					} else {
						if err := os.Unsetenv(p.envKey); err != nil {
							t.Fatal(err)
						}
					}

					target, dir := targetForAccount(t, config.Config{}, p.name, "")
					wantDir := quotaTestAccountDir(t, filepath.Join(home, "."+p.name))
					if dir != wantDir {
						t.Fatalf("default account dir = %q, want %q", dir, wantDir)
					}

					if want := p.env(os.Environ(), dir); !reflect.DeepEqual(target.Env, want) {
						t.Fatal("model discovery and quota/delegation environments differ")
					}
					got := envMap(target.Env)
					if _, ok := got[p.secretKey]; ok {
						t.Fatalf("auth credential %s leaked into run env", p.secretKey)
					}
					if p.name == "claude" {
						if v, ok := got[p.envKey]; ok {
							t.Fatalf("%s = %q (present=%v), want Claude builtin default", p.envKey, v, ok)
						}
					} else if v, ok := got[p.envKey]; !ok || v != wantDir {
						t.Fatalf("%s = %q (present=%v), want quota default %q", p.envKey, v, ok, wantDir)
					}

					if !filepath.IsAbs(target.ConfigDir) {
						t.Fatalf("cache identity not absolute: %q", target.ConfigDir)
					}

					removed := slices.Contains(p.unsetEnv(dir), p.envKey)
					if p.name == "claude" {
						if tc.setEnv && !removed {
							t.Fatal("default Claude recommendation must remove inherited CLAUDE_CONFIG_DIR")
						}
					} else if removed {
						t.Fatal("recommendation removes selected account environment")
					}
					set := p.setEnv(dir)
					if p.name == "claude" {
						if len(set) != 0 {
							t.Fatalf("default Claude account recommended override: %v", set)
						}
					} else if len(set) != 1 || set[p.envKey] != wantDir {
						t.Fatalf("default account recommended override: %v", set)
					}
					autoEnv := envMap(autoPromptEnv(autoPromptAccount{provider: p.name, dir: dir}, os.Environ()))
					if p.name == "claude" {
						if v, ok := autoEnv[p.envKey]; ok {
							t.Fatalf("autoPromptEnv %s = %q, want Claude builtin default", p.envKey, v)
						}
					} else if v := autoEnv[p.envKey]; v != wantDir {
						t.Fatalf("autoPromptEnv %s = %q, want quota default %q", p.envKey, v, wantDir)
					}
				})
			}

			t.Run("extra", func(t *testing.T) {
				home := t.TempDir()
				extra := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("PATH", bin)
				t.Setenv("ANTHROPIC_API_KEY", "test-not-a-real-key")
				t.Setenv("OPENAI_API_KEY", "test-not-a-real-key")
				t.Setenv(p.envKey, filepath.Join(home, "inherited"))

				target, dir := targetForAccount(t, p.extraField(extra), p.name, p.extraKey)
				if !filepath.IsAbs(dir) {
					t.Fatalf("extra account dir not absolute: %q", dir)
				}

				if !reflect.DeepEqual(target.Env, p.env(os.Environ(), dir)) {
					t.Fatal("extra model discovery and quota/delegation environments differ")
				}
				got := envMap(target.Env)
				if got[p.envKey] != dir {
					t.Fatalf("extra %s = %q, want override %q", p.envKey, got[p.envKey], dir)
				}
				if _, ok := got[p.secretKey]; ok {
					t.Fatalf("auth credential %s leaked into run env", p.secretKey)
				}
				if target.ConfigDir != dir {
					t.Fatalf("cache identity = %q, want %q", target.ConfigDir, dir)
				}
				if set := p.setEnv(dir); len(set) != 1 || set[p.envKey] != dir {
					t.Fatalf("extra account override = %v, want {%s:%s}", set, p.envKey, dir)
				}
				if v := envMap(autoPromptEnv(autoPromptAccount{provider: p.name, dir: dir}, os.Environ()))[p.envKey]; v != dir {
					t.Fatalf("autoPromptEnv %s = %q, want override %q", p.envKey, v, dir)
				}
			})
		})
	}
}

func TestModelTargetIgnoresRelativeInheritedAccountEnvironment(t *testing.T) {
	bin := t.TempDir()
	for _, p := range modelProviders() {
		if err := os.WriteFile(filepath.Join(bin, p.name), []byte("placeholder"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	for _, p := range modelProviders() {
		t.Run(p.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv(p.envKey, "relative-account")
			target, err := modelTarget(p.name, "")
			if err != nil {
				t.Fatalf("relative inherited account should not affect default: %v", err)
			}
			want := quotaTestAccountDir(t, filepath.Join(home, "."+p.name))
			if p.name == "claude" {
				if got, ok := envMap(target.Env)[p.envKey]; ok {
					t.Fatalf("%s = %q, want Claude builtin default", p.envKey, got)
				}
			} else if got := envMap(target.Env)[p.envKey]; got != want {
				t.Fatalf("%s = %q, want default %q", p.envKey, got, want)
			}
			_, err = modelTarget(p.name, "relative-account")
			if err == nil || !strings.Contains(err.Error(), "absolute "+p.envKey) {
				t.Fatalf("explicit relative account directory should fail before discovery: %v", err)
			}
			if os.Getenv(p.envKey) != "relative-account" {
				t.Fatal("validation changed the inherited environment")
			}
			explicit := t.TempDir()
			target, err = modelTarget(p.name, explicit)
			if err != nil || envMap(target.Env)[p.envKey] != explicit {
				t.Fatalf("relative inherited value blocked an explicit absolute account: %v", err)
			}
		})
	}
}
