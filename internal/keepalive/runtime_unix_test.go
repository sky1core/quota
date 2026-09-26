//go:build darwin || linux

package keepalive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func syntheticRegistry() claudeRegistry {
	return claudeRegistry{PID: 123, SessionID: runtimeTestSession, CWD: "/example/project", StartedAt: 1893585600000, ProcStart: "Wed Jan 2 12:00:00 2030", Version: "2.1.266", Kind: "interactive", Status: "idle", MessagingSocketPath: "/example/session.sock"}
}

func TestClaudeRegistryRejectsNonIdleAndUnknownShapes(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*claudeRegistry)
		err  bool
	}{
		{"busy", func(r *claudeRegistry) { r.Status = "busy" }, false},
		{"dialog", func(r *claudeRegistry) { r.Status, r.WaitingFor = "waiting", "dialog open" }, false},
		{"contradictory dialog", func(r *claudeRegistry) { r.WaitingFor = "dialog open" }, false},
		{"unknown status", func(r *claudeRegistry) { r.Status = "unrecognized" }, true},
		{"shell", func(r *claudeRegistry) { r.Status = "shell" }, false},
		{"headless", func(r *claudeRegistry) { r.Kind = "headless" }, false},
		{"background", func(r *claudeRegistry) { r.Kind = "bg" }, false},
		{"subagent", func(r *claudeRegistry) { r.Agent = "example-agent" }, false},
		{"spare", func(r *claudeRegistry) { r.Spare = true }, false},
		{"blocked", func(r *claudeRegistry) { r.Tempo = "blocked" }, false},
		{"needs", func(r *claudeRegistry) { r.Needs = "input" }, false},
		{"pid", func(r *claudeRegistry) { r.PID = 321 }, true},
		{"missing start", func(r *claudeRegistry) { r.ProcStart = "" }, true},
		{"session traversal", func(r *claudeRegistry) { r.SessionID = "../../other" }, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := syntheticRegistry()
			test.edit(&r)
			got, err := parseClaudeRegistry(runtimeJSON(t, r), 123)
			if (err != nil) != test.err || err == nil && got.idle() {
				t.Fatalf("unsafe registry accepted: idle=%v error=%v", got.idle(), err)
			}
		})
	}
	r, err := parseClaudeRegistry(runtimeJSON(t, syntheticRegistry()), 123)
	if err != nil || !r.idle() {
		t.Fatal("known plain idle contract rejected")
	}
}

func TestClaudeRegistryVersionMinimum(t *testing.T) {
	for _, tc := range []struct {
		version string
		accept  bool
	}{
		{"2.1.259", true},
		{"2.1.260", true},
		{"2.2.0", true},
		{"9.9.9", true},
		{"2.1.258", false},
		{"2.0.999", false},
		{"1.9.9", false},
		{"2.1", false},
		{"2.1.259.1", false},
		{"v2.1.259", false},
		{"2.1.x", false},
		{"2.1.259-rc.1", false},
		{"2.1.259+build", false},
		{"", false},
	} {
		r := syntheticRegistry()
		r.Version = tc.version
		got, err := parseClaudeRegistry(runtimeJSON(t, r), 123)
		if tc.accept {
			if err != nil || !got.idle() {
				t.Fatalf("version %q rejected: err=%v idle=%v", tc.version, err, got.idle())
			}
			continue
		}
		if !errors.Is(err, errClaudeRegistryVersion) {
			t.Fatalf("version %q not rejected by version gate: err=%v", tc.version, err)
		}
	}
	r := syntheticRegistry()
	r.Version, r.Status = "9.9.9", "unrecognized"
	if _, err := parseClaudeRegistry(runtimeJSON(t, r), 123); err == nil || errors.Is(err, errClaudeRegistryVersion) {
		t.Fatalf("newer version bypassed status safeguard: err=%v", err)
	}
}

func TestRuntimeProcessMetadata(t *testing.T) {
	for _, test := range []struct {
		line string
		ok   bool
	}{
		{"501 S+ pts/1 Wed Jan  2 12:00:00 2030\n", true},
		{"501 S+ pts/1 Wed Jan 2 12:00:00 2030\n", true},
		{"502 S+ pts/1 Wed Jan 2 12:00:00 2030\n", false},
		{"501 Z pts/1 Wed Jan 2 12:00:00 2030\n", false},
		{"501 T pts/1 Wed Jan 2 12:00:00 2030\n", false},
		{"501 S+ pts/1 unsupported timestamp\n", false},
		{"501 S+ pts/1 Wed Jan 2 12:00:00 2030\n501 S+ pts/1 Wed Jan 2 12:00:00 2030\n", false},
	} {
		_, err := parseRuntimeProcess([]byte(test.line), 501)
		if (err == nil) != test.ok {
			t.Errorf("process shape result: %v", err)
		}
	}
	data := []byte("p123\nftxt\nn/example/cli\nftxt\nn/example/loader\n")
	if runtimeExecutablePath(data, 123) != "/example/cli" || runtimeExecutablePath(data, 321) != "" {
		t.Fatal("executable not correlated to owning PID")
	}
}

