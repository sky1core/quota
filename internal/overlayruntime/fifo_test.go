package overlayruntime

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRuntimeRejectsFIFOWithoutWaitingForWriter(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "AGENTS.local.md", "CLAUDE.md", "codex-config", "codex-config-link", "claude-config", "claude-config-link"} {
		t.Run(name, func(t *testing.T) {
			repo := newRepo(t)
			write(t, filepath.Join(repo, "AGENTS.md"), "# shared rules\n")
			write(t, filepath.Join(repo, "CLAUDE.md"), "@AGENTS.md\n")
			path := filepath.Join(repo, name)
			runtime := "claude"
			if strings.HasPrefix(name, "codex-config") {
				path = filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
				runtime = "codex"
			}
			if strings.HasPrefix(name, "claude-config") {
				path = filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json")
			}
			if strings.HasSuffix(name, "-link") {
				configPath := path
				path = filepath.Join(t.TempDir(), "config-pipe")
				if err := os.Symlink(path, configPath); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			for _, operation := range []string{"check", "setup"} {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				var out, errb bytes.Buffer
				code := Run(ctx, []string{operation, "--runtime=" + runtime, repo}, strings.NewReader(""), &out, &errb)
				ctxErr := ctx.Err()
				cancel()
				if ctxErr != nil {
					t.Fatalf("%s waited for FIFO writer: %v", operation, ctxErr)
				}
				if code != 1 || !strings.Contains(out.String()+errb.String(), "not a regular file") {
					t.Fatalf("%s: exit=%d out=%q err=%q", operation, code, out.String(), errb.String())
				}
				info, err := os.Lstat(path)
				if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
					t.Fatalf("%s changed FIFO: info=%v err=%v", operation, info, err)
				}
			}
		})
	}
}
