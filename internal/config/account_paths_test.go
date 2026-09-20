package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCanonicalAccountDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, alias)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{real, alias, "~/alias", relative} {
		got, err := CanonicalAccountDirectory(input)
		if err != nil || got != want {
			t.Fatalf("%q = %q, %v; want %q", input, got, err, want)
		}
		got, err = CanonicalAccountDirectory(filepath.Join(input, "missing", "child"))
		if err != nil || got != filepath.Join(want, "missing", "child") {
			t.Fatalf("missing suffix: %q, %v", got, err)
		}
	}
	dangling := filepath.Join(root, "dangling")
	if err := os.Symlink("real/missing", dangling); err != nil {
		t.Fatal(err)
	}
	got, err := CanonicalAccountDirectory(filepath.Join(dangling, "child"))
	if err != nil || got != filepath.Join(want, "missing", "child") {
		t.Fatalf("dangling alias: %q, %v", got, err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(root, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"", " ", "bad\x00dir", file, filepath.Join(file, "child"), loop} {
		if got, err := CanonicalAccountDirectory(input); err == nil {
			t.Errorf("invalid path %q accepted as %q", input, got)
		}
	}
}

func TestResolveAccountDirectoryConflicts(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, kind := range []string{"default", "extras", "dangling alias", "key", "key and directory", "invalid path"} {
			t.Run(provider+"/"+kind, func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("HOME", root)
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				t.Setenv("CODEX_HOME", "")
				base := filepath.Join(root, "."+provider)
				if err := os.MkdirAll(base, 0700); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(base, alias); err != nil {
					t.Fatal(err)
				}
				entries := []accountDirectory{{provider + "-2", alias}}
				want := []string{provider + "-9"}
				skipCount := 2
				switch kind {
				case "extras":
					entries = []accountDirectory{{provider + "-2", filepath.Join(base, "missing")}, {provider + "-3", filepath.Join(alias, "missing")}}
					want = append([]string{provider}, want...)
				case "dangling alias":
					dangling := filepath.Join(root, "dangling")
					if err := os.Symlink(filepath.Join(base, "missing"), dangling); err != nil {
						t.Fatal(err)
					}
					entries = []accountDirectory{{provider + "-2", filepath.Join(base, "missing")}, {provider + "-3", dangling}}
					want = append([]string{provider}, want...)
				case "key", "key and directory":
					entries = []accountDirectory{{provider + "-2", filepath.Join(root, "a")}, {provider + "-2", filepath.Join(root, "b")}}
					if kind == "key and directory" {
						entries = append(entries, accountDirectory{provider + "-3", filepath.Join(root, "b")})
						skipCount = 3
					}
					want = append([]string{provider}, want...)
				case "invalid path":
					loop := filepath.Join(root, "loop")
					if err := os.Symlink(loop, loop); err != nil {
						t.Fatal(err)
					}
					entries = []accountDirectory{{provider + "-2", loop}}
					want = append([]string{provider}, want...)
					skipCount = 1
				}
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(cwd, filepath.Join(root, "valid"))
				if err != nil {
					t.Fatal(err)
				}
				entries = append(entries, accountDirectory{provider + "-9", relative})
				cfg := Config{}
				for _, entry := range entries {
					if provider == "claude" {
						cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, ClaudeAccount{entry.key, entry.dir})
					} else {
						cfg.CodexAccounts = append(cfg.CodexAccounts, CodexAccount{entry.key, entry.dir})
					}
				}
				var keys, skipped []string
				if provider == "claude" {
					accounts, diagnostics := cfg.ResolveAccounts()
					skipped = diagnostics
					for _, a := range accounts {
						keys = append(keys, a.Key)
						if a.Key != provider && !filepath.IsAbs(a.ConfigDir) {
							t.Fatal(a)
						}
					}
					others, errs := cfg.ResolveCodexAccounts()
					if len(others) != 1 || len(errs) != 0 {
						t.Fatalf("unrelated provider lost: %v %v", others, errs)
					}
				} else {
					accounts, diagnostics := cfg.ResolveCodexAccounts()
					skipped = diagnostics
					for _, a := range accounts {
						keys = append(keys, a.Key)
						if a.Key != provider && !filepath.IsAbs(a.Home) {
							t.Fatal(a)
						}
					}
					others, errs := cfg.ResolveAccounts()
					if len(others) != 1 || len(errs) != 0 {
						t.Fatalf("unrelated provider lost: %v %v", others, errs)
					}
				}
				if !reflect.DeepEqual(keys, want) || len(skipped) != skipCount {
					t.Fatalf("accounts %v, diagnostics %v; want %v, %d skipped", keys, skipped, want, skipCount)
				}
				if kind != "invalid path" && !strings.Contains(strings.Join(skipped, " "), "ambiguous") {
					t.Fatal(skipped)
				}
			})
		}
	}
}

