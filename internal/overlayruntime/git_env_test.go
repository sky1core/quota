package overlayruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstructionHooksAllowGeneralGitConfig(t *testing.T) {
	for _, kind := range []string{"empty", "authentication", "parameters", "include"} {
		t.Run(kind, func(t *testing.T) {
			testHome(t)
			repo := newRepo(t)
			write(t, filepath.Join(repo, "AGENTS.local.md"), "Synthetic local instructions.\n")
			switch kind {
			case "empty":
				t.Setenv("GIT_CONFIG_COUNT", "0")
			case "authentication":
				t.Setenv("GIT_CONFIG_COUNT", "3")
				for i, pair := range [][2]string{{"credential.interactive", "false"}, {"http.extraHeader", "X-Synthetic: test"}, {"safe.directory", repo}} {
					t.Setenv(fmt.Sprintf("GIT_CONFIG_KEY_%d", i), pair[0])
					t.Setenv(fmt.Sprintf("GIT_CONFIG_VALUE_%d", i), pair[1])
				}
			case "parameters":
				t.Setenv("GIT_CONFIG_PARAMETERS", "'credential.interactive=false'")
			case "include":
				file := filepath.Join(t.TempDir(), "authentication.config")
				write(t, file, "[credential]\ninteractive = false\n")
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", "include.path")
				t.Setenv("GIT_CONFIG_VALUE_0", file)
			}
			for _, agent := range []string{"claude", "codex"} {
				code, out, stderr := hook(t, agent, map[string]any{"cwd": repo, "source": "startup"})
				if code != 0 || !strings.Contains(out, "Synthetic local instructions.") || stderr != "" {
					t.Errorf("%s: code=%d stdout=%s stderr=%s", agent, code, out, stderr)
				}
				status, err := CheckRepository(context.Background(), repo, agent, CheckOptions{})
				if err != nil || status.Primary != repo || !status.LocalPresent {
					t.Errorf("%s status: %+v error=%v", agent, status, err)
				}
			}
			if reason := promptHookContext(t, promptInput(repo)); reason != "" {
				t.Errorf("general Git config blocked prompt: %s", reason)
			}
		})
	}
}

func TestInstructionHooksRejectRepositoryConfigOverrides(t *testing.T) {
	for _, kind := range []string{"worktree", "bare", "format", "extensions", "parameters", "include", "conditional-include", "malformed", "malformed-include"} {
		t.Run(kind, func(t *testing.T) {
			testHome(t)
			repo, other := newRepo(t), newRepo(t)
			for _, dir := range []string{repo, other} {
				write(t, filepath.Join(dir, "AGENTS.local.md"), "Preserve local instructions.\n")
			}
			t.Chdir(other)
			key, value := "Core.WorkTree", other
			switch kind {
			case "bare":
				key, value = "core.bare", "true"
			case "format":
				key, value = "core.repositoryFormatVersion", "1"
			case "extensions":
				key, value = "extensions.worktreeConfig", "true"
			case "parameters":
				t.Setenv("GIT_CONFIG_PARAMETERS", "'core.worktree="+other+"'")
			case "include", "conditional-include", "malformed-include":
				file := filepath.Join(t.TempDir(), "repository.config")
				write(t, file, "[core]\nworktree = "+other+"\n")
				key, value = "include.path", file
				if kind == "conditional-include" {
					key = "includeIf.gitdir:" + filepath.Join(repo, ".git") + ".path"
				}
				if kind == "malformed-include" {
					write(t, file, "[invalid section\n")
				}
			}
			if kind != "parameters" {
				t.Setenv("GIT_CONFIG_COUNT", "1")
				t.Setenv("GIT_CONFIG_KEY_0", key)
				t.Setenv("GIT_CONFIG_VALUE_0", value)
			}
			if kind == "malformed" {
				t.Setenv("GIT_CONFIG_COUNT", "invalid")
			}
			for _, agent := range []string{"claude", "codex"} {
				code, out, stderr := hook(t, agent, map[string]any{"cwd": repo, "source": "startup"})
				if code == 0 || out != "" || stderr == "" {
					t.Errorf("%s override accepted: code=%d stdout=%s stderr=%s", agent, code, out, stderr)
				}
				if _, err := CheckRepository(context.Background(), repo, agent, CheckOptions{}); err == nil {
					t.Errorf("%s status accepted override", agent)
				}
			}
			reason := promptHookContext(t, promptInput(repo))
			if !strings.Contains(reason, "instruction check failed") {
				t.Errorf("missing model-facing failure: %s", reason)
			}
			if kind == "malformed-include" && !strings.Contains(reason, "bad config line") {
				t.Errorf("configuration error cause lost: %s", reason)
			}
			for _, dir := range []string{repo, other} {
				body, err := os.ReadFile(filepath.Join(dir, "AGENTS.local.md"))
				if err != nil || string(body) != "Preserve local instructions.\n" {
					t.Errorf("instructions changed in %s: %q %v", dir, body, err)
				}
			}
		})
	}
}
