package overlayruntime

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexLegacyHooksNeverInjectInstructionBodies(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared source sentinel\n")
	write(t, filepath.Join(repo, localRule), "private source sentinel\n")
	if code, out, errb := run(t, "", "setup", "--runtime=codex", repo); code != 0 {
		t.Fatalf("setup: %d %q %q", code, out, errb)
	}
	for _, source := range []string{"startup", "resume", "resume", "clear", "compact"} {
		input := `{"cwd":` + quote(repo) + `,"source":` + quote(source) + `}`
		for _, policy := range []string{"codex-session", "codex-subagent"} {
			event := "SessionStart"
			if policy == "codex-subagent" {
				event = "SubagentStart"
			}
			code, out, errb := run(t, input, "json", event, "x", "y", repo, policy)
			if code != 0 || out != "" {
				t.Fatalf("%s/%s appended context: %d %q %q", policy, source, code, out, errb)
			}
		}
	}
}

func TestCodexLegacyHookReportsStaleFileWithoutUpdatingIt(t *testing.T) {
	repo := newRepo(t)
	write(t, filepath.Join(repo, sharedRule), "shared source sentinel\n")
	write(t, filepath.Join(repo, localRule), "private source sentinel\n")
	if code, out, errb := run(t, "", "setup", "--runtime=codex", repo); code != 0 {
		t.Fatalf("setup: %d %q %q", code, out, errb)
	}
	before := regressionRead(t, filepath.Join(repo, codexRule))
	write(t, filepath.Join(repo, localRule), "changed private source sentinel\n")
	for _, source := range []string{"startup", "resume", "compact"} {
		code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"source":`+quote(source)+`}`, "json", "SessionStart", "x", "y", repo, "codex-session")
		if code != 0 || !strings.Contains(out, "stale") || strings.Contains(out, "source sentinel") {
			t.Fatalf("stale native instructions: %d %q %q", code, out, errb)
		}
		if got := regressionRead(t, filepath.Join(repo, codexRule)); got != before {
			t.Fatal("hook rewrote native instructions after the loader read them")
		}
	}
}

func TestCodexLegacyHookReportsConflictsWithoutPrivateBody(t *testing.T) {
	for _, condition := range []string{"tracked", "override"} {
		t.Run(condition, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, sharedRule), "# shared\n")
			write(t, filepath.Join(repo, localRule), "private source sentinel\n")
			if condition == "tracked" {
				git(t, repo, "add", "-f", localRule)
			} else {
				write(t, filepath.Join(repo, ".gitignore"), localRule+"\n"+codexRule+"\n")
				write(t, filepath.Join(repo, codexRule), "user override\n")
			}
			code, out, errb := run(t, `{"cwd":`+quote(repo)+`,"source":"resume"}`, "json", "SessionStart", "x", "y", repo, "codex-session")
			if code != 0 || !strings.Contains(out, condition) || strings.Contains(out+errb, "private source sentinel") {
				t.Fatalf("conflict handling: %d %q %q", code, out, errb)
			}
		})
	}
}
