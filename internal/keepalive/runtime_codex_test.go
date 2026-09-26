//go:build darwin || linux

package keepalive

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	codexTestSession = "11111111-1111-4111-8111-111111111111"
	codexTestTurn    = "22222222-2222-4222-8222-222222222222"
	codexTestUser    = "33333333-3333-4333-8333-333333333333"
	codexTestClient  = "44444444-4444-4444-8444-444444444444"
)

func codexTestRecords(at time.Time) []map[string]any {
	return []map[string]any{
		{"type": "session_meta", "payload": map[string]any{"id": codexTestSession, "cwd": "/example/project", "source": "cli", "cli_version": "0.153.4"}},
		{"type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": codexTestTurn, "started_at": at.Unix()}},
		{"type": "turn_context", "payload": map[string]any{"turn_id": codexTestTurn, "cwd": "/example/project"}},
		{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Synthetic input"}}}},
		{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": codexTestSession, "turn_id": codexTestTurn, "item": map[string]any{"type": "UserMessage", "id": codexTestUser, "client_id": codexTestClient, "content": []any{map[string]any{"type": "text", "text": "Synthetic input"}}}}},
		{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": codexTestSession, "turn_id": codexTestTurn, "item": map[string]any{"type": "AgentMessage", "id": "synthetic-response", "phase": "final_answer", "content": []any{map[string]any{"type": "Text", "text": "OK"}}}}},
		{"type": "event_msg", "payload": map[string]any{"type": "token_count"}},
		{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": codexTestTurn, "started_at": at.Unix(), "completed_at": at.Unix()}},
	}
}

func codexTestData(t *testing.T, records []map[string]any, at time.Time) []byte {
	t.Helper()
	var data []byte
	for _, record := range records {
		record["timestamp"] = at.Format(time.RFC3339Nano)
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, append(line, '\n')...)
	}
	return data
}

func codexTestPayload(records []map[string]any, index int) map[string]any {
	return records[index]["payload"].(map[string]any)
}

func TestCodexCustomExecBackgroundCompletion(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	records := codexTestRecords(at)
	call := map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "synthetic-call", "status": "completed"}}
	output := map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call_output", "call_id": "synthetic-call", "output": []any{map[string]any{"type": "input_text", "text": "Script completed\n"}, map[string]any{"type": "input_text", "text": `{"session_id":4321}`}}}}
	records = append(records[:5], append([]map[string]any{call, output}, records[5:]...)...)
	state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || state.idle() {
		t.Fatalf("live custom command accepted: idle=%v error=%v", state.idle(), err)
	}
	completed := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": codexTestSession, "turn_id": codexTestTurn, "item": map[string]any{"type": "CommandExecution", "id": "synthetic-command", "process_id": "4321", "status": "completed", "exit_code": 0}}}
	records = append(records, completed)
	state, err = parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || !state.idle() {
		t.Fatalf("completed custom command stayed excluded: idle=%v error=%v", state.idle(), err)
	}
	started := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_started", "thread_id": codexTestSession, "turn_id": codexTestTurn, "item": map[string]any{"type": "CommandExecution", "id": "synthetic-command"}}}
	withStart := append([]map[string]any{}, records[:5]...)
	withStart = append(withStart, started)
	withStart = append(withStart, records[5:]...)
	state, err = parseCodexTranscript(context.Background(), codexTestData(t, withStart, at), codexTestSession, "/example/project")
	if err != nil || !state.idle() {
		t.Fatalf("both command identities were not completed: idle=%v error=%v", state.idle(), err)
	}
	output["payload"].(map[string]any)["output"] = "aborted"
	_, err = parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err == nil {
		t.Fatal("unreadable custom command output accepted")
	}
	for _, tc := range []struct{ name, header, result string }{
		{"running", "Script running with cell ID 7\n", `{"exit_code":0}`},
		{"missing result", "Script completed\n", `{}`},
		{"conflicting result", "Script completed\n", `{"session_id":4321,"exit_code":0}`},
	} {
		output["payload"].(map[string]any)["output"] = []any{map[string]any{"type": "input_text", "text": tc.header}, map[string]any{"type": "input_text", "text": tc.result}}
		if _, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project"); err == nil {
			t.Errorf("%s custom command output accepted", tc.name)
		}
	}
	output["payload"].(map[string]any)["output"] = []any{map[string]any{"type": "input_text", "text": "Script completed\n"}, map[string]any{"type": "input_text", "text": `{"exit_code":0}`}}
	state, err = parseCodexTranscript(context.Background(), codexTestData(t, records[:len(records)-1], at), codexTestSession, "/example/project")
	if err != nil || !state.idle() {
		t.Fatalf("completed custom command stayed excluded: idle=%v error=%v", state.idle(), err)
	}
}

