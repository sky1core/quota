package quotacache_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
)

const probeWait = 5 * time.Second

type probeMessage struct {
	Kind    string `json:"kind"`
	Account string `json:"account"`
	Agent   string `json:"agent"`
}

type probeEvent struct {
	probeMessage
	release chan string
}

type callerResult struct {
	Left     int    `json:"left"`
	Err      string `json:"err,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
}

func TestMain(m *testing.M) {
	if agent := filepath.Base(os.Args[0]); agent == "claude" || agent == "codex" {
		os.Exit(runSyntheticCLI(agent))
	}
	if os.Getenv("QUOTA_PROBE_TEST_ROLE") == "caller" {
		os.Exit(runProbeCaller())
	}
	os.Exit(m.Run())
}

func runProbeCaller() int {
	agent := os.Getenv("QUOTA_PROBE_TEST_AGENT")
	account := os.Getenv("QUOTA_PROBE_TEST_ACCOUNT")
	if _, err := exchangeProbeMessage(probeMessage{Kind: "call", Agent: agent, Account: account}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	var result map[string]any
	var err error
	switch agent {
	case "claude":
		result, err = claude.GetQuotaForConfigDir(ctx, probeWait, account, time.Minute)
	case "codex":
		result, err = codex.GetQuotaForHome(ctx, probeWait, account, time.Minute)
	default:
		err = fmt.Errorf("unknown synthetic agent %q", agent)
	}
	out := callerResult{Canceled: errors.Is(err, context.Canceled)}
	if err != nil {
		out.Err = err.Error()
	} else {
		windows, ok := result["windows"].([]map[string]any)
		if !ok || len(windows) == 0 {
			out.Err = fmt.Sprintf("missing windows: %v", result)
		} else if left, ok := windows[0]["left"].(int); ok {
			out.Left = left
		} else {
			out.Err = fmt.Sprintf("invalid left: %v", windows[0]["left"])
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runSyntheticCLI(agent string) int {
	account := os.Getenv("CLAUDE_CONFIG_DIR")
	if agent == "codex" {
		account = os.Getenv("CODEX_HOME")
		r := bufio.NewScanner(os.Stdin)
		for r.Scan() {
			var request struct {
				ID int `json:"id"`
			}
			if json.Unmarshal(r.Bytes(), &request) != nil {
				return 1
			}
			switch request.ID {
			case 1:
				fmt.Println(`{"jsonrpc":"2.0","id":1,"result":{}}`)
			case 2:
				reply, err := exchangeProbeMessage(probeMessage{Kind: "probe", Agent: agent, Account: account})
				if err != nil || reply != "ok" {
					return 1
				}
				fmt.Println(`{"jsonrpc":"2.0","id":2,"result":{"rateLimits":{"primary":{"usedPercent":10,"windowDurationMins":300}}}}`)
				return 0
			}
		}
		return 1
	}
	reply, err := exchangeProbeMessage(probeMessage{Kind: "probe", Agent: agent, Account: account})
	if err != nil || reply != "ok" {
		return 1
	}
	fmt.Println(`{"result":"Current session: 10% used\n","is_error":false}`)
	return 0
}

func exchangeProbeMessage(message probeMessage) (string, error) {
	conn, err := net.DialTimeout("tcp", os.Getenv("QUOTA_PROBE_TEST_ADDR"), probeWait)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(probeWait))
	if err := json.NewEncoder(conn).Encode(message); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	return strings.TrimSpace(line), err
}

type probeServer struct {
	addr   string
	calls  chan probeMessage
	probes chan probeEvent
	done   chan struct{}
}

func startProbeServer(t *testing.T) *probeServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &probeServer{
		addr: listener.Addr().String(), calls: make(chan probeMessage, 16),
		probes: make(chan probeEvent, 16), done: make(chan struct{}),
	}
	t.Cleanup(func() { close(server.done); _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go server.handle(conn)
		}
	}()
	return server
}

func (server *probeServer) handle(conn net.Conn) {
	defer conn.Close()
	var message probeMessage
	if json.NewDecoder(conn).Decode(&message) != nil {
		return
	}
	if message.Kind == "call" {
		select {
		case server.calls <- message:
		case <-server.done:
			return
		}
		_, _ = fmt.Fprintln(conn, "ok")
		return
	}
	event := probeEvent{probeMessage: message, release: make(chan string, 1)}
	select {
	case server.probes <- event:
	case <-server.done:
		return
	}
	select {
	case reply := <-event.release:
		_, _ = fmt.Fprintln(conn, reply)
	case <-server.done:
	}
}

type probeCaller struct {
	stdin io.WriteCloser
	done  chan callerCompletion
}

type callerCompletion struct {
	result callerResult
	err    error
}

func startProbeCaller(t *testing.T, server *probeServer, agent, account, home, binDir string) *probeCaller {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable)
	cmd.Env = []string{
		"HOME=" + home, "PATH=" + binDir, "QUOTA_PROBE_TEST_ROLE=caller",
		"QUOTA_PROBE_TEST_AGENT=" + agent, "QUOTA_PROBE_TEST_ACCOUNT=" + account,
		"QUOTA_PROBE_TEST_ADDR=" + server.addr,
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	caller := &probeCaller{stdin: stdin, done: make(chan callerCompletion, 1)}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = stdin.Close() })
	go func() {
		err := cmd.Wait()
		_ = stdin.Close()
		var result callerResult
		if err == nil {
			err = json.Unmarshal(stdout.Bytes(), &result)
		}
		if err != nil {
			err = fmt.Errorf("caller failed: %w; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
		caller.done <- callerCompletion{result: result, err: err}
	}()
	return caller
}

func waitCall(t *testing.T, server *probeServer, agent, account string) {
	t.Helper()
	select {
	case message := <-server.calls:
		if message.Agent != agent || message.Account != account {
			t.Fatalf("call = %+v, want %s %s", message, agent, account)
		}
	case <-time.After(probeWait):
		t.Fatal("caller did not start")
	}
}

func waitProbe(t *testing.T, server *probeServer, agent, account string) probeEvent {
	t.Helper()
	select {
	case event := <-server.probes:
		if event.Kind != "probe" || event.Agent != agent || event.Account != account {
			t.Fatalf("probe = %+v, want %s %s", event.probeMessage, agent, account)
		}
		return event
	case <-time.After(probeWait):
		t.Fatal("probe did not start")
		return probeEvent{}
	}
}

func waitResult(t *testing.T, caller *probeCaller) callerResult {
	t.Helper()
	select {
	case completion := <-caller.done:
		if completion.err != nil {
			t.Fatal(completion.err)
		}
		return completion.result
	case <-time.After(probeWait):
		t.Fatal("caller did not return")
		return callerResult{}
	}
}

func assertNoProbe(t *testing.T, server *probeServer) {
	t.Helper()
	select {
	case event := <-server.probes:
		event.release <- "fail"
		t.Fatalf("unexpected extra probe: %+v", event.probeMessage)
	default:
	}
}

func probeEnvironment(t *testing.T) (home, binDir string, server *probeServer) {
	t.Helper()
	home, binDir = t.TempDir(), t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.Symlink(executable, filepath.Join(binDir, name)); err != nil {
			t.Fatal(err)
		}
	}
	return home, binDir, startProbeServer(t)
}

func accountDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCrossProcessProbeCoordination(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			t.Run("same account shares one probe", func(t *testing.T) {
				home, binDir, server := probeEnvironment(t)
				account := accountDirectory(t)
				first := startProbeCaller(t, server, agent, account, home, binDir)
				waitCall(t, server, agent, account)
				probe := waitProbe(t, server, agent, account)
				second := startProbeCaller(t, server, agent, account, home, binDir)
				waitCall(t, server, agent, account)
				select {
				case extra := <-server.probes:
					extra.release <- "fail"
					t.Fatalf("same account started a second probe: %+v", extra.probeMessage)
				case <-time.After(200 * time.Millisecond):
				}
				probe.release <- "ok"
				for _, caller := range []*probeCaller{first, second} {
					if got := waitResult(t, caller); got.Err != "" || got.Left != 90 {
						t.Fatalf("shared result = %+v, want left 90", got)
					}
				}
				assertNoProbe(t, server)
			})
			t.Run("different accounts probe concurrently", func(t *testing.T) {
				home, binDir, server := probeEnvironment(t)
				accountA, accountB := accountDirectory(t), accountDirectory(t)
				first := startProbeCaller(t, server, agent, accountA, home, binDir)
				waitCall(t, server, agent, accountA)
				probeA := waitProbe(t, server, agent, accountA)
				second := startProbeCaller(t, server, agent, accountB, home, binDir)
				waitCall(t, server, agent, accountB)
				probeB := waitProbe(t, server, agent, accountB)
				probeB.release <- "ok"
				if got := waitResult(t, second); got.Err != "" || got.Left != 90 {
					t.Fatalf("independent account = %+v, want left 90", got)
				}
				probeA.release <- "ok"
				if got := waitResult(t, first); got.Err != "" || got.Left != 90 {
					t.Fatalf("first account = %+v, want left 90", got)
				}
				assertNoProbe(t, server)
			})
			t.Run("canceled waiter never probes", func(t *testing.T) {
				home, binDir, server := probeEnvironment(t)
				account := accountDirectory(t)
				first := startProbeCaller(t, server, agent, account, home, binDir)
				waitCall(t, server, agent, account)
				probe := waitProbe(t, server, agent, account)
				waiter := startProbeCaller(t, server, agent, account, home, binDir)
				waitCall(t, server, agent, account)
				if err := waiter.stdin.Close(); err != nil {
					t.Fatal(err)
				}
				if got := waitResult(t, waiter); !got.Canceled {
					t.Fatalf("canceled waiter = %+v, want context cancellation", got)
				}
				probe.release <- "ok"
				if got := waitResult(t, first); got.Err != "" || got.Left != 90 {
					t.Fatalf("first result = %+v, want left 90", got)
				}
				assertNoProbe(t, server)
			})
			t.Run("failed probe is retried", func(t *testing.T) {
				home, binDir, server := probeEnvironment(t)
				account := accountDirectory(t)
				first := startProbeCaller(t, server, agent, account, home, binDir)
				waitCall(t, server, agent, account)
				probe := waitProbe(t, server, agent, account)
				probe.release <- "fail"
				if got := waitResult(t, first); got.Err == "" {
					t.Fatalf("failed probe returned success: %+v", got)
				}
				second := startProbeCaller(t, server, agent, account, home, binDir)
				waitCall(t, server, agent, account)
				retry := waitProbe(t, server, agent, account)
				retry.release <- "ok"
				if got := waitResult(t, second); got.Err != "" || got.Left != 90 {
					t.Fatalf("retry result = %+v, want left 90", got)
				}
				assertNoProbe(t, server)
			})
		})
	}
}
