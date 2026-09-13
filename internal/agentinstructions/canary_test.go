package agentinstructions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func claudeJSON(t *testing.T, isError bool, numTurns int, result string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"type":      "result",
		"subtype":   "success",
		"is_error":  isError,
		"num_turns": numTurns,
		"result":    result,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func TestCheckClaudeCanaryPositive(t *testing.T) {
	want := joinMarkers("aaaa", "bbbb")
	raw := claudeJSON(t, false, 1, "aaaa bbbb")
	if err := checkClaudeCanary(raw, want, "positive"); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestCheckClaudeCanaryPositiveWhitespaceTolerant(t *testing.T) {
	want := joinMarkers("aaaa", "bbbb")
	raw := claudeJSON(t, false, 1, "\n  aaaa   bbbb \n")
	if err := checkClaudeCanary(raw, want, "positive"); err != nil {
		t.Fatalf("expected whitespace-normalized pass, got %v", err)
	}
}

func TestCheckClaudeCanaryNegativeNone(t *testing.T) {
	raw := claudeJSON(t, false, 1, "NONE")
	if err := checkClaudeCanary(raw, canaryNone, "negative control"); err != nil {
		t.Fatalf("expected NONE pass, got %v", err)
	}
}

func TestCheckClaudeCanaryRejects(t *testing.T) {
	want := joinMarkers("aaaa", "bbbb")
	cases := []struct {
		name string
		raw  []byte
	}{
		{"is_error", claudeJSON(t, true, 1, "aaaa bbbb")},
		{"multi_turn", claudeJSON(t, false, 2, "aaaa bbbb")},
		{"zero_turn", claudeJSON(t, false, 0, "aaaa bbbb")},
		{"wrong_result", claudeJSON(t, false, 1, "aaaa cccc")},
		{"only_shared", claudeJSON(t, false, 1, "aaaa")},
		{"duplicate_shared", claudeJSON(t, false, 1, "aaaa aaaa bbbb")},
		{"duplicate_local", claudeJSON(t, false, 1, "aaaa bbbb bbbb")},
		{"none_when_markers_expected", claudeJSON(t, false, 1, "NONE")},
		{"malformed", []byte("not json")},
		{"empty", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkClaudeCanary(tc.raw, want, "positive"); err == nil {
				t.Fatalf("expected rejection for %s", tc.name)
			}
		})
	}
}

func TestCheckClaudeCanaryErrorHidesValues(t *testing.T) {
	want := joinMarkers("secretshared", "secretlocal")
	raw := claudeJSON(t, false, 1, "secretshared wrongtoken")
	err := checkClaudeCanary(raw, want, "positive")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if strings.Contains(err.Error(), "secretshared") || strings.Contains(err.Error(), "wrongtoken") {
		t.Fatalf("error leaked marker/model data: %v", err)
	}
}

func codexStream(t *testing.T, events []map[string]any) []byte {
	t.Helper()
	var b strings.Builder
	for i, ev := range events {
		if item, ok := ev["item"].(map[string]any); ok {
			if _, present := item["id"]; !present {
				item["id"] = fmt.Sprintf("item_%d", i)
			}
		}
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func codexGood(text string) []map[string]any {
	return []map[string]any{
		{"type": "thread.started", "thread_id": "t1"},
		{"type": "turn.started"},
		{"type": "item.completed", "item": map[string]any{"type": "reasoning", "text": "thinking"}},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": text}},
		{"type": "turn.completed", "usage": map[string]any{}},
	}
}

func TestCheckCodexCanaryPositive(t *testing.T) {
	want := joinMarkers("aaaa", "bbbb")
	raw := codexStream(t, codexGood("aaaa bbbb"))
	if err := checkCodexCanary(raw, want, "positive"); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}

func TestCheckCodexCanaryNegativeSharedOnly(t *testing.T) {
	raw := codexStream(t, codexGood("aaaa"))
	if err := checkCodexCanary(raw, "aaaa", "negative control"); err != nil {
		t.Fatalf("expected shared-only pass, got %v", err)
	}
}

func TestCheckCodexCanaryRejectsToolItem(t *testing.T) {
	events := []map[string]any{
		{"type": "thread.started"},
		{"type": "turn.started"},
		{"type": "item.completed", "item": map[string]any{"type": "command_execution", "text": "cat AGENTS.local.md"}},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "aaaa bbbb"}},
		{"type": "turn.completed"},
	}
	if err := checkCodexCanary(codexStream(t, events), joinMarkers("aaaa", "bbbb"), "positive"); err == nil {
		t.Fatal("expected rejection of tool item")
	}
}

func TestCheckCodexCanaryRejectsUnknownItem(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.started"},
		{"type": "item.completed", "item": map[string]any{"type": "some_future_item", "text": "x"}},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "aaaa"}},
		{"type": "turn.completed"},
	}
	if err := checkCodexCanary(codexStream(t, events), "aaaa", "positive"); err == nil {
		t.Fatal("expected rejection of unknown item type")
	}
}