func TestCodexTranscriptCorrelatesCompletedInput(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	for _, client := range []bool{false, true} {
		records := codexTestRecords(at)
		want := codexTestClient
		if !client {
			delete(codexTestPayload(records, 4)["item"].(map[string]any), "client_id")
			want = codexTestUser
		}
		state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
		if err != nil || !state.idle() || state.activity != want || !state.lastActivity.Equal(at) {
			t.Fatalf("completed input not correlated: %+v %v", state, err)
		}
		e := codexEvidence{thread: codexThread{ID: codexTestSession, Transcript: "/example/account/sessions/log.jsonl"}, state: state}
		a := Account{Provider: "codex", Key: "codex", Home: "/example/account"}
		if e.candidate(a, 123, at.Add(time.Second), time.Minute).ActivityID != want {
			t.Fatal("completed user activity unavailable")
		}
		if e.candidate(a, 123, at.Add(2*time.Minute), time.Minute).ActivityID != "" || e.candidate(a, 123, at.Add(-time.Second), time.Minute).ActivityID != "" {
			t.Fatal("stale or future activity accepted")
		}
	}
}

func codexTokenCount(input, cached int) map[string]any {
	return map[string]any{"type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": map[string]any{"input_tokens": input, "cached_input_tokens": cached}}}}
}

// codexTurnWithUsage replaces the bare token_count in the synthetic turn with an
// ordered sequence of cumulative total_token_usage snapshots.
func codexTurnWithUsage(at time.Time, counts ...[2]int) []map[string]any {
	r := codexTestRecords(at)
	turn := append([]map[string]any{}, r[:6]...)
	for _, c := range counts {
		turn = append(turn, codexTokenCount(c[0], c[1]))
	}
	return append(turn, r[7])
}

