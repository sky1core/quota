package agenthooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOwnsInstructionCommandAcceptsSameExecutableThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quota-cli-real")
	link := filepath.Join(dir, "quota-cli")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	command := ShellQuote([]string{link, "agent", "instructions", "_hook", "--agent=claude", "--event=WorktreeCreate"})
	if !OwnsInstructionCommand(command, target, "claude", "WorktreeCreate") {
		t.Fatal("symlinked executable was not treated as owned")
	}
}

func TestOwnsInstructionCommandRejectsDifferentExecutable(t *testing.T) {
	dir := t.TempDir()
	owned := filepath.Join(dir, "owned-quota-cli")
	other := filepath.Join(dir, "other-quota-cli")
	for _, path := range []string{owned, other} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	command := ShellQuote([]string{other, "agent", "instructions", "_hook", "--agent=claude", "--event=WorktreeCreate"})
	if OwnsInstructionCommand(command, owned, "claude", "WorktreeCreate") {
		t.Fatal("different executable was treated as owned")
	}
}

func TestOwnsInstructionCommandRejectsIndirectShellSyntax(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "quota-cli")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{
		`f() { ` + ShellQuote([]string{executable, "agent", "instructions", "_prepare", "--agent=codex", "--event=SessionStart"}) + `; }`,
		`env X=value ` + ShellQuote([]string{executable, "agent", "instructions", "_prepare", "--agent=codex", "--event=SessionStart"}),
		`sh -c ` + ShellQuote([]string{ShellQuote([]string{executable, "agent", "instructions", "_prepare", "--agent=codex", "--event=SessionStart"})}),
	} {
		if OwnsInstructionCommand(command, executable, "codex", "SessionStart") {
			t.Fatalf("indirect command was treated as owned: %s", command)
		}
	}
}

func TestOwnsInstructionCommandAcceptsLegacyOverlayCleanupCommand(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "quota-cli-real")
	link := filepath.Join(dir, "quota-cli")
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	command := ShellQuote([]string{link, "agent", "overlay", "hook", "--runtime=codex", "--event=SessionStart"})
	if !OwnsInstructionCommand(command, target, "codex", "SessionStart") {
		t.Fatal("legacy overlay cleanup command was not treated as owned")
	}
}

func TestSuspiciousInstructionCommandRecognizesLegacyOverlayCommand(t *testing.T) {
	command := "/other/quota-cli agent overlay hook --runtime=codex --event=SessionStart"
	if !SuspiciousInstructionCommand(command) {
		t.Fatal("unknown legacy overlay command was not suspicious")
	}
}