func TestCheckCodexCanaryRejectsErrorEvent(t *testing.T) {
	events := []map[string]any{
		{"type": "thread.started"},
		{"type": "error", "message": "auth failed"},
	}
	if err := checkCodexCanary(codexStream(t, events), "aaaa", "positive"); err == nil {
		t.Fatal("expected rejection of error event")
	}
}

func TestCheckCodexCanaryRejectsTurnFailed(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.started"},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "aaaa bbbb"}},
		{"type": "turn.failed", "error": map[string]any{"message": "boom"}},
	}
	if err := checkCodexCanary(codexStream(t, events), joinMarkers("aaaa", "bbbb"), "positive"); err == nil {
		t.Fatal("expected rejection of turn.failed")
	}
}

func TestCheckCodexCanaryRejectsNoCompletedTurn(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.started"},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "aaaa bbbb"}},
	}
	if err := checkCodexCanary(codexStream(t, events), joinMarkers("aaaa", "bbbb"), "positive"); err == nil {
		t.Fatal("expected rejection when turn never completed")
	}
}

func TestCheckCodexCanaryRejectsNoAgentMessage(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.started"},
		{"type": "item.completed", "item": map[string]any{"type": "reasoning", "text": "thinking"}},
		{"type": "turn.completed"},
	}
	if err := checkCodexCanary(codexStream(t, events), "aaaa", "positive"); err == nil {
		t.Fatal("expected rejection when no agent message present")
	}
}

func TestCheckCodexCanaryRejectsMalformedJSON(t *testing.T) {
	raw := []byte("{\"type\":\"turn.started\"}\nthis is not json\n")
	if err := checkCodexCanary(raw, "aaaa", "positive"); err == nil {
		t.Fatal("expected rejection of malformed JSON line")
	}
}

func TestCheckCodexCanaryRejectsWrongMarkers(t *testing.T) {
	raw := codexStream(t, codexGood("aaaa cccc"))
	if err := checkCodexCanary(raw, joinMarkers("aaaa", "bbbb"), "positive"); err == nil {
		t.Fatal("expected mismatch rejection")
	}
}

func TestCheckCodexCanaryRejectsDuplicateMarkers(t *testing.T) {
	for _, answer := range []string{"aaaa aaaa bbbb", "aaaa bbbb bbbb"} {
		raw := codexStream(t, codexGood(answer))
		if err := checkCodexCanary(raw, joinMarkers("aaaa", "bbbb"), "positive"); err == nil {
			t.Fatalf("accepted duplicate markers: %q", answer)
		}
	}
}

func TestCheckCodexCanaryErrorHidesValues(t *testing.T) {
	raw := codexStream(t, codexGood("secretshared wrongtoken"))
	err := checkCodexCanary(raw, joinMarkers("secretshared", "secretlocal"), "positive")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if strings.Contains(err.Error(), "secretshared") || strings.Contains(err.Error(), "wrongtoken") {
		t.Fatalf("error leaked marker/model data: %v", err)
	}
}

func TestParseCodexCanaryTakesLastAgentMessage(t *testing.T) {
	events := []map[string]any{
		{"type": "turn.started"},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "first"}},
		{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": "final"}},
		{"type": "turn.completed"},
	}
	got, err := parseCodexCanary(codexStream(t, events))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "final" {
		t.Fatalf("want last agent message %q, got %q", "final", got)
	}
}