func TestCodexTranscriptCacheUsage(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	tests := []struct {
		name    string
		records []map[string]any
		want    *CacheUsage
	}{
		{"positive", codexTurnWithUsage(at, [2]int{120, 30}), &CacheUsage{InputTokens: 120, CachedTokens: 30}},
		{"zero cached", codexTurnWithUsage(at, [2]int{120, 0}), &CacheUsage{InputTokens: 120, CachedTokens: 0}},
		{"missing snapshot", codexTestRecords(at), nil},
		{"negative count", codexTurnWithUsage(at, [2]int{-1, 0}), nil},
		{"non-monotonic total", codexTurnWithUsage(at, [2]int{120, 30}, [2]int{80, 10}), nil},
		{"duplicate snapshot", codexTurnWithUsage(at, [2]int{120, 30}, [2]int{120, 30}), &CacheUsage{InputTokens: 120, CachedTokens: 30}},
		{"multiple requests", codexTurnWithUsage(at, [2]int{120, 30}, [2]int{200, 50}), &CacheUsage{InputTokens: 200, CachedTokens: 50}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, err := parseCodexTranscript(context.Background(), codexTestData(t, test.records, at), codexTestSession, "/example/project")
			if err != nil || !state.idle() {
				t.Fatalf("parse error=%v idle=%v", err, state.idle())
			}
			got := state.cacheUsage()
			if (got == nil) != (test.want == nil) || got != nil && *got != *test.want {
				t.Fatalf("cache usage = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestCodexTranscriptCacheUsageSubtractsPreviousTurn(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	const (
		turn2 = "55555555-5555-4555-8555-555555555555"
		user2 = "66666666-6666-4666-8666-666666666666"
	)
	records := codexTurnWithUsage(at, [2]int{100, 20})
	second := []map[string]any{
		{"type": "event_msg", "payload": map[string]any{"type": "task_started", "turn_id": turn2, "started_at": at.Unix()}},
		{"type": "turn_context", "payload": map[string]any{"turn_id": turn2, "cwd": "/example/project"}},
		{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Second input"}}}},
		{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": codexTestSession, "turn_id": turn2, "item": map[string]any{"type": "UserMessage", "id": user2, "content": []any{map[string]any{"type": "text", "text": "Second input"}}}}},
		{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "thread_id": codexTestSession, "turn_id": turn2, "item": map[string]any{"type": "AgentMessage", "id": "synthetic-response-2", "phase": "final_answer", "content": []any{map[string]any{"type": "Text", "text": "OK"}}}}},
		codexTokenCount(250, 60),
		{"type": "event_msg", "payload": map[string]any{"type": "task_complete", "turn_id": turn2, "started_at": at.Unix(), "completed_at": at.Unix()}},
	}
	records = append(records, second...)
	state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
	if err != nil || !state.idle() || state.activity != user2 {
		t.Fatalf("parse error=%v idle=%v activity=%s", err, state.idle(), state.activity)
	}
	want := &CacheUsage{InputTokens: 150, CachedTokens: 40}
	if got := state.cacheUsage(); got == nil || *got != *want {
		t.Fatalf("cache usage = %+v, want %+v", got, want)
	}
}

func TestCodexTranscriptExcludesRunningAndUnknownState(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	for _, test := range []struct {
		name    string
		edit    func([]map[string]any) []map[string]any
		wantErr bool
	}{
		{"running", func(r []map[string]any) []map[string]any { return r[:7] }, false},
		{"input without answer", func(r []map[string]any) []map[string]any { return r[:5] }, false},
		{"wrong completion", func(r []map[string]any) []map[string]any { codexTestPayload(r, 7)["turn_id"] = codexTestUser; return r }, true},
		{"unmatched completion", func(r []map[string]any) []map[string]any { return append(r[:1], r[7]) }, true},
		{"duplicate start", func(r []map[string]any) []map[string]any { return append(r[:7], r[1]) }, true},
		{"wrong thread", func(r []map[string]any) []map[string]any {
			codexTestPayload(r, 4)["thread_id"] = codexTestUser
			return r
		}, true},
		{"wrong item turn", func(r []map[string]any) []map[string]any { codexTestPayload(r, 4)["turn_id"] = codexTestUser; return r }, true},
		{"missing user identity", func(r []map[string]any) []map[string]any {
			delete(codexTestPayload(r, 4)["item"].(map[string]any), "id")
			return r
		}, true},
		{"invalid client identity", func(r []map[string]any) []map[string]any {
			codexTestPayload(r, 4)["item"].(map[string]any)["client_id"] = "unsupported"
			return r
		}, true},
		{"commentary only", func(r []map[string]any) []map[string]any {
			codexTestPayload(r, 5)["item"].(map[string]any)["phase"] = "commentary"
			return r
		}, true},
		{"wrong started time", func(r []map[string]any) []map[string]any { codexTestPayload(r, 7)["started_at"] = 1; return r }, true},
		{"future completion", func(r []map[string]any) []map[string]any {
			codexTestPayload(r, 7)["completed_at"] = at.Add(time.Hour).Unix()
			return r
		}, true},
		{"below minimum version", func(r []map[string]any) []map[string]any { codexTestPayload(r, 0)["cli_version"] = "0.153.3"; return r }, true},
		{"malformed version", func(r []map[string]any) []map[string]any { codexTestPayload(r, 0)["cli_version"] = "0.153"; return r }, true},
		{"subagent source", func(r []map[string]any) []map[string]any { codexTestPayload(r, 0)["source"] = "subagent"; return r }, true},
		{"changed cwd", func(r []map[string]any) []map[string]any { codexTestPayload(r, 2)["cwd"] = "/example/other"; return r }, true},
		{"unknown outer shape", func(r []map[string]any) []map[string]any { r[6]["type"] = "unknown"; return r }, true},
		{"unknown item", func(r []map[string]any) []map[string]any {
			codexTestPayload(r, 5)["item"].(map[string]any)["type"] = "Unknown"
			return r
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, err := parseCodexTranscript(context.Background(), codexTestData(t, test.edit(codexTestRecords(at)), at), codexTestSession, "/example/project")
			if (err != nil) != test.wantErr || err == nil && state.idle() {
				t.Fatalf("unsafe state accepted: idle=%v err=%v", state.idle(), err)
			}
		})
	}
	for _, event := range []string{"turn_aborted", "error", "exec_approval_request", "apply_patch_approval_request", "request_user_input", "collab_agent_spawn_begin", "unknown"} {
		r := codexTestRecords(at)
		r = append(r, map[string]any{"type": "event_msg", "payload": map[string]any{"type": event}})
		if _, err := parseCodexTranscript(context.Background(), codexTestData(t, r, at), codexTestSession, "/example/project"); err == nil {
			t.Errorf("%s accepted", event)
		}
	}
	data := codexTestData(t, codexTestRecords(at), at)
	if _, err := parseCodexTranscript(context.Background(), data[:len(data)-1], codexTestSession, "/example/project"); err == nil {
		t.Fatal("partial record accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parseCodexTranscript(ctx, data, codexTestSession, "/example/project"); err != context.Canceled {
		t.Fatal("cancellation ignored")
	}
}

func TestCodexTranscriptVersionMinimum(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	for _, tc := range []struct {
		version string
		accept  bool
	}{
		{"0.153.4", true},
		{"0.153.5", true},
		{"0.154.0", true},
		{"1.0.0", true},
		{"0.153.3", false},
		{"0.152.9", false},
		{"0.153", false},
		{"0.153.4.1", false},
		{"0.153.x", false},
		{"0.153.4-rc.1", false},
		{"", false},
	} {
		records := codexTestRecords(at)
		codexTestPayload(records, 0)["cli_version"] = tc.version
		state, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project")
		if tc.accept {
			if err != nil || !state.idle() {
				t.Fatalf("version %q rejected: err=%v idle=%v", tc.version, err, state.idle())
			}
			continue
		}
		if err == nil {
			t.Fatalf("version %q not rejected by version gate", tc.version)
		}
	}
	records := codexTestRecords(at)
	codexTestPayload(records, 0)["cli_version"] = "1.0.0"
	codexTestPayload(records, 0)["source"] = "subagent"
	if _, err := parseCodexTranscript(context.Background(), codexTestData(t, records, at), codexTestSession, "/example/project"); err == nil {
		t.Fatal("newer version bypassed source safeguard")
	}
}

func TestCodexLocksPreserveIndependentAndAmbiguousOwners(t *testing.T) {
	home := "/example/account"
	path := filepath.Join(home, "thread-writer-locks", codexTestSession+".lock")
	other := filepath.Join(home, "thread-writer-locks", codexTestUser+".lock")
	data := "p123\nf3u\nn" + path + "\nf4u\nn" + path + "\np456\nf3u\nn" + path + "\np789\nf3u\nn" + other + "\n"
	owners, err := parseCodexLocks([]byte(data), home)
	if err != nil || len(owners[codexTestSession]) != 2 || len(owners[codexTestUser]) != 1 {
		t.Fatalf("ownership lost: %v %v", owners, err)
	}
	if codexSoleWriter(owners, codexTestSession, 123) || !codexSoleWriter(owners, codexTestUser, 789) {
		t.Fatal("ambiguous and independent writers conflated")
	}
	shared := map[string][]int{codexTestSession: {123}, codexTestUser: {123}}
	if codexSoleWriter(shared, codexTestSession, 123) {
		t.Fatal("multiple loaded threads treated as one interactive session")
	}
	for _, data := range []string{"p0\nf3u\nn" + path, "pbad\n", "p123\nf3u\nn" + filepath.Join(home, "thread-writer-locks", "invalid.lock")} {
		if _, err := parseCodexLocks([]byte(data), home); err == nil {
			t.Fatal("unsupported owner accepted")
		}
	}
	owners, err = parseCodexLocks([]byte("p123\nf3u\nn/example/other/thread-writer-locks/"+codexTestSession+".lock\n"), home)
	if err != nil || len(owners) != 0 {
		t.Fatal("cross-account writer accepted")
	}
}

func TestCodexReadOnlyThreadMetadata(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 unavailable")
	}
	for _, tc := range []struct {
		mode   string
		accept bool
	}{
		{"minimum version", true},
		{"newer version", true},
		{"missing", false},
		{"linked", false},
		{"source", false},
		{"below minimum version", false},
		{"malformed version", false},
		{"outside", false},
		{"missing edges", false},
	} {
		mode := tc.mode
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "state_5.sqlite")
			transcript := filepath.Join(home, "sessions", "synthetic.jsonl")
			source, version := "cli", "0.153.4"
			if mode == "source" {
				source = "exec"
			}
			if mode == "newer version" {
				version = "1.0.0"
			}
			if mode == "below minimum version" {
				version = "0.153.3"
			}
			if mode == "malformed version" {
				version = "0.153"
			}
			if mode == "outside" {
				transcript = "/example/outside.jsonl"
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
			query := "CREATE TABLE threads(id TEXT,rollout_path TEXT,source TEXT,cli_version TEXT,cwd TEXT);"
			if mode != "missing edges" {
				query += "CREATE TABLE thread_spawn_edges(parent_thread_id TEXT,child_thread_id TEXT,status TEXT);"
			}
			if mode != "missing" {
				query += "INSERT INTO threads VALUES(" + quote(codexTestSession) + "," + quote(transcript) + "," + quote(source) + "," + quote(version) + ",'/example/project');"
			}
			if mode == "linked" {
				query += "INSERT INTO thread_spawn_edges VALUES(" + quote(codexTestSession) + "," + quote(codexTestUser) + ",'synthetic');"
			}
			if out, err := exec.Command("sqlite3", "-init", "/dev/null", path, query).CombinedOutput(); err != nil {
				t.Fatalf("synthetic metadata creation: %v %s", err, out)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(home)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			thread, err := readCodexThread(context.Background(), root, Account{Provider: "codex", Key: "codex", Home: home}, codexTestSession)
			if (err == nil) != tc.accept {
				t.Fatalf("metadata eligibility mismatch: %v", err)
			}
			if err == nil && thread.Transcript != transcript {
				t.Fatal("transcript mapping changed")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("read-only metadata query changed database")
			}
		})
	}
}

func TestCodexReadOnlyWALMetadataAfterHelperStarts(t *testing.T) {
	if os.Getenv("QUOTA_CODEX_INTEGRATION_TEST") != "1" {
		t.Skip("requires an isolated agent environment; set QUOTA_CODEX_INTEGRATION_TEST=1")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 unavailable")
	}
	home := t.TempDir()
	path := filepath.Join(home, "state_5.sqlite")
	transcript := filepath.Join(home, "sessions", "synthetic.jsonl")
	executable, err := codexNativeExecutable()
	if err != nil {
		t.Skip("native Codex unavailable")
	}
	account := Account{Provider: "codex", Key: "codex", Home: home}
	_, closeInitial, err := startCodexRPC(context.Background(), account, executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := closeInitial(); err != nil {
		t.Fatal(err)
	}
	query := "PRAGMA journal_mode=WAL; INSERT INTO threads(id,rollout_path,created_at,updated_at,source,model_provider,cwd,title,sandbox_policy,approval_mode,cli_version) VALUES('" + codexTestSession + "','" + transcript + "',1,1,'cli','openai','/example/project','Synthetic','read-only','never','0.153.4');"
	if out, err := exec.Command("sqlite3", "-init", "/dev/null", path, query).CombinedOutput(); err != nil {
		t.Fatalf("synthetic WAL creation: %v %s", err, out)
	}
	if out, err := exec.Command("sqlite3", "-init", "/dev/null", path, "PRAGMA wal_checkpoint(TRUNCATE);").CombinedOutput(); err != nil {
		t.Fatalf("synthetic WAL checkpoint: %v %s", err, out)
	}
	for _, suffix := range []string{"-shm", "-wal"} {
		if err := os.Rename(path+suffix, path+suffix+".saved"); err != nil && !os.IsNotExist(err) {
			t.Fatalf("synthetic WAL sidecar isolation: %v", err)
		}
	}
	_, closeHelper, err := startCodexRPC(context.Background(), account, executable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeHelper(); err != nil {
			t.Error(err)
		}
	}()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	thread, err := readCodexThread(context.Background(), root, account, codexTestSession)
	if err != nil || thread.Transcript != transcript {
		t.Fatalf("read-only metadata after helper startup: %+v %v", thread, err)
	}
}

func TestCodexLiveLockObservation(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "thread-writer-locks")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, codexTestSession+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	owners, err := readCodexLocks(context.Background(), root, home)
	if err != nil {
		t.Skip("lsof is unavailable in this sandbox: " + err.Error())
	}
	if len(owners[codexTestSession]) != 1 || owners[codexTestSession][0] != os.Getpid() {
		t.Fatalf("real open file PID not observed: %v", owners)
	}
	data, err := codexLsof(context.Background(), "-a", "-p", strconv.Itoa(os.Getpid()), "-d", "txt", "-Fpn")
	if err != nil {
		t.Fatal(err)
	}
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !sameRuntimeExecutable(runtimeExecutablePath(data, os.Getpid()), path) {
		t.Fatal("actual native executable not corroborated")
	}
}

func TestCodexCompletionNeverConfirmsAcceptanceOrRewrittenLogs(t *testing.T) {
	for _, mode := range []string{"accepted only", "other input", "partial", "interrupted", "truncated", "replaced", "prefix changed"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			root, err := os.OpenRoot(home)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			at := time.Now().Add(-time.Minute).UTC()
			baseline := codexTestData(t, codexTestRecords(at), at)
			path := filepath.Join(home, "transcript.jsonl")
			if err := os.WriteFile(path, baseline, 0600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			state, err := parseCodexTranscript(context.Background(), baseline, codexTestSession, "/example/project")
			if err != nil {
				t.Fatal(err)
			}
			e := codexEvidence{info: info, data: baseline, state: state, thread: codexThread{ID: codexTestSession, CWD: "/example/project", Transcript: path}}
			c := Candidate{Provider: "codex", Home: home, SessionID: codexTestSession, PID: os.Getpid(), Transcript: path}
			a := Account{Provider: "codex", Home: home}
			next := codexTestRecords(at.Add(time.Second))
			for _, r := range next {
				p := r["payload"].(map[string]any)
				if _, exists := p["turn_id"]; exists {
					p["turn_id"] = codexTestUser
				}
			}
			want := "55555555-5555-4555-8555-555555555555"
			codexTestPayload(next, 4)["item"].(map[string]any)["client_id"] = want
			var tail []byte
			switch mode {
			case "other input":
				codexTestPayload(next, 4)["item"].(map[string]any)["client_id"] = codexTestClient
				tail = codexTestData(t, next[1:], at.Add(time.Second))
			case "partial":
				tail = []byte("{\"type\":")
			case "interrupted":
				next[7]["payload"] = map[string]any{"type": "error"}
				tail = codexTestData(t, next[1:], at.Add(time.Second))
			case "prefix changed":
				tail = codexTestData(t, next[1:], at.Add(time.Second))
			}
			if len(tail) > 0 {
				if err := os.WriteFile(path, append(append([]byte(nil), baseline...), tail...), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "truncated":
				if err := os.Truncate(path, 0); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				if err := os.Rename(path, path+".preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, baseline, 0600); err != nil {
					t.Fatal(err)
				}
			case "prefix changed":
				f, err := os.OpenFile(path, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.WriteAt([]byte(" "), 0); err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 650*time.Millisecond)
			defer cancel()
			if _, err := awaitCodexCompletion(ctx, root, a, c, "/example/unused", e, want); err == nil {
				t.Fatal("unconfirmed dispatch reported complete")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("confirmation altered transcript")
			}
		})
	}
}

func TestCodexMalformedMessageAndIndependentLockExclusion(t *testing.T) {
	at := time.Now().Add(-time.Minute).UTC()
	for _, index := range []int{4, 5} {
		r := codexTestRecords(at)
		delete(codexTestPayload(r, index)["item"].(map[string]any), "content")
		if _, err := parseCodexTranscript(context.Background(), codexTestData(t, r, at), codexTestSession, "/example/project"); err == nil {
			t.Fatal("missing message content accepted")
		}
	}
	home := "/example/account"
	data := "p123\nf3u\nn" + filepath.Join(home, "thread-writer-locks", codexTestSession+".lock") + "\nf4u\nn" + filepath.Join(home, "thread-writer-locks", "unsupported.lock") + "\n"
	owners, err := parseCodexLocks([]byte(data), home)
	if err == nil || len(owners[codexTestSession]) != 1 {
		t.Fatal("independent owner was suppressed by unsupported lock")
	}
	if codexSoleWriter(owners, codexTestSession, 123) {
		t.Fatal("unsupported lock on the same process lost its ambiguous ownership")
	}
	data = strings.Replace(data, "\nf4u\n", "\np456\nf4u\n", 1)
	owners, err = parseCodexLocks([]byte(data), home)
	if err == nil || !codexSoleWriter(owners, codexTestSession, 123) {
		t.Fatal("unrelated unsupported lock suppressed a verified writer")
	}
}
