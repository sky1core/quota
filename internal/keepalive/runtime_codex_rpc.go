//go:build darwin || linux

package keepalive

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	codexprovider "github.com/sky1core/quota/internal/codex"
)

type codexRPC struct {
	in       io.WriteCloser
	out      io.ReadCloser
	lines    chan []byte
	done     chan struct{}
	stopOnce sync.Once
	seq      int
	readErr  error
	stopped  bool
}

func newCodexRPC(in io.WriteCloser, out io.ReadCloser) *codexRPC {
	r := &codexRPC{in: in, out: out, lines: make(chan []byte), done: make(chan struct{})}
	go func() {
		defer close(r.lines)
		s := bufio.NewScanner(out)
		s.Buffer(make([]byte, 4096), 1024*1024)
		for s.Scan() {
			select {
			case r.lines <- append([]byte(nil), s.Bytes()...):
			case <-r.done:
				return
			}
		}
		r.readErr = s.Err()
	}()
	return r
}

func (r *codexRPC) close() {
	r.stopOnce.Do(func() { close(r.done); r.in.Close(); r.out.Close() })
}

func (r *codexRPC) write(ctx context.Context, v any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return errors.New("codex RPC encoding failed")
	}
	done := make(chan error, 1)
	go func() {
		n, err := r.in.Write(append(data, '\n'))
		if err == nil && n != len(data)+1 {
			err = io.ErrShortWrite
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			r.stopped = true
			r.close()
			return errors.New("codex RPC write failed")
		}
		return nil
	case <-ctx.Done():
		r.stopped = true
		r.close()
		<-done
		return ctx.Err()
	}
}

func (r *codexRPC) call(ctx context.Context, method string, params, result any) error {
	switch method {
	case "initialize", "thread/queue/list", "thread/queue/add", "thread/queue/delete":
	default:
		return errors.New("codex keepalive RPC method is forbidden")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if r.stopped {
		return errors.New("codex RPC transport is closed")
	}
	r.seq++
	if err := r.write(ctx, map[string]any{"id": r.seq, "method": method, "params": params}); err != nil {
		return err
	}
	for count := 0; count < 256; count++ {
		select {
		case <-ctx.Done():
			r.stopped = true
			r.close()
			return ctx.Err()
		case line, ok := <-r.lines:
			if !ok {
				r.stopped = true
				if r.readErr != nil {
					return errors.New("codex RPC output is oversized or unreadable")
				}
				return errors.New("codex RPC helper exited before replying")
			}
			var reply struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &reply) != nil {
				return errors.New("codex RPC reply shape is unsupported")
			}
			if reply.ID == nil && reply.Method != "" {
				continue
			}
			if reply.ID == nil || *reply.ID != r.seq || reply.Method != "" {
				return errors.New("codex RPC response identity mismatch")
			}
			if reply.Error != nil {
				return fmt.Errorf("codex RPC %s failed (code %d)", method, reply.Error.Code)
			}
			if len(reply.Result) == 0 || string(reply.Result) == "null" || json.Unmarshal(reply.Result, result) != nil {
				return errors.New("codex RPC result shape is unsupported")
			}
			return nil
		}
	}
	return errors.New("codex RPC notification limit exceeded")
}

func (r *codexRPC) initialize(ctx context.Context) error {
	var result map[string]json.RawMessage
	if err := r.call(ctx, "initialize", map[string]any{"clientInfo": map[string]string{"name": "quota_keepalive", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, &result); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return r.write(ctx, map[string]any{"method": "initialized", "params": map[string]any{}})
}

func startCodexRPC(ctx context.Context, account Account, executable string) (*codexRPC, func() error, error) {
	lifetime, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	cmd := exec.CommandContext(lifetime, executable, "app-server", "--stdio")
	cmd.Env = codexprovider.EnvForHome(os.Environ(), account.Home)
	cmd.Dir = account.Home
	cmd.WaitDelay = time.Second
	var stderr runtimeOutput
	cmd.Stderr = &stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, nil, errors.New("codex helper stdin unavailable")
	}
	out, writer, err := os.Pipe()
	if err != nil {
		in.Close()
		cancel()
		return nil, nil, errors.New("codex helper stdout unavailable")
	}
	cmd.Stdout = writer
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		writer.Close()
		cancel()
		return nil, nil, errors.New("codex helper could not start")
	}
	writer.Close()
	r := newCodexRPC(in, out)
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	closeHelper := func() error {
		defer cancel()
		in.Close()
		defer r.close()
		select {
		case err := <-exited:
			if err != nil {
				return errors.New("codex helper exited unsuccessfully; private output withheld")
			}
			return nil
		case <-time.After(2 * time.Second):
			cancel()
			<-exited
			return errors.New("codex helper did not exit after stdin closed")
		}
	}
	if err := r.initialize(ctx); err != nil {
		return nil, nil, errors.Join(err, closeHelper())
	}
	return r, closeHelper, nil
}

