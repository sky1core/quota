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
	command = ShellQuote([]string{link, "agent", "instructions", "_prepare", "--agent=claude", "--event=WorktreeCreate", "--claude-config-dir", filepath.Join(dir, ".claude")})
	if !OwnsInstructionCommand(command, target, "claude", "WorktreeCreate") {
		t.Fatal("owned command with explicit account directory was not treated as owned")
	}
}

func TestOwnsInstructionCommandRecognizesSessionStartParts(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "quota-cli")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	prepare := func(extra ...string) string {
		return ShellQuote(append([]string{executable, "agent", "instructions", "_prepare", "--agent=claude", "--event=SessionStart"}, extra...))
	}
	for _, command := range []string{prepare("--part=1"), prepare("--part=8"), prepare()} {
		if !OwnsInstructionCommand(command, executable, "claude", "SessionStart") {
			t.Fatalf("session start part command was not owned: %s", command)
		}
	}
	for _, command := range []string{prepare("--part=0"), prepare("--part=x"), prepare("--part"), prepare("--part=1", "extra")} {
		if OwnsInstructionCommand(command, executable, "claude", "SessionStart") {
			t.Fatalf("malformed part command was owned: %s", command)
		}
	}
	if OwnsInstructionCommand(prepare("--part=1"), executable, "codex", "SessionStart") {
		t.Fatal("Claude command was owned for Codex")
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

func TestInstructionCommandDistinguishesCurrentPreviousAndLegacyForms(t *testing.T) {
	dir := t.TempDir()
	executable := filepath.Join(dir, "current", "quota-cli")
	previous := filepath.Join(dir, "previous", "quota-cli")
	removed := filepath.Join(dir, "removed", "quota-cli")
	renamed := filepath.Join(dir, "current", "quota-cli-dev")
	for _, path := range []string{executable, previous, renamed} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "stable", "quota-cli")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(executable, link); err != nil {
		t.Fatal(err)
	}
	current := func(path string) string {
		return ShellQuote([]string{path, "agent", "instructions", "_prepare", "--agent=codex", "--event=SessionStart"})
	}
	for _, tc := range []struct {
		command string
		want    InstructionCommandState
	}{
		{current(executable), InstructionCurrent},
		{current(link), InstructionCurrent},
		{current(previous), InstructionOtherExecutable},
		{current(removed), InstructionOtherExecutable},
		{current(renamed), InstructionNotManaged},
		{current("quota-cli"), InstructionNotManaged},
		{ShellQuote([]string{executable, "agent", "instructions", "_hook", "--agent=codex", "--event=SessionStart"}), InstructionLegacy},
		{ShellQuote([]string{previous, "agent", "instructions", "_prepare", "--agent=codex", "--event=SessionStart", "--codex-home", dir}), InstructionLegacy},
		{ShellQuote([]string{previous, "agent", "overlay", "hook", "--runtime=codex", "--event=SessionStart"}), InstructionLegacy},
		{`sh "$HOME/.local/bin/agents-overlay-context" json SessionStart AGENTS.md - . codex-session`, InstructionLegacy},
		{current(executable), InstructionCurrent},
	} {
		if got := InstructionCommand(tc.command, executable, "codex", "SessionStart"); got != tc.want {
			t.Errorf("%s: state %d, want %d", tc.command, got, tc.want)
		}
	}
	if got := InstructionCommand(current(executable), link, "codex", "SessionStart"); got != InstructionCurrent {
		t.Errorf("real path command inspected through the link: state %d", got)
	}
	if got := InstructionCommand(current(executable), executable, "claude", "SessionStart"); got != InstructionNotManaged {
		t.Errorf("Codex command owned for Claude: state %d", got)
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
