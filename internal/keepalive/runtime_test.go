package keepalive

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	runtimeTestSession = "11111111-1111-4111-8111-111111111111"
	runtimeTestUser    = "22222222-2222-4222-8222-222222222222"
	runtimeTestReply   = "33333333-3333-4333-8333-333333333333"
	runtimeTestAuto    = "44444444-4444-4444-8444-444444444444"
	runtimeTestAnswer  = "55555555-5555-4555-8555-555555555555"
	runtimeTestEnd     = "66666666-6666-4666-8666-666666666666"
)

func runtimeJSON(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func runtimeTurn(t *testing.T, user, reply string, at time.Time) []byte {
	t.Helper()
	var data []byte
	for _, entry := range []map[string]any{
		{"type": "user", "sessionId": runtimeTestSession, "uuid": user, "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "Synthetic input"}},
		{"type": "assistant", "sessionId": runtimeTestSession, "uuid": reply, "parentUuid": user, "timestamp": at.Add(time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "assistant", "model": "example-model", "stop_reason": "end_turn", "content": []map[string]any{{"type": "text", "text": "Synthetic response"}}, "usage": map[string]int{"output_tokens": 7}}},
		{"type": "system", "subtype": "turn_duration", "sessionId": runtimeTestSession, "uuid": runtimeTestEnd, "timestamp": at.Add(2 * time.Second).Format(time.RFC3339Nano), "durationMs": 2000},
	} {
		data = append(data, runtimeJSON(t, entry)...)
	}
	return data
}

func TestRuntimeKeyAndActivityWindow(t *testing.T) {
	a := Candidate{Provider: "claude", Account: "example", SessionID: runtimeTestSession}
	b := a
	b.Account = "other"
	c := a
	c.Provider = "codex"
	d := a
	d.PID, d.Home, d.Transcript = 123, "/example/root", "/example/log"
	if a.Key() == b.Key() || a.Key() == c.Key() || a.Key() != d.Key() {
		t.Fatal("key is not stable and account/provider scoped")
	}
	if (Candidate{Provider: "a", Account: "b:c", SessionID: "d"}).Key() == (Candidate{Provider: "a:b", Account: "c", SessionID: "d"}).Key() {
		t.Fatal("key delimiter collision")
	}
	now := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		at   time.Time
		want bool
	}{{now, true}, {now.Add(-50 * time.Minute), true}, {now.Add(-50*time.Minute - time.Nanosecond), false}, {now.Add(time.Nanosecond), false}, {time.Time{}, false}} {
		if got := runtimeRecent(test.at, now, 50*time.Minute); got != test.want {
			t.Errorf("activity window result = %v, want %v", got, test.want)
		}
	}
}

func TestRuntimeAccountBoundaryAndCancellation(t *testing.T) {
	home := t.TempDir()
	r := &Runtime{Accounts: []Account{{Provider: "claude", Key: "example", Home: home}}}
	now := time.Now()
	if got, err := r.Scan(context.Background(), now, time.Minute, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty synthetic home: %v %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Scan(ctx, now, time.Minute, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("scan ignored cancellation")
	}
	if _, err := r.Deliver(ctx, Candidate{}, "example", time.Minute, runtimeTestAuto, func() bool { return true }); !errors.Is(err, context.Canceled) {
		t.Fatal("delivery ignored cancellation")
	}
	if _, err := r.Scan(context.Background(), now, 0, nil); err == nil {
		t.Fatal("implicit activity window accepted")
	}
	if _, err := r.Deliver(context.Background(), Candidate{Provider: "claude", Account: "other", Home: home, SessionID: runtimeTestSession, PID: 123, ActivityID: runtimeTestUser}, "example", time.Minute, runtimeTestAuto, func() bool { return true }); err == nil {
		t.Fatal("cross-account delivery accepted")
	}
	for _, accounts := range [][]Account{
		{{Provider: "claude", Key: "example"}},
		{{Provider: "unknown", Key: "example", Home: home}},
		{{Provider: "claude", Key: "example", Home: home}, {Provider: "claude", Key: "other", Home: home}},
	} {
		if _, err := (&Runtime{Accounts: accounts}).runtimeAccounts(); err == nil {
			t.Error("invalid or ambiguous account accepted")
		}
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(home, alias); err != nil {
		t.Fatal(err)
	}
	r.Accounts = append(r.Accounts, Account{Provider: "claude", Key: "other", Home: alias})
	if _, err := r.runtimeAccounts(); err == nil {
		t.Fatal("aliased account ownership accepted")
	}
}

func TestMissingDefaultProviderHomeDoesNotBlockOthers(t *testing.T) {
	r := Runtime{Accounts: []Account{
		{Provider: "claude", Key: "example", Home: t.TempDir()},
		{Provider: "codex", Key: "other", Home: filepath.Join(t.TempDir(), "missing")},
	}}
	if candidates, err := r.Scan(context.Background(), time.Now(), time.Hour, nil); err != nil || len(candidates) != 0 {
		t.Fatalf("missing provider blocked scan: %v %v", candidates, err)
	}
}

func TestClaudeContextAttachmentsAndMetaResponse(t *testing.T) {
	for _, attachment := range []string{"environment", "deferred_tools_delta", "agent_listing_delta", "skill_listing", "auto_mode", "total_tokens_reminder"} {
		for _, meta := range []bool{false, true} {
			at := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
			lines := strings.Split(strings.TrimSpace(string(runtimeTurn(t, runtimeTestUser, runtimeTestReply, at))), "\n")
			var user, answer map[string]any
			json.Unmarshal([]byte(lines[0]), &user)
			json.Unmarshal([]byte(lines[1]), &answer)
			user["isMeta"] = meta
			answer["parentUuid"] = runtimeTestAuto
			var data []byte
			data = append(data, runtimeJSON(t, map[string]any{"type": "ai-title", "sessionId": runtimeTestSession, "title": "Example"})...)
			data = append(data, runtimeJSON(t, user)...)
			data = append(data, runtimeJSON(t, map[string]any{"type": "attachment", "sessionId": runtimeTestSession, "uuid": runtimeTestAuto, "parentUuid": runtimeTestUser, "attachment": map[string]string{"type": attachment}})...)
			data = append(data, runtimeJSON(t, answer)...)
			data = append(data, runtimeJSON(t, map[string]any{"type": "system", "subtype": "stop_hook_summary", "sessionId": runtimeTestSession})...)
			data = append(data, []byte(lines[2]+"\n")...)
			state, err := parseClaudeTranscript(context.Background(), data, runtimeTestSession)
			if err != nil || !state.responseComplete() || state.idle() == meta || state.activity != runtimeTestUser {
				t.Fatalf("attachment=%s meta=%v: state=%+v error=%v", attachment, meta, state, err)
			}
		}
	}
}

func TestCodexRuntimeRejectsMissingLiveSession(t *testing.T) {
	home := t.TempDir()
	r := &Runtime{Accounts: []Account{{Provider: "codex", Key: "example", Home: home}}}
	got, err := r.Scan(context.Background(), time.Now(), time.Hour, nil)
	if len(got) != 0 {
		t.Fatalf("missing live session must be excluded: %v", err)
	}
	accounts, err := r.runtimeAccounts()
	if err != nil {
		t.Fatal(err)
	}
	c := Candidate{Provider: "codex", Account: "example", Home: accounts[0].Home, SessionID: runtimeTestSession, PID: 123, ActivityID: runtimeTestUser}
	receipt, err := r.Deliver(context.Background(), c, "example", time.Hour, runtimeTestAuto, func() bool { return true })
	if receipt != (Receipt{}) || err == nil {
		t.Fatalf("missing live session dispatched: %+v %v", receipt, err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatal("missing live session created state")
	}
}

func TestClaudeTranscriptTurnBoundaries(t *testing.T) {
	at := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	baseline := runtimeTurn(t, runtimeTestUser, runtimeTestReply, at)
	tests := []struct {
		name string
		edit func([]byte) []byte
		idle bool
		err  bool
	}{
		{"completed", func(b []byte) []byte { return b }, true, false},
		{"null stop requires duration", func(b []byte) []byte {
			return []byte(strings.ReplaceAll(string(b), `"stop_reason":"end_turn"`, `"stop_reason":null`))
		}, true, false},
		{"no end marker", func(b []byte) []byte { return b[:strings.LastIndex(string(b[:len(b)-1]), "\n")+1] }, false, false},
		{"truncated", func(b []byte) []byte { return b[:len(b)-1] }, false, true},
		{"wrong session", func(b []byte) []byte {
			return []byte(strings.ReplaceAll(string(b), runtimeTestSession, runtimeTestAuto))
		}, false, true},
		{"subagent transcript", func(b []byte) []byte { return append(b, []byte("{\"type\":\"user\",\"isSidechain\":true}\n")...) }, false, true},
		{"wrong ancestry", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"parentUuid":"`+runtimeTestUser+`"`, `"parentUuid":"`+runtimeTestAuto+`"`, 1))
		}, false, true},
		{"refusal", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"stop_reason":"end_turn"`, `"stop_reason":"refusal"`, 1))
		}, false, false},
		{"tool call", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"type":"text"`, `"type":"tool_use"`, 1))
		}, false, false},
		{"no model usage", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"output_tokens":7`, `"output_tokens":0`, 1))
		}, false, false},
		{"background work", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"durationMs":2000`, `"durationMs":2000,"pendingBackgroundAgentCount":1`, 1))
		}, false, false},
		{"workflow work", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"durationMs":2000`, `"durationMs":2000,"pendingWorkflowCount":1`, 1))
		}, false, false},
		{"unknown work count", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"durationMs":2000`, `"durationMs":2000,"pendingWorkflowCount":null`, 1))
		}, false, true},
		{"missing end duration", func(b []byte) []byte {
			return []byte(strings.Replace(string(b), `"durationMs":2000,`, "", 1))
		}, false, true},
		{"unknown record", func(b []byte) []byte { return append(b, []byte("{\"type\":\"unrecognized\"}\n")...) }, false, true},
		{"api error", func(b []byte) []byte {
			return append(b, []byte("{\"type\":\"system\",\"subtype\":\"api_error\"}\n")...)
		}, false, false},
		{"new input", func(b []byte) []byte {
			return append(b, runtimeJSON(t, map[string]any{"type": "user", "sessionId": runtimeTestSession, "uuid": runtimeTestAuto, "timestamp": at.Add(3 * time.Second).Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": "next input"}})...)
		}, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := parseClaudeTranscript(context.Background(), test.edit(append([]byte(nil), baseline...)), runtimeTestSession)
			if (err != nil) != test.err || err == nil && state.idle() != test.idle {
				t.Fatalf("idle=%v error=%v; want idle=%v error=%v", state.idle(), err, test.idle, test.err)
			}
		})
	}
	state, err := parseClaudeTranscript(context.Background(), baseline, runtimeTestSession)
	if err != nil || state.activity != runtimeTestUser || !state.lastActivity.Equal(at.Add(time.Second)) {
		t.Fatal("turn ID and actual model activity timestamp were not preserved")
	}
	data := append(baseline, runtimeTurn(t, runtimeTestAuto, runtimeTestAnswer, at.Add(time.Minute))...)
	state, err = parseClaudeTranscript(context.Background(), data, runtimeTestSession)
	if err != nil || state.activity != runtimeTestAuto {
		t.Fatal("automation ID must remain the input UUID through turn completion")
	}
}

func claudeUserRecord(uuid, parent, content string, at time.Time) map[string]any {
	record := map[string]any{"type": "user", "sessionId": runtimeTestSession, "uuid": uuid, "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": content}}
	if parent != "" {
		record["parentUuid"] = parent
	}
	return record
}

func claudeToolResultRecord(uuid, parent string, at time.Time) map[string]any {
	return map[string]any{"type": "user", "sessionId": runtimeTestSession, "uuid": uuid, "parentUuid": parent, "timestamp": at.Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result"}}}}
}

func claudeAssistantRecord(uuid, parent, msgID string, at time.Time, usage map[string]any, text bool) map[string]any {
	content := []map[string]any{{"type": "tool_use"}}
	if text {
		content = []map[string]any{{"type": "text", "text": "Synthetic response"}}
	}
	message := map[string]any{"role": "assistant", "model": "example-model", "stop_reason": "end_turn", "content": content}
	if msgID != "" {
		message["id"] = msgID
	}
	if usage != nil {
		message["usage"] = usage
	}
	return map[string]any{"type": "assistant", "sessionId": runtimeTestSession, "uuid": uuid, "parentUuid": parent, "timestamp": at.Format(time.RFC3339Nano), "message": message}
}

func claudeUsageFields(input, read, create int) map[string]any {
	return map[string]any{"output_tokens": 7, "input_tokens": input, "cache_read_input_tokens": read, "cache_creation_input_tokens": create}
}

func TestClaudeTranscriptCacheUsage(t *testing.T) {
	at := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	const (
		asstA = "77777777-7777-4777-8777-777777777777"
		toolU = "88888888-8888-4888-8888-888888888888"
		asstB = "99999999-9999-4999-8999-999999999999"
		user2 = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	)
	build := func(records ...map[string]any) []byte {
		var data []byte
		for _, record := range records {
			data = append(data, runtimeJSON(t, record)...)
		}
		return data
	}
	tests := []struct {
		name string
		data []byte
		want *CacheUsage
	}{
		{"positive", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "msg-1", at.Add(time.Second), claudeUsageFields(100, 20, 10), true),
		), &CacheUsage{InputTokens: 130, CachedTokens: 20}},
		{"zero cached", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "msg-1", at.Add(time.Second), claudeUsageFields(100, 0, 0), true),
		), &CacheUsage{InputTokens: 100, CachedTokens: 0}},
		{"missing usage fields", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "msg-1", at.Add(time.Second), map[string]any{"output_tokens": 7}, true),
		), nil},
		{"missing message id", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "", at.Add(time.Second), claudeUsageFields(100, 20, 10), true),
		), nil},
		{"negative count", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "msg-1", at.Add(time.Second), claudeUsageFields(-1, 20, 10), true),
		), nil},
		{"duplicate streaming update", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "msg-1", at.Add(time.Second), claudeUsageFields(100, 20, 10), false),
			claudeAssistantRecord(asstA, runtimeTestReply, "msg-1", at.Add(2*time.Second), claudeUsageFields(100, 20, 10), true),
		), &CacheUsage{InputTokens: 130, CachedTokens: 20}},
		{"multiple requests", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "msg-1", at.Add(time.Second), claudeUsageFields(100, 20, 10), false),
			claudeToolResultRecord(toolU, runtimeTestReply, at.Add(2*time.Second)),
			claudeAssistantRecord(asstB, toolU, "msg-2", at.Add(3*time.Second), claudeUsageFields(200, 30, 0), true),
		), &CacheUsage{InputTokens: 360, CachedTokens: 50}},
		{"reset discards prior turn", build(
			claudeUserRecord(runtimeTestUser, "", "Synthetic input", at),
			claudeAssistantRecord(runtimeTestReply, runtimeTestUser, "msg-1", at.Add(time.Second), claudeUsageFields(-1, 20, 10), true),
			claudeUserRecord(user2, "", "Next input", at.Add(2*time.Second)),
			claudeAssistantRecord(asstB, user2, "msg-2", at.Add(3*time.Second), claudeUsageFields(200, 50, 0), true),
		), &CacheUsage{InputTokens: 250, CachedTokens: 50}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := parseClaudeTranscript(context.Background(), test.data, runtimeTestSession)
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}
			got := state.cacheUsage()
			if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
				t.Fatalf("cache usage = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestClaudeTranscriptQueueBalance(t *testing.T) {
	baseline := runtimeTurn(t, runtimeTestUser, runtimeTestReply, time.Now().Add(-time.Minute))
	op := func(operation string) []byte {
		return runtimeJSON(t, map[string]any{"type": "queue-operation", "sessionId": runtimeTestSession, "operation": operation})
	}
	data := append(append([]byte(nil), baseline...), op("enqueue")...)
	state, err := parseClaudeTranscript(context.Background(), data, runtimeTestSession)
	if err != nil || state.idle() {
		t.Fatal("queued input treated as idle")
	}
	data = append(data, op("enqueue")...)
	data = append(data, op("dequeue")...)
	state, err = parseClaudeTranscript(context.Background(), data, runtimeTestSession)
	if err != nil || state.idle() {
		t.Fatal("dequeue incorrectly cleared multiple inputs")
	}
	data = append(data, op("remove")...)
	state, err = parseClaudeTranscript(context.Background(), data, runtimeTestSession)
	if err != nil || !state.idle() {
		t.Fatal("balanced queue was not accepted")
	}
	for _, operation := range []string{"dequeue", "unknown"} {
		if _, err := parseClaudeTranscript(context.Background(), append(append([]byte(nil), baseline...), op(operation)...), runtimeTestSession); err == nil {
			t.Error("incomplete/unknown queue history accepted")
		}
	}
}

func TestRuntimeParserErrorsDoNotExposeContents(t *testing.T) {
	marker := "SYNTHETIC_PRIVATE_MARKER"
	for _, data := range [][]byte{[]byte(marker + "\n"), []byte(`{"type":"` + marker + `"}` + "\n")} {
		_, err := parseClaudeTranscript(context.Background(), data, runtimeTestSession)
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatal("parser error exposed input")
		}
	}
}

func TestCodexSettingsEventPreservesTurnState(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	for _, active := range []bool{false, true} {
		for _, wrongOwner := range []bool{false, true} {
			records := codexTestRecords(at)
			if active {
				records = records[:7]
			}
			owner := codexTestSession
			if wrongOwner {
				owner = codexTestUser
			}
			records = append(records, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "thread_settings_applied", "thread_id": owner, "thread_settings": map[string]any{"cwd": "/example/project"}}})
			state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
			if wrongOwner {
				if err == nil {
					t.Fatal("foreign settings accepted")
				}
				continue
			}
			if err != nil || state.idle() == active {
				t.Fatalf("settings changed activity state: active=%v idle=%v err=%v", active, state.idle(), err)
			}
		}
	}
}

func TestCodexBackgroundCommandOutlivesAnswer(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	records := codexTestRecords(at)
	command := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": codexTestSession, "turn_id": codexTestTurn, "item": map[string]any{"type": "CommandExecution", "id": "command-1", "status": "in_progress"}}}
	records = append(records[:5], append([]map[string]any{command}, records[5:]...)...)
	state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || state.idle() {
		t.Fatalf("background command treated idle: %v", err)
	}
	records = append(records, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "exec_command_end", "call_id": "command-1", "status": "completed", "exit_code": 0}})
	state, err = parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || !state.idle() {
		t.Fatalf("completed command kept busy: %v", err)
	}
}

func TestCodexResolvedApprovalAndAbortedTurnRecover(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	for _, event := range []string{"exec_approval_request", "apply_patch_approval_request", "request_user_input", "turn_aborted"} {
		records := codexTestRecords(at)
		wait := map[string]any{"type": "event_msg", "payload": map[string]any{"type": event, "turn_id": codexTestTurn}}
		if event == "turn_aborted" {
			prefix := append(records[:5], wait)
			next := codexTestRecords(at)[1:]
			for _, row := range next {
				p := row["payload"].(map[string]any)
				if _, ok := p["turn_id"]; ok {
					p["turn_id"] = codexTestClient
				}
			}
			records = append(prefix, next...)
		} else {
			records = append(records[:5], append([]map[string]any{wait}, records[5:]...)...)
		}
		state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
		if err != nil || !state.idle() {
			t.Fatalf("resolved %s remained excluded: %v", event, err)
		}
	}
}

func TestCodexYieldedCommandCompletesAfterTurn(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	records := codexTestRecords(at)
	call := map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call", "name": "exec_command", "call_id": "command-1", "arguments": "{}"}}
	records = append(records[:5], append([]map[string]any{call}, records[5:]...)...)
	state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || state.idle() {
		t.Fatalf("yielded command treated idle: %v", err)
	}
	records = append(records, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "exec_command_output_delta", "call_id": "command-1", "chunk": []int{79, 75}}})
	records = append(records, map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": codexTestSession, "turn_id": codexTestTurn, "item": map[string]any{"type": "CommandExecution", "id": "command-1", "status": "completed", "exit_code": 0}}})
	state, err = parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || !state.idle() {
		t.Fatalf("post-turn completion not recognized: %v", err)
	}
}

func TestAccountPathFailurePreservesIndependentAccounts(t *testing.T) {
	home := t.TempDir()
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	loop := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	good := Account{Provider: "claude", Key: "healthy", Home: home}
	bad := Account{Provider: "claude", Key: "broken", Home: loop}
	for _, accounts := range [][]Account{{good, bad}, {bad, good}} {
		r := Runtime{Accounts: accounts}
		got, err := r.runtimeAccounts()
		if err == nil || len(got) != 1 || got[0] != good {
			t.Fatalf("lost independent account: %v %v", got, err)
		}
		if err := os.WriteFile(filepath.Join(home, "sessions"), []byte("invalid directory"), 0600); err != nil {
			t.Fatal(err)
		}
		_, err = r.Scan(context.Background(), time.Now(), time.Minute, nil)
		if err == nil || !strings.Contains(err.Error(), "broken") || !strings.Contains(err.Error(), "healthy") || !strings.Contains(err.Error(), "claude runtime registry unavailable") {
			t.Fatalf("independent scanner was skipped or account errors lost: %v", err)
		}
		candidate := Candidate{Provider: "claude", Account: "healthy", Home: home, SessionID: runtimeTestSession, PID: 123, ActivityID: runtimeTestUser}
		_, err = r.Deliver(context.Background(), candidate, "OK", time.Minute, runtimeTestAuto, func() bool { return true })
		if err == nil || strings.Contains(err.Error(), "broken") || strings.Contains(err.Error(), "does not match") {
			t.Fatalf("unrelated account prevented target inspection: %v", err)
		}
		candidate.Account, candidate.Home = "broken", loop
		if _, err = r.Deliver(context.Background(), candidate, "OK", time.Minute, runtimeTestAuto, func() bool { return true }); err == nil {
			t.Fatal("broken target accepted")
		}
	}
	bad.Key = good.Key
	for _, accounts := range [][]Account{{good, bad}, {bad, good}} {
		if got, err := (&Runtime{Accounts: accounts}).runtimeAccounts(); err == nil || len(got) != 0 {
			t.Fatalf("duplicate key escaped through path error: %v %v", got, err)
		}
	}
}

func TestRuntimeExcludesEveryConflictingAccountAndRetainsOthers(t *testing.T) {
	for _, kind := range []string{"relative", "duplicate home", "duplicate key", "key and home", "invalid sibling", "symlink loop"} {
		for _, reverse := range []bool{false, true} {
			t.Run(kind+"/"+map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				dir := filepath.Join(root, "conflict")
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				alias := filepath.Join(root, "alias")
				if err := os.Symlink(dir, alias); err != nil {
					t.Fatal(err)
				}
				bad := []Account{{Provider: "claude", Key: "bad", Home: dir}}
				switch kind {
				case "relative":
					bad[0].Home = "relative"
				case "duplicate home":
					bad = append(bad, Account{Provider: "claude", Key: "alias", Home: alias})
				case "duplicate key":
					bad = append(bad, Account{Provider: "claude", Key: "bad", Home: root})
				case "key and home":
					bad = append(bad, Account{Provider: "claude", Key: "bad", Home: root}, Account{Provider: "claude", Key: "alias", Home: alias})
				case "invalid sibling":
					bad = append(bad, Account{Provider: "claude", Key: "bad", Home: "relative"})
				case "symlink loop":
					loop := filepath.Join(root, "loop")
					if err := os.Symlink(loop, loop); err != nil {
						t.Fatal(err)
					}
					bad[0].Home = loop
				}
				goodDir := filepath.Join(root, "valid")
				if err := os.Mkdir(goodDir, 0700); err != nil {
					t.Fatal(err)
				}
				good := Account{Provider: "claude", Key: "valid", Home: goodDir}
				otherProvider := Account{Provider: "codex", Key: "other", Home: dir}
				entries := append(bad, good, otherProvider)
				if reverse {
					for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
						entries[i], entries[j] = entries[j], entries[i]
					}
				}
				runtime := &Runtime{Accounts: entries}
				accounts, err := runtime.runtimeAccounts()
				if err == nil || len(accounts) != 2 {
					t.Fatalf("accounts = %v, %v", accounts, err)
				}
				for _, account := range accounts {
					if account != good && account != otherProvider {
						t.Fatalf("conflicting account retained: %v", account)
					}
				}
				if err := os.Mkdir(filepath.Join(goodDir, "sessions"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(goodDir, "sessions", strconv.Itoa(os.Getpid())+".json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
				_, err = runtime.Scan(context.Background(), time.Now(), time.Hour, nil)
				if err == nil || !strings.Contains(err.Error(), "keepalive account valid: claude runtime registry shape is unsupported") {
					t.Fatalf("valid account was not scanned: %v", err)
				}
				candidate := Candidate{Provider: good.Provider, Account: good.Key, Home: good.Home, SessionID: runtimeTestSession, PID: 123, ActivityID: runtimeTestUser}
				_, err = runtime.Deliver(context.Background(), candidate, "synthetic", time.Hour, runtimeTestAuto, func() bool { return true })
				if err == nil || err.Error() != "claude runtime registry shape is unsupported" {
					t.Fatalf("valid candidate rejected by unrelated account config: %v", err)
				}
				candidate.Account, candidate.Home = "bad", dir
				_, err = runtime.Deliver(context.Background(), candidate, "synthetic", time.Hour, runtimeTestAuto, func() bool { return true })
				if err == nil || !strings.Contains(err.Error(), "does not match a configured account") {
					t.Fatalf("invalid candidate admitted: %v", err)
				}
			})
		}
	}
}