func TestMarkersEqual(t *testing.T) {
	if !markersEqual("  aaaa   bbbb\n", "aaaa bbbb") {
		t.Fatal("normalized whitespace should compare equal")
	}
	if markersEqual("aaaa", "aaaa bbbb") {
		t.Fatal("missing token should not compare equal")
	}
	if markersEqual("AAAA bbbb", "aaaa bbbb") {
		t.Fatal("token comparison must be case-sensitive")
	}
}

func TestNewMarkersDistinctAndSized(t *testing.T) {
	shared, local, err := newMarkers()
	if err != nil {
		t.Fatalf("newMarkers: %v", err)
	}
	if shared == local {
		t.Fatal("shared and local tokens must differ")
	}
	if len(shared) != 32 || len(local) != 32 {
		t.Fatalf("expected 32-hex-char tokens, got %d/%d", len(shared), len(local))
	}
}

func TestMarkerLineContainsLabelAndValue(t *testing.T) {
	line := markerLine(canaryLabelShared, "deadbeef")
	if !strings.Contains(line, canaryLabelShared+": deadbeef") {
		t.Fatalf("marker line missing label/value: %q", line)
	}
}

func TestVerifyCanaryUnknownRuntime(t *testing.T) {
	if err := VerifyCanary(context.Background(), "bogus"); err == nil {
		t.Fatal("expected error for unknown runtime")
	}
}

func TestCanaryMarkerMismatchDiagnostics(t *testing.T) {
	shared, local, forbidden := "private-shared", "private-local", "forbidden-private"
	cases := []struct {
		name, want, got, diagnostics string
		forbidden                    []string
	}{
		{name: "none", want: joinMarkers(shared, local), got: "NONE", diagnostics: "expected_tokens=2 observed_expected=[0 0] unexpected_tokens=1 none_only=true"},
		{name: "missing", want: joinMarkers(shared, local), got: shared, diagnostics: "expected_tokens=2 observed_expected=[1 0] unexpected_tokens=0 none_only=false"},
		{name: "duplicate", want: joinMarkers(shared, local), got: shared + " " + shared + " " + local, diagnostics: "expected_tokens=2 observed_expected=[2 1] unexpected_tokens=0 none_only=false"},
		{name: "explanation", want: joinMarkers(shared, local), got: shared + " " + local + " model-prose", diagnostics: "expected_tokens=2 observed_expected=[1 1] unexpected_tokens=1 none_only=false"},
		{name: "wrong order remains failure", want: joinMarkers(shared, local), got: local + " " + shared, diagnostics: "expected_tokens=2 observed_expected=[1 1] unexpected_tokens=0 none_only=false"},
		{name: "forbidden private", want: shared, got: shared + " " + forbidden, forbidden: []string{forbidden}, diagnostics: "expected_tokens=1 observed_expected=[1] unexpected_tokens=1 none_only=false observed_forbidden=[1]"},
		{name: "negative marker leakage", want: canaryNone, got: local, forbidden: []string{shared, local}, diagnostics: "expected_tokens=0 observed_expected=[] unexpected_tokens=1 none_only=false observed_forbidden=[0 1]"},
		{name: "negative explanation", want: canaryNone, got: "NONE model-prose", diagnostics: "expected_tokens=0 observed_expected=[] unexpected_tokens=1 none_only=false"},
	}
	for _, provider := range []string{"claude", "codex"} {
		for _, tc := range cases {
			t.Run(provider+"/"+tc.name, func(t *testing.T) {
				var err error
				if provider == "claude" {
					err = checkClaudeCanary(claudeJSON(t, false, 1, tc.got), tc.want, "diagnostic test", tc.forbidden...)
				} else {
					err = checkCodexCanary(codexStream(t, codexGood(tc.got)), tc.want, "diagnostic test", tc.forbidden...)
				}
				if err == nil {
					t.Fatal("marker mismatch accepted")
				}
				if !strings.HasSuffix(err.Error(), "("+tc.diagnostics+")") {
					t.Fatalf("incorrect mismatch counts: %v", err)
				}
				for _, value := range []string{shared, local, forbidden, "model-prose"} {
					if strings.Contains(err.Error(), value) {
						t.Fatalf("diagnostic leaked marker or model text: %v", err)
					}
				}
			})
		}
	}
}