func TestResolveDefaultDirectoryIgnoresProviderEnvironment(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("HOME", root)
			real := filepath.Join(root, "real")
			if err := os.Mkdir(real, 0700); err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(root, "alias")
			if err := os.Symlink(real, alias); err != nil {
				t.Fatal(err)
			}
			env := "CLAUDE_CONFIG_DIR"
			pattern := ClaudeExtraKeyRe
			if provider == "codex" {
				env, pattern = "CODEX_HOME", CodexExtraKeyRe
			}
			t.Setenv(env, alias)
			accounts, skipped := resolveAccountDirectories(provider, nil, pattern)
			want, err := CanonicalAccountDirectory(filepath.Join(root, "."+provider))
			if err != nil {
				t.Fatal(err)
			}
			if len(skipped) != 0 || len(accounts) != 1 || accounts[0].dir != want {
				t.Fatalf("default directory follows provider env: %v %v; want %s", accounts, skipped, want)
			}
			loop := filepath.Join(root, "loop")
			if err := os.Symlink(loop, loop); err != nil {
				t.Fatal(err)
			}
			t.Setenv(env, loop)
			accounts, skipped = resolveAccountDirectories(provider, []accountDirectory{{provider + "-2", real}}, pattern)
			if len(skipped) != 0 || len(accounts) != 2 || accounts[0].key != provider || accounts[1].key != provider+"-2" {
				t.Fatalf("provider env changed resolved accounts: %v %v", accounts, skipped)
			}
		})
	}
}

func TestAccountPathsResolveSymlinksBeforeParentTraversal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	for _, sub := range []string{"real/sub", "real/account", "account"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("real/sub", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Join(root, "real/account"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, path := range []string{root + "/alias/../account", "alias/../account", "~/alias/../account"} {
		got, err := CanonicalAccountDirectory(path)
		if err != nil || got != want {
			t.Fatalf("%q resolved %q, %v; want %q", path, got, err, want)
		}
		missing, err := CanonicalAccountDirectory(path + "/missing")
		if err != nil || missing != filepath.Join(want, "missing") {
			t.Fatalf("missing: %q %v", missing, err)
		}
		cfg := Config{ClaudeAccounts: []ClaudeAccount{{Key: "claude-2", ConfigDir: path}}}
		accounts, skipped := cfg.ResolveAccounts()
		if len(skipped) != 0 || len(accounts) != 2 || accounts[1].ConfigDir != want {
			t.Fatalf("wrong resolved account: %v %v", accounts, skipped)
		}
		cfg.ClaudeAccounts = append(cfg.ClaudeAccounts, ClaudeAccount{Key: "claude-3", ConfigDir: want})
		accounts, skipped = cfg.ResolveAccounts()
		if len(accounts) != 1 || len(skipped) != 2 {
			t.Fatalf("duplicate actual directory retained: %v %v", accounts, skipped)
		}
	}
}

func TestAccountPathDanglingParentCycleTerminates(t *testing.T) {
	if os.Getenv("QUOTA_TEST_PATH_CYCLE") == "1" {
		root := os.Getenv("HOME")
		if _, err := CanonicalAccountDirectory(filepath.Join(root, "a")); err == nil {
			t.Fatal("accepted broken traversal")
		}
		cfg := Config{ClaudeAccounts: []ClaudeAccount{{Key: "claude-2", ConfigDir: filepath.Join(root, "a")}, {Key: "claude-3", ConfigDir: filepath.Join(root, "valid")}}}
		accounts, skipped := cfg.ResolveAccounts()
		if len(accounts) != 2 || len(skipped) != 1 || accounts[1].Key != "claude-3" {
			t.Fatalf("independent account lost: %v %v", accounts, skipped)
		}
		return
	}
	root := t.TempDir()
	if err := os.Symlink("missing/../a", filepath.Join(root, "a")); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestAccountPathDanglingParentCycleTerminates$")
	cmd.Env = append(os.Environ(), "QUOTA_TEST_PATH_CYCLE=1", "HOME="+root, "CLAUDE_CONFIG_DIR=")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("path resolution did not finish successfully: %v %s", err, out)
	}
}
