package overlayruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexNativeFilesDoNotRequireHooks(t *testing.T) {
	for _, tc := range []struct {
		name, user, project, nested string
		withoutTrust, sharedOnly    bool
		want                        int
	}{
		{name: "project disables hooks", user: "hooks = true", project: "hooks = false"},
		{name: "nested disables hooks", project: "hooks = true", nested: "hooks = false"},
		{name: "deprecated alias", project: "codex_hooks = false"},
		{name: "user disables hooks", user: "hooks = false"},
		{name: "project enables hooks", user: "hooks = false", project: "hooks = true"},
		{name: "canonical beats alias", user: "codex_hooks = false", project: "hooks = true"},
		{name: "alias cannot override canonical", user: "hooks = true", project: "codex_hooks = false"},
		{name: "invalid boolean", project: "hooks = 'false'", want: 1},
		{name: "invalid ignored alias", user: "hooks = true", project: "codex_hooks = 'false'", want: 1},
		{name: "unrelated feature", project: "shell_tool = false"},
		{name: "project without trust ignored", project: "hooks = false", withoutTrust: true},
		{name: "shared rules need no hook", project: "hooks = false", sharedOnly: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "AGENTS.md"), "# shared\n")
			if !tc.sharedOnly {
				write(t, filepath.Join(repo, "AGENTS.local.md"), "private rule\n")
			}
			if code, out, errb := run(t, "", "setup", "--runtime=codex", repo); code != 0 {
				t.Fatalf("setup: exit=%d out=%q err=%q", code, out, errb)
			}
			user := "[features]\n" + tc.user + "\n"
			if !tc.withoutTrust {
				user += fmt.Sprintf("[projects.%q]\ntrust_level = 'trusted'\n", repo)
			}
			write(t, filepath.Join(os.Getenv("CODEX_HOME"), "config.toml"), user)
			dir := repo
			for _, layer := range []struct{ directory, content string }{{repo, tc.project}, {filepath.Join(repo, "child"), tc.nested}} {
				if layer.content == "" {
					continue
				}
				configDir := filepath.Join(layer.directory, ".codex")
				if err := os.MkdirAll(configDir, 0o700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(configDir, "config.toml"), "[features]\n"+layer.content+"\n")
				dir = layer.directory
			}
			for _, operation := range []string{"check", "setup"} {
				code, out, errb := run(t, "", operation, "--runtime=codex", dir)
				if code != tc.want || (tc.want != 0 && !strings.Contains(out+errb, "features.")) {
					t.Fatalf("%s: exit=%d want=%d out=%q err=%q", operation, code, tc.want, out, errb)
				}
			}
		})
	}
}