func TestRuntimeHarmlessProcess(t *testing.T) {
	if os.Getenv("QUOTA_RUNTIME_TEST_PROCESS") == "1" {
		time.Sleep(3 * time.Second)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestRuntimeHarmlessProcess$")
	cmd.Env = append(os.Environ(), "QUOTA_RUNTIME_TEST_PROCESS=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := cmd.Wait(); err != nil {
			t.Error("harmless process did not exit normally")
		}
	})
	if !runtimeAlive(cmd.Process.Pid) {
		t.Fatal("live harmless process not detected")
	}
	data, err := runtimeCommand(context.Background(), "lsof", []string{"-n", "-P", "-a", "-p", strconv.Itoa(cmd.Process.Pid), "-d", "txt", "-Fpn"})
	if err != nil {
		t.Skip("process inspection unavailable in this sandbox")
	}
	path := runtimeExecutablePath(data, cmd.Process.Pid)
	if !sameRuntimeExecutable(path, executable) {
		t.Fatal("live process executable mismatch")
	}
	process, err := readRuntimeProcess(context.Background(), cmd.Process.Pid)
	if err != nil {
		t.Skip("full process start verification unavailable in this sandbox: " + err.Error())
	}
	if process.Start == "" || !sameRuntimeExecutable(process.Executable, executable) {
		t.Fatal("process identity missing")
	}
}

