package overlayruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRepositoryReportsSourceAndNativeBlockers(t *testing.T) {
	home := testHome(t)
	repo := newRepo(t)
	configDir := filepath.Join(home, ".claude")
	write(t, filepath.Join(configDir, "CLAUDE.md"), "global placeholder\n")
	options := CheckOptions{ClaudeConfigDir: configDir}
	status, err := CheckRepository(context.Background(), repo, "claude", options)
	if err != nil || status.LocalPresent || len(status.Problems) != 0 || len(status.Warnings) != 0 || status.Primary != repo || status.LocalInstructions != filepath.Join(repo, "AGENTS.local.md") {
		t.Fatalf("empty repository status = %+v %v", status, err)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), placeholderBody(1400))
	status, err = CheckRepository(context.Background(), repo, "claude", options)
	if err != nil || !status.LocalPresent || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "hook limit") {
		t.Fatalf("status = %+v %v", status, err)
	}
	if codex, err := CheckRepository(context.Background(), repo, "codex", CheckOptions{}); err != nil || !codex.LocalPresent || len(codex.Warnings) != 0 {
		t.Fatalf("codex status = %+v %v", codex, err)
	}
	write(t, filepath.Join(repo, "AGENTS.local.md"), placeholderBody(3200))
	status, err = CheckRepository(context.Background(), repo, "claude", options)
	if err != nil || !status.LocalPresent || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "hook limit") {
		t.Fatalf("large source status = %+v %v", status, err)
	}
	if err := os.Remove(filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(repo, "AGENTS.local.md")); err != nil {
		t.Fatal(err)
	}
	status, err = CheckRepository(context.Background(), repo, "claude", options)
	if err != nil || !status.LocalPresent || len(status.Problems) != 1 || !strings.Contains(status.Problems[0], "symlink") {
		t.Fatalf("symlink status = %+v %v", status, err)
	}
	write(t, filepath.Join(repo, "sub", "CLAUDE.md"), "nested\n")
	write(t, filepath.Join(repo, "CLAUDE.local.md"), "local\n")
	write(t, filepath.Join(filepath.Dir(repo), ".claude", "CLAUDE.md"), "other account\n")
	status, err = CheckRepository(context.Background(), filepath.Join(repo, "sub"), "claude", options)
	if err != nil {
		t.Fatal(err)
	}
	warnings := strings.Join(status.Warnings, "\n")
	for _, want := range []string{filepath.Join(repo, "sub", "CLAUDE.md"), filepath.Join(repo, "CLAUDE.local.md"), filepath.Join(filepath.Dir(repo), ".claude", "CLAUDE.md")} {
		if !strings.Contains(warnings, "native AGENTS.md reading is blocked by "+want) {
			t.Fatalf("missing warning for %s: %s", want, warnings)
		}
	}
	if strings.Contains(warnings, filepath.Join(configDir, "CLAUDE.md")) {
		t.Fatalf("account global CLAUDE.md reported: %s", warnings)
	}
	if codex, err := CheckRepository(context.Background(), filepath.Join(repo, "sub"), "codex", CheckOptions{}); err != nil || len(codex.Warnings) != 0 {
		t.Fatalf("Claude blockers reported for Codex: %+v %v", codex, err)
	}
	for mode, want := range map[string]string{"claude-md": "claude-md does not read AGENTS.md", "managed-only": "managed-only drops", "claude-md-and-agents-md": ""} {
		write(t, filepath.Join(configDir, "settings.json"), `{"pluginConfigs":{"agents-md@builtin":{"options":{"instructionFiles":"`+mode+`"}}}}`)
		status, err = CheckRepository(context.Background(), repo, "claude", options)
		if err != nil {
			t.Fatal(err)
		}
		found := strings.Contains(strings.Join(status.Warnings, "\n"), want)
		if (want == "") != !strings.Contains(strings.Join(status.Warnings, "\n"), "project instruction mode") || !found {
			t.Fatalf("mode %s warnings = %v", mode, status.Warnings)
		}
	}
	write(t, filepath.Join(configDir, "settings.json"), "{invalid")
	status, err = CheckRepository(context.Background(), repo, "claude", options)
	if err != nil || len(status.Problems) != 2 || !strings.Contains(status.Problems[1], "settings.json") {
		t.Fatalf("invalid settings status = %+v %v", status, err)
	}
	if _, err := CheckRepository(context.Background(), t.TempDir(), "claude", options); err == nil || !strings.Contains(err.Error(), "not inside a Git worktree") {
		t.Fatalf("outside repository error = %v", err)
	}
}

func TestCheckRepositoryWarnsAboutPreviousGeneratedFiles(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	write(t, filepath.Join(repo, "AGENTS.override.md"), "merged\n")
	record := recordLegacy(t, repo, map[string]string{"AGENTS.override.md": "merged\n", ".claude/rules/quota-instructions.md": "gone\n"})
	status, err := CheckRepository(context.Background(), repo, "codex", CheckOptions{})
	if err != nil || len(status.Problems) != 0 || len(status.Warnings) != 1 {
		t.Fatalf("status = %+v %v", status, err)
	}
	if w := status.Warnings[0]; !strings.Contains(w, record) || !strings.Contains(w, filepath.Join(repo, "AGENTS.override.md")) || strings.Contains(w, "quota-instructions.md") {
		t.Fatalf("warning = %q", w)
	}
	if exists(filepath.Join(repo, "AGENTS.override.md")) != true || !exists(record) {
		t.Fatal("status changed files")
	}
}

func TestCheckRepositoryReportsClaudeHookLimitBoundary(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	for _, tc := range []struct {
		name string
		body string
		over bool
	}{
		{"below", strings.Repeat("x", 9983), false},
		{"at", strings.Repeat("x", 9984), false},
		{"above", strings.Repeat("x", 9985), true},
		{"unicode-at", strings.Repeat("😀", 4992), false},
		{"unicode-above", strings.Repeat("😀", 4993), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			write(t, filepath.Join(repo, "AGENTS.local.md"), tc.body)
			for _, agent := range []string{"claude", "codex"} {
				status, err := CheckRepository(context.Background(), repo, agent, CheckOptions{})
				want := agent == "claude" && tc.over
				if err != nil || (len(status.Problems) != 0) != want {
					t.Fatalf("%s status: %+v %v", agent, status, err)
				}
			}
		})
	}
}
