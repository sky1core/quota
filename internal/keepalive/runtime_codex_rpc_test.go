//go:build darwin || linux

package keepalive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexTestRPC(t *testing.T, reply func(map[string]any) any) *codexRPC {
	t.Helper()
	client, server := net.Pipe()
	rpc := newCodexRPC(client, client)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		s := bufio.NewScanner(server)
		for s.Scan() {
			var request map[string]any
			if json.Unmarshal(s.Bytes(), &request) != nil {
				t.Error("invalid local request")
				return
			}
			response := reply(request)
			if response == nil {
				continue
			}
			if err := json.NewEncoder(server).Encode(response); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { rpc.close(); server.Close(); <-done })
	return rpc
}

func codexTestReply(request map[string]any, result any) any {
	return map[string]any{"id": request["id"], "result": result}
}
func codexTestEntry(id, client, text string) any {
	return map[string]any{"id": id, "clientUserMessageId": client, "input": []any{map[string]any{"type": "text", "text": text}}}
}

func TestCodexRPCQueueProtocolAndOwnCleanup(t *testing.T) {
	step := 0
	rpc := codexTestRPC(t, func(request map[string]any) any {
		step++
		method := request["method"]
		params := request["params"].(map[string]any)
		switch step {
		case 1:
			if method != "initialize" || params["capabilities"].(map[string]any)["experimentalApi"] != true {
				t.Error("experimental initialization missing")
			}
			return codexTestReply(request, map[string]any{"userAgent": "synthetic"})
		case 2:
			if method != "initialized" {
				t.Error("initialized notification missing")
			}
			return nil
		case 3:
			if method != "thread/queue/list" || params["threadId"] != codexTestSession {
				t.Error("queue/list targeted wrong thread")
			}
			return codexTestReply(request, map[string]any{"data": []any{}, "nextCursor": nil})
		case 4:
			if method != "thread/queue/add" || len(params) != 3 || params["threadId"] != codexTestSession || params["clientUserMessageId"] != codexTestClient {
				t.Error("add changed routing or identity")
			}
			input := params["input"].([]any)
			if len(input) != 1 || len(input[0].(map[string]any)) != 2 || input[0].(map[string]any)["text"] != "Synthetic\ninput" {
				t.Error("add changed input or added model controls")
			}
			return codexTestReply(request, map[string]any{"queuedSubmission": codexTestEntry("own-queue", codexTestClient, "Synthetic\ninput")})
		case 5:
			if method != "thread/queue/list" {
				t.Error("cleanup did not list queue")
			}
			return codexTestReply(request, map[string]any{"data": []any{codexTestEntry("user-queue", codexTestUser, "User input")}, "nextCursor": "synthetic-page"})
		case 6:
			if method != "thread/queue/list" || params["cursor"] != "synthetic-page" {
				t.Error("cleanup pagination lost")
			}
			return codexTestReply(request, map[string]any{"data": []any{codexTestEntry("own-queue", codexTestClient, "Synthetic\ninput")}, "nextCursor": nil})
		case 7:
			if method != "thread/queue/delete" || params["threadId"] != codexTestSession || params["queuedSubmissionId"] != "own-queue" || len(params) != 2 {
				t.Error("cleanup touched another input")
			}
			return codexTestReply(request, map[string]any{"deleted": true})
		default:
			t.Error("unexpected RPC")
			return nil
		}
	})
	ctx := context.Background()
	if err := rpc.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	queue, err := rpc.queue(ctx, codexTestSession)
	if err != nil || len(queue) != 0 {
		t.Fatal(err)
	}
	if err := rpc.add(ctx, codexTestSession, codexTestClient, "Synthetic\ninput", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	cleanup, cancelCleanup := context.WithTimeout(context.WithoutCancel(canceled), time.Second)
	defer cancelCleanup()
	if err := rpc.removeOwn(cleanup, codexTestSession, codexTestClient, "Synthetic\ninput"); err != nil {
		t.Fatal(err)
	}
	if step != 7 {
		t.Fatal("native protocol steps incomplete")
	}
}

func TestCodexRPCExcludesUnsupportedQueueAndAcceptance(t *testing.T) {
	for _, result := range []any{map[string]any{}, map[string]any{"data": nil}, map[string]any{"data": "unknown"}, map[string]any{"data": []any{}, "nextCursor": 17}, map[string]any{"data": []any{}, "nextCursor": ""}, map[string]any{"data": []any{map[string]any{"id": "incomplete"}}}} {
		rpc := codexTestRPC(t, func(request map[string]any) any { return codexTestReply(request, result) })
		if _, err := rpc.queue(context.Background(), codexTestSession); err == nil {
			t.Fatal("unknown queue became empty")
		}
	}
	rpc := codexTestRPC(t, func(request map[string]any) any {
		return codexTestReply(request, map[string]any{"data": []any{}, "nextCursor": "repeated"})
	})
	if _, err := rpc.queue(context.Background(), codexTestSession); err == nil {
		t.Fatal("repeated cursor accepted")
	}
	for _, result := range []any{map[string]any{}, map[string]any{"queuedSubmission": codexTestEntry("entry", codexTestUser, "Synthetic")}, map[string]any{"queuedSubmission": codexTestEntry("entry", codexTestClient, "Changed")}} {
		rpc := codexTestRPC(t, func(request map[string]any) any { return codexTestReply(request, result) })
		if err := rpc.add(context.Background(), codexTestSession, codexTestClient, "Synthetic", func() bool { return true }); err == nil {
			t.Fatal("mismatched acceptance confirmed")
		}
	}
}

func TestCodexRPCCleanupNeverDeletesOtherInputs(t *testing.T) {
	for _, entries := range [][]any{
		{codexTestEntry("user", codexTestUser, "User input")},
		{codexTestEntry("own", codexTestClient, "Changed input")},
		{codexTestEntry("one", codexTestClient, "Synthetic"), codexTestEntry("two", codexTestClient, "Synthetic")},
	} {
		rpc := codexTestRPC(t, func(request map[string]any) any {
			if request["method"] != "thread/queue/list" {
				t.Error("unsafe queue mutation")
			}
			return codexTestReply(request, map[string]any{"data": entries, "nextCursor": nil})
		})
		if err := rpc.removeOwn(context.Background(), codexTestSession, codexTestClient, "Synthetic"); err == nil {
			t.Fatal("uncertain consumption or ownership hidden")
		}
	}
	rpc := codexTestRPC(t, func(request map[string]any) any {
		if request["method"] == "thread/queue/list" {
			return codexTestReply(request, map[string]any{"data": []any{codexTestEntry("own", codexTestClient, "Synthetic")}})
		}
		return codexTestReply(request, map[string]any{"deleted": false})
	})
	if err := rpc.removeOwn(context.Background(), codexTestSession, codexTestClient, "Synthetic"); err == nil {
		t.Fatal("consumption/delete race hidden")
	}
}

func TestCodexRPCPCCheckAndForbiddenMethodsWriteNothing(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	rpc := newCodexRPC(client, client)
	defer rpc.close()
	called := false
	if err := rpc.add(context.Background(), codexTestSession, codexTestClient, "Synthetic", func() bool { called = true; return false }); err == nil || !called {
		t.Fatal("PC check ignored")
	}
	for _, method := range []string{"thread/start", "thread/resume", "thread/fork", "turn/start", "thread/queue/start", "thread/queue/update", "model/list", "account/rateLimits/read"} {
		var result any
		if err := rpc.call(context.Background(), method, map[string]any{}, &result); err == nil {
			t.Fatal("forbidden helper operation accepted")
		}
	}
	server.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	var b [1]byte
	if n, err := server.Read(b[:]); n != 0 || err == nil {
		t.Fatal("request written despite PC or method restriction")
	}
}

func TestCodexRPCCancellationAndPrivateErrors(t *testing.T) {
	for _, phase := range []string{"write", "reply"} {
		client, server := net.Pipe()
		rpc := newCodexRPC(client, client)
		if phase == "reply" {
			go func() { bufio.NewReader(server).ReadBytes('\n') }()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		_, err := rpc.queue(ctx, codexTestSession)
		cancel()
		rpc.close()
		server.Close()
		if !errors.Is(err, context.DeadlineExceeded) || !rpc.stopped {
			t.Fatalf("%s cancellation failed: %v", phase, err)
		}
	}
	for _, mode := range []string{"private error", "wrong id", "oversized", "null"} {
		rpc := codexTestRPC(t, func(request map[string]any) any {
			switch mode {
			case "private error":
				return map[string]any{"id": request["id"], "error": map[string]any{"code": -32600, "message": "SYNTHETIC_PRIVATE_MARKER"}}
			case "wrong id":
				return map[string]any{"id": 999, "result": map[string]any{"data": []any{}}}
			case "oversized":
				return codexTestReply(request, map[string]any{"text": strings.Repeat("x", 1024*1024)})
			default:
				return codexTestReply(request, nil)
			}
		})
		_, err := rpc.queue(context.Background(), codexTestSession)
		if err == nil || strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_MARKER") {
			t.Fatalf("protocol failure exposed output or succeeded: %v", err)
		}
	}
	client, server := net.Pipe()
	rpc := newCodexRPC(client, client)
	server.Close()
	defer rpc.close()
	if _, err := rpc.queue(context.Background(), codexTestSession); err == nil {
		t.Fatal("helper exit ignored")
	}
}

func TestCodexRPCProcess(t *testing.T) {
	if os.Getenv("QUOTA_CODEX_RPC_TEST_CHILD") == "1" {
		if os.Getenv("CODEX_HOME") != os.Getenv("QUOTA_CODEX_RPC_EXPECT_HOME") || os.Getenv("HOME") != os.Getenv("QUOTA_CODEX_RPC_EXPECT_USER_HOME") {
			os.Exit(4)
		}
		if _, set := os.LookupEnv("OPENAI_API_KEY"); set {
			os.Exit(5)
		}
		s := bufio.NewScanner(os.Stdin)
		for s.Scan() {
			var request map[string]any
			if json.Unmarshal(s.Bytes(), &request) != nil {
				os.Exit(6)
			}
			if request["method"] == "initialized" {
				continue
			}
			result := map[string]any{"userAgent": "synthetic"}
			if request["method"] == "thread/queue/list" {
				result = map[string]any{"data": []any{}}
			}
			json.NewEncoder(os.Stdout).Encode(codexTestReply(request, result))
		}
		if err := os.WriteFile(os.Getenv("QUOTA_CODEX_RPC_EXIT_MARKER"), []byte("stdin closed"), 0600); err != nil {
			os.Exit(7)
		}
		return
	}
	home := t.TempDir()
	userHome := t.TempDir()
	marker := filepath.Join(t.TempDir(), "exit")
	path := filepath.Join(t.TempDir(), "synthetic-helper")
	script := "#!/bin/sh\n[ \"$1\" = app-server ] && [ \"$2\" = --stdio ] || exit 8\nexec \"$QUOTA_CODEX_RPC_TEST_BINARY\" -test.run=^TestCodexRPCProcess$\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("QUOTA_CODEX_RPC_TEST_CHILD", "1")
	t.Setenv("QUOTA_CODEX_RPC_TEST_BINARY", binary)
	t.Setenv("QUOTA_CODEX_RPC_EXPECT_HOME", home)
	t.Setenv("QUOTA_CODEX_RPC_EXPECT_USER_HOME", userHome)
	t.Setenv("QUOTA_CODEX_RPC_EXIT_MARKER", marker)
	t.Setenv("HOME", userHome)
	t.Setenv("CODEX_HOME", "/example/wrong-account")
	t.Setenv("OPENAI_API_KEY", "synthetic-override")
	rpc, closeHelper, err := startCodexRPC(context.Background(), Account{Provider: "codex", Key: "codex", Home: home}, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rpc.queue(context.Background(), codexTestSession); err != nil {
		t.Error(err)
	}
	if err := closeHelper(); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "stdin closed" {
		t.Fatal("helper did not observe EOF and exit")
	}
}

func TestCodexRPCShortWrite(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	rpc := newCodexRPC(codexShortWriter{}, reader)
	defer rpc.close()
	if err := rpc.write(context.Background(), map[string]any{"synthetic": true}); err == nil {
		t.Fatal("short write accepted")
	}
}

type codexShortWriter struct{}

func (codexShortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }
func (codexShortWriter) Close() error                { return nil }

func TestCodexRPCProtocolErrorRetainsIndependentThread(t *testing.T) {
	count := 0
	rpc := codexTestRPC(t, func(request map[string]any) any {
		count++
		if count == 1 {
			return map[string]any{"id": request["id"], "error": map[string]any{"code": -32000, "message": "synthetic thread unavailable"}}
		}
		return codexTestReply(request, map[string]any{"data": []any{}})
	})
	if _, err := rpc.queue(context.Background(), codexTestSession); err == nil {
		t.Fatal("thread error hidden")
	}
	if queue, err := rpc.queue(context.Background(), codexTestUser); err != nil || len(queue) != 0 {
		t.Fatal(fmt.Sprintf("independent thread suppressed: %v", err))
	}
}