func TestRuntimeFileAndTranscriptBoundaries(t *testing.T) {
	home := t.TempDir()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, project := range []string{"one", "two"} {
		if err := os.MkdirAll(filepath.Join(home, "projects", project), 0700); err != nil {
			t.Fatal(err)
		}
	}
	name := filepath.Join("projects", "one", runtimeTestSession+".jsonl")
	data := runtimeTurn(t, runtimeTestUser, runtimeTestReply, time.Now().Add(-time.Minute))
	if err := os.WriteFile(filepath.Join(home, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := findClaudeTranscript(context.Background(), root, runtimeTestSession); err != nil || got != name {
		t.Fatalf("synthetic transcript discovery: %s %v", got, err)
	}
	if _, err := readRuntimeFile(context.Background(), root, name, 1); err == nil {
		t.Fatal("size limit not enforced")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("SYNTHETIC_PRIVATE_MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeFile(context.Background(), root, "link", 1024); err == nil {
		t.Fatal("outside symlink followed")
	}
	if err := syscall.Mkfifo(filepath.Join(home, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeFile(context.Background(), root, "pipe", 1024); err == nil {
		t.Fatal("nonregular metadata accepted")
	}
	if f, err := openRuntimeDirectory(root, "pipe"); err == nil {
		f.Close()
		t.Fatal("non-directory registry accepted")
	}
	other := filepath.Join(home, "projects", "two", runtimeTestSession+".jsonl")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := findClaudeTranscript(context.Background(), root, runtimeTestSession); err == nil {
		t.Fatal("ambiguous transcripts accepted")
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "SYNTHETIC_PRIVATE_MARKER" {
		t.Fatal("outside data modified")
	}
}

func runtimeTestSocket(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "quota-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "session.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			t.Skip("Unix socket listening is blocked by this sandbox")
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener, socket
}

func TestClaudeSocketFrameAndPeerIdentity(t *testing.T) {
	listener, socket := runtimeTestSocket(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !runtimeSocketOwned(ctx, os.Getpid(), socket) || runtimeSocketOwned(ctx, os.Getppid(), socket) {
		t.Fatal("socket ownership did not match the listening process")
	}
	conn, err := connectRuntimeSocket(ctx, socket, os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	server, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := sendClaudeRuntimeFrame(ctx, conn, runtimeTestSession, runtimeTestAuto, "Synthetic\nmessage", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	server.SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(server).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var frame map[string]any
	if json.Unmarshal(line, &frame) != nil || len(frame) != 5 || frame["type"] != "user" || frame["session_id"] != runtimeTestSession || frame["uuid"] != runtimeTestAuto || frame["from"] != "quota-keepalive" {
		t.Fatal("wire frame changed session/correlation or added control fields")
	}
	message, ok := frame["message"].(map[string]any)
	if !ok || message["role"] != "user" || message["content"] != "Synthetic\nmessage" {
		t.Fatal("message encoding changed")
	}
	if c, err := connectRuntimeSocket(ctx, socket, os.Getpid()+1); err == nil {
		c.Close()
		t.Fatal("wrong socket PID accepted")
	}
}

func TestFrameRechecksPCBeforeWriting(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	called := false
	err := sendClaudeRuntimeFrame(context.Background(), client, runtimeTestSession, runtimeTestAuto, "Synthetic input", func() bool { called = true; return false })
	if err == nil || !called {
		t.Fatal("final inactivity check was not enforced")
	}
	server.SetReadDeadline(time.Now().Add(10 * time.Millisecond))
	var b [1]byte
	if n, err := server.Read(b[:]); n != 0 || err == nil {
		t.Fatal("frame was sent after inactivity check failed")
	}
}

func TestUnsupportedRegistryRetainsOtherSessionsAndOwnership(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	for i, pid := range []int{os.Getpid(), os.Getppid()} {
		r := syntheticRegistry()
		r.PID = pid
		if i == 0 {
			r.Version = "unsupported"
		}
		if err := os.WriteFile(filepath.Join(home, "sessions", strconv.Itoa(pid)+".json"), runtimeJSON(t, r), 0600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	records, err := readClaudeRegistries(context.Background(), root)
	if !errors.Is(err, errClaudeRegistryVersion) || len(records) != 2 {
		t.Fatalf("partial registry was discarded: %d %v", len(records), err)
	}
	idle, ownership := 0, 0
	for _, r := range records {
		if r.idle() {
			idle++
		}
		if r.SessionID == runtimeTestSession {
			ownership++
		}
	}
	if idle != 1 || ownership != 2 {
		t.Fatal("unsupported session became eligible or lost its ownership claim")
	}
}

func TestClaudeSocketCancellationAndUnconfirmedWrite(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sendClaudeRuntimeFrame(ctx, client, runtimeTestSession, runtimeTestAuto, "Synthetic input", func() bool { return true }); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled frame was written")
	}
	server.Close()
	if err := sendClaudeRuntimeFrame(context.Background(), client, runtimeTestSession, runtimeTestAuto, "Synthetic input", func() bool { return true }); err == nil {
		t.Fatal("failed socket write was confirmed")
	}
}

func TestClaudeUnixSocketPairFrame(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	connections := make([]net.Conn, 0, 2)
	for _, fd := range fds {
		f := os.NewFile(uintptr(fd), "synthetic-socket")
		conn, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		t.Cleanup(func() { conn.Close() })
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sendClaudeRuntimeFrame(ctx, connections[0], runtimeTestSession, runtimeTestAuto, "Synthetic input", func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	connections[1].SetReadDeadline(time.Now().Add(time.Second))
	line, err := bufio.NewReader(connections[1]).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var frame struct {
		SessionID string `json:"session_id"`
		UUID      string `json:"uuid"`
		Message   struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &frame) != nil || frame.SessionID != runtimeTestSession || frame.UUID != runtimeTestAuto || frame.Message.Content != "Synthetic input" {
		t.Fatal("Unix stream transport did not preserve the frame")
	}
}

func TestClaudeCompletionRequiresCorrelatedEndAndPreservesLogs(t *testing.T) {
	for _, mode := range []string{"complete", "enqueued", "other turn", "approval", "truncated", "replaced", "prefix changed"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			root, err := os.OpenRoot(home)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			at := time.Now().Add(-time.Minute)
			baseline := runtimeTurn(t, runtimeTestUser, runtimeTestReply, at)
			name := "transcript.jsonl"
			path := filepath.Join(home, name)
			if err := os.WriteFile(path, baseline, 0600); err != nil {
				t.Fatal(err)
			}
			record := syntheticRegistry()
			record.PID = os.Getpid()
			if mode == "approval" {
				record.Status, record.WaitingFor = "waiting", "dialog open"
			}
			if err := os.Mkdir(filepath.Join(home, "sessions"), 0700); err != nil {
				t.Fatal(err)
			}
			registryPath := filepath.Join(home, "sessions", strconv.Itoa(record.PID)+".json")
			if err := os.WriteFile(registryPath, runtimeJSON(t, record), 0600); err != nil {
				t.Fatal(err)
			}
			f, info, err := openRuntimeFile(root, name, claudeTranscriptLimit)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if _, err := f.Seek(int64(len(baseline)), io.SeekStart); err != nil {
				t.Fatal(err)
			}
			state, err := parseClaudeTranscript(context.Background(), baseline, runtimeTestSession)
			if err != nil {
				t.Fatal(err)
			}
			var appendData []byte
			switch mode {
			case "complete", "prefix changed":
				appendData = runtimeTurn(t, runtimeTestAuto, runtimeTestAnswer, at.Add(10*time.Second))
			case "other turn":
				appendData = runtimeTurn(t, runtimeTestAnswer, runtimeTestReply, at.Add(10*time.Second))
			case "enqueued":
				appendData = runtimeJSON(t, map[string]any{"type": "queue-operation", "operation": "enqueue", "sessionId": runtimeTestSession})
			}
			writer, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(appendData); err != nil {
				t.Fatal(err)
			}
			writer.Close()
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
				writer, err := os.OpenFile(path, os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.WriteAt([]byte(" "), 0); err != nil {
					t.Fatal(err)
				}
				writer.Close()
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 650*time.Millisecond)
			defer cancel()
			_, err = awaitClaudeCompletion(ctx, root, name, f, info, baseline, state, record, runtimeTestAuto)
			if (err == nil) != (mode == "complete") {
				t.Fatalf("completion evidence result: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatal("confirmation changed logs")
			}
		})
	}
}
