package agentinstructions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanaryGitInitRefusesForeignGitDirectory(t *testing.T) {
	foreign := t.TempDir()
	cmd := exec.Command("git", "init", "-q", foreign)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create foreign repository: %v\n%s", err, out)
	}
	config := filepath.Join(foreign, ".git", "config")
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(foreign, ".git"))
	if err := gitInit(context.Background(), target); err == nil || !strings.Contains(err.Error(), "GIT_DIR") {
		t.Fatalf("foreign Git directory accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".git")); !os.IsNotExist(err) {
		t.Fatalf("canary initialized despite refused environment: %v", err)
	}
	after, err := os.ReadFile(config)
	if err != nil || string(after) != string(before) {
		t.Fatalf("foreign repository changed: %v", err)
	}
}
