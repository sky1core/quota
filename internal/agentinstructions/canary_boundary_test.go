package agentinstructions

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/childprocess"
)

func TestCanaryProcessFailureCannotPassWithSuccessOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, exit := range []string{"0", "7"} {
		cmd := childprocess.CommandContext(ctx, "/bin/sh", "-c", `printf '%s' '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"result":"marker"}'; exit "$1"`, "canary-process-test", exit)
		out, err := runCanaryCommand(ctx, cmd)
		if exit == "7" {
			if err == nil {
				t.Fatal("nonzero process status was accepted")
			}
		} else if err != nil || checkClaudeCanary(out, "marker", "positive") != nil {
			t.Fatalf("successful process was rejected: %v", err)
		}
	}
}

func TestClaudeCanaryPreservesNativeAccountEnvironment(t *testing.T) {
	want := []string{
		"PATH=/example/bin", "CLAUDE_CONFIG_DIR=/example/account",
		"ANTHROPIC_API_KEY=placeholder", "ANTHROPIC_BASE_URL=https://example.invalid",
		"CLAUDE_CODE_OAUTH_TOKEN=placeholder", "CODEX_HOME=/example/codex",
	}
	base := append(append([]string{}, want...), "CLAUDECODE=1")
	if got := claudeCanaryEnv(base); !reflect.DeepEqual(got, want) {
		t.Fatalf("native account environment changed: %v", got)
	}
}

func TestClaudeCanaryRequiresExplicitSuccess(t *testing.T) {
	for _, raw := range []string{
		`{"num_turns":1,"result":"marker"}`,
		`{"type":"result","subtype":"success","is_error":null,"num_turns":1,"result":"marker"}`,
		`{"type":"result","is_error":false,"subtype":"error_max_turns","num_turns":1,"result":"marker"}`,
	} {
		if err := checkClaudeCanary([]byte(raw), "marker", "positive"); err == nil {
			t.Errorf("accepted result without explicit success: %s", raw)
		}
	}
}

func TestCodexCanaryRejectsIncompleteOrAmbiguousEvents(t *testing.T) {
	prefix := "{\"type\":\"thread.started\"}\n{\"type\":\"turn.started\"}\n"
	message := "{\"type\":\"item.completed\",\"item\":{\"id\":\"message-1\",\"type\":\"agent_message\",\"text\":\"marker\"}}\n"
	completed := "{\"type\":\"turn.completed\"}\n"
	if err := checkCodexCanary([]byte(prefix+message+completed), "marker", "positive"); err != nil {
		t.Fatalf("valid control stream failed: %v", err)
	}
	for name, raw := range map[string]string{
		"unfinished_message":       prefix + "{\"type\":\"item.started\",\"item\":{\"id\":\"message-1\",\"type\":\"agent_message\",\"text\":\"marker\"}}\n" + completed,
		"empty_final":              prefix + message + "{\"type\":\"item.completed\",\"item\":{\"id\":\"message-2\",\"type\":\"agent_message\",\"text\":\"\"}}\n" + completed,
		"missing_item":             prefix + "{\"type\":\"item.completed\"}\n" + message + completed,
		"untyped_item":             prefix + "{\"type\":\"item.completed\",\"item\":{}}\n" + message + completed,
		"null_event":               prefix + "null\n" + message + completed,
		"unknown_event":            prefix + "{\"type\":\"future_tool_event\"}\n" + message + completed,
		"unfinished_second_turn":   prefix + message + completed + "{\"type\":\"turn.started\"}\n",
		"message_after_completion": prefix + completed + message,
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkCodexCanary([]byte(raw), "marker", "positive"); err == nil {
				t.Fatalf("accepted ambiguous stream: %s", raw)
			}
		})
	}
}

func TestCodexCanaryAcceptsCompletedStreamingMessage(t *testing.T) {
	events := []map[string]any{
		{"type": "thread.started", "thread_id": "thread-placeholder"},
		{"type": "turn.started"},
		{"type": "item.started", "item": map[string]any{"id": "message", "type": "agent_message", "text": ""}},
		{"type": "item.updated", "item": map[string]any{"id": "message", "type": "agent_message", "text": "mark"}},
		{"type": "item.completed", "item": map[string]any{"id": "message", "type": "agent_message", "text": "marker"}},
		{"type": "turn.completed"},
	}
	if err := checkCodexCanary(codexStream(t, events), "marker", "positive"); err != nil {
		t.Fatal(err)
	}
}