type codexQueuedSubmission struct {
	ID       string                        `json:"id"`
	ClientID string                        `json:"clientUserMessageId"`
	Input    []struct{ Type, Text string } `json:"input"`
}

func (r *codexRPC) queue(ctx context.Context, session string) ([]codexQueuedSubmission, error) {
	var all []codexQueuedSubmission
	cursor := ""
	seen := map[string]bool{}
	entryIDs := map[string]bool{}
	for page := 0; page < 16; page++ {
		params := map[string]any{"threadId": session, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Data *[]codexQueuedSubmission `json:"data"`
			Next *string                  `json:"nextCursor"`
		}
		if err := r.call(ctx, "thread/queue/list", params, &result); err != nil {
			return nil, err
		}
		if result.Data == nil {
			return nil, errors.New("codex queue data is missing or unsupported")
		}
		for _, entry := range *result.Data {
			if entry.ID == "" || entry.ClientID == "" || len(entry.Input) == 0 || entryIDs[entry.ID] {
				return nil, errors.New("codex queued input shape is unsupported")
			}
			entryIDs[entry.ID] = true
		}
		all = append(all, (*result.Data)...)
		if len(all) > 1600 {
			return nil, errors.New("codex queue exceeds inspection limit")
		}
		if result.Next == nil {
			return all, nil
		}
		cursor = *result.Next
		if cursor == "" || seen[cursor] {
			return nil, errors.New("codex queue pagination is unsupported")
		}
		seen[cursor] = true
	}
	return nil, errors.New("codex queue pagination exceeds inspection limit")
}

func (r *codexRPC) add(ctx context.Context, session, id, message string, ready func() bool) error {
	if ready == nil || !ready() {
		return errors.New("keepalive PC inactivity condition no longer holds")
	}
	var result struct {
		Entry codexQueuedSubmission `json:"queuedSubmission"`
	}
	if err := r.call(ctx, "thread/queue/add", map[string]any{"threadId": session, "clientUserMessageId": id, "input": []map[string]string{{"type": "text", "text": message}}}, &result); err != nil {
		return err
	}
	if !result.Entry.owns(id, message) {
		return errors.New("codex queue acceptance is not correlated to the submitted input")
	}
	return nil
}

func (e codexQueuedSubmission) owns(id, message string) bool {
	return e.ID != "" && e.ClientID == id && len(e.Input) == 1 && e.Input[0].Type == "text" && e.Input[0].Text == message
}

func (r *codexRPC) removeOwn(ctx context.Context, session, id, message string) error {
	entries, err := r.queue(ctx, session)
	if err != nil {
		return err
	}
	var own *codexQueuedSubmission
	for i := range entries {
		if entries[i].ClientID != id {
			continue
		}
		if own != nil || !entries[i].owns(id, message) {
			return errors.New("codex cleanup ownership is ambiguous; queue unchanged")
		}
		own = &entries[i]
	}
	if own == nil {
		return errors.New("codex input is no longer queued; consumption or completion remains unconfirmed")
	}
	var result struct {
		Deleted *bool `json:"deleted"`
	}
	if err := r.call(ctx, "thread/queue/delete", map[string]string{"threadId": session, "queuedSubmissionId": own.ID}, &result); err != nil {
		return err
	}
	if result.Deleted == nil || !*result.Deleted {
		return errors.New("codex queued input was not removed; consumption remains unconfirmed")
	}
	return nil
}
