package overlayruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedInstructionOutput struct{}

func (failedInstructionOutput) Write([]byte) (int, error) {
	return 0, errors.New("output unavailable")
}

func TestResumeDeliversOnlyChangedLocalInstructionsPerSession(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			testHome(t)
			repo := newRepo(t)
			path := filepath.Join(repo, localInstructions)
			invoke := func(session, source string) string {
				t.Helper()
				code, out, stderr := hook(t, agent, map[string]any{"cwd": repo, "source": source, "session_id": session})
				if code != 0 || stderr != "" {
					t.Fatalf("%s/%s: %d %s", session, source, code, stderr)
				}
				return additionalContext(t, out)
			}
			write(t, path, "original instruction\n")
			for _, session := range []string{"first", "second"} {
				if got := invoke(session, "startup"); got != "AGENTS.local.md\noriginal instruction\n" {
					t.Fatal(got)
				}
			}
			if got := invoke("first", "resume"); got != "" {
				t.Fatalf("unchanged resume repeated instructions: %q", got)
			}
			write(t, path, "updated instruction\n")
			for _, session := range []string{"second", "first"} {
				if got := invoke(session, "resume"); got != "AGENTS.local.md\nupdated instruction\n" {
					t.Fatalf("%s missed updated instructions: %q", session, got)
				}
				if got := invoke(session, "resume"); got != "" {
					t.Fatalf("%s repeated updated instructions: %q", session, got)
				}
			}
			write(t, path, "")
			if got := invoke("first", "resume"); !strings.Contains(got, "empty") || !strings.Contains(got, "no longer apply") {
				t.Fatal(got)
			}
			if got := invoke("first", "resume"); got != "" {
				t.Fatal(got)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if got := invoke("second", "resume"); !strings.Contains(got, "absent") || !strings.Contains(got, "no longer apply") {
				t.Fatal(got)
			}
			if got := invoke("second", "resume"); got != "" {
				t.Fatal(got)
			}
			write(t, path, "restored instruction\n")
			if got := invoke("first", "resume"); got != "AGENTS.local.md\nrestored instruction\n" {
				t.Fatal(got)
			}
		})
	}
}

func TestResumeWithoutDeliveryRecordRecordsCurrentStateWithoutRedelivering(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			testHome(t)
			repo := newRepo(t)
			path := filepath.Join(repo, localInstructions)
			transcript := filepath.Join(t.TempDir(), "existing-session.jsonl")
			var record any
			if agent == "claude" {
				record = map[string]any{"type": "attachment", "sessionId": "existing-session", "attachment": map[string]any{"type": "hook_additional_context", "hookEvent": "SessionStart", "content": []string{"AGENTS.local.md\nolder instruction\n"}}}
			} else {
				record = map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "developer", "content": []any{map[string]string{"type": "input_text", "text": "AGENTS.local.md\nolder instruction\n"}}}}
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			write(t, transcript, string(data)+"\n")
			input := map[string]any{"cwd": repo, "source": "resume", "session_id": "existing-session", "transcript_path": transcript}
			write(t, path, "current instruction\n")
			if code, out, stderr := hook(t, agent, input); code != 0 || stderr != "" || out != "" {
				t.Fatalf("resume without a delivery record redelivered: %d %q %s", code, out, stderr)
			}
			if code, out, stderr := hook(t, agent, input); code != 0 || stderr != "" || out != "" {
				t.Fatalf("recorded state was not reused: %d %q %s", code, out, stderr)
			}
			write(t, path, "updated instruction\n")
			code, out, stderr := hook(t, agent, input)
			if code != 0 || stderr != "" || additionalContext(t, out) != "AGENTS.local.md\nupdated instruction\n" {
				t.Fatalf("change after the recorded state was not delivered: %d %q %s", code, out, stderr)
			}
		})
	}
}

func TestResumeDoesNotRecordFailedDelivery(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	path := filepath.Join(repo, localInstructions)
	input := map[string]any{"cwd": repo, "source": "resume", "session_id": "existing-session"}
	write(t, path, "current instruction\n")
	if code, out, stderr := hook(t, "claude", input); code != 0 || stderr != "" || out != "" {
		t.Fatalf("no-record resume: %d %q %s", code, out, stderr)
	}
	write(t, path, strings.Repeat("x", 10001))
	for i := 0; i < 2; i++ {
		code, out, stderr := hook(t, "claude", input)
		if code != 0 || stderr != "" || !strings.Contains(additionalContext(t, out), "exceeds") {
			t.Fatalf("failed delivery was treated as current: %d %q %s", code, out, stderr)
		}
	}
	write(t, path, "fixed instruction\n")
	code, out, stderr := hook(t, "claude", input)
	if code != 0 || stderr != "" || additionalContext(t, out) != "AGENTS.local.md\nfixed instruction\n" {
		t.Fatalf("fixed delivery: %d %q %s", code, out, stderr)
	}
	write(t, path, "instruction after failed output\n")
	encoded, _ := json.Marshal(input)
	var failure bytes.Buffer
	if code := RunSessionStartHook(context.Background(), "claude", bytes.NewReader(encoded), failedInstructionOutput{}, &failure); code == 0 {
		t.Fatal("failed output reported success")
	}
	code, out, stderr = hook(t, "claude", input)
	if code != 0 || stderr != "" || additionalContext(t, out) != "AGENTS.local.md\ninstruction after failed output\n" {
		t.Fatalf("failed output was recorded as delivered: %d %q %s", code, out, stderr)
	}
}

func TestResumeKeepsAccountTranscriptsIndependent(t *testing.T) {
	testHome(t)
	repo := newRepo(t)
	path := filepath.Join(repo, localInstructions)
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			write(t, path, "shared original "+agent+"\n")
			first := map[string]any{"cwd": repo, "source": "startup", "session_id": "same-explicit-id", "transcript_path": "/account-one/session.jsonl"}
			second := map[string]any{"cwd": repo, "source": "resume", "session_id": "same-explicit-id", "transcript_path": "/account-two/session.jsonl"}
			if code, out, stderr := hook(t, agent, first); code != 0 || stderr != "" || additionalContext(t, out) != "AGENTS.local.md\nshared original "+agent+"\n" {
				t.Fatalf("startup: %d %q %s", code, out, stderr)
			}
			if code, out, stderr := hook(t, agent, second); code != 0 || stderr != "" || out != "" {
				t.Fatalf("other account's record was reused: %d %q %s", code, out, stderr)
			}
			write(t, path, "shared updated "+agent+"\n")
			first["source"] = "resume"
			for _, input := range []map[string]any{first, second} {
				if code, out, stderr := hook(t, agent, input); code != 0 || stderr != "" || additionalContext(t, out) != "AGENTS.local.md\nshared updated "+agent+"\n" {
					t.Fatalf("%v: independent record missed the change: %d %q %s", input["transcript_path"], code, out, stderr)
				}
			}
		})
	}
}
