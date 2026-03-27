package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type rateLimitsResponse struct {
	RateLimitsByLimitId map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
	RateLimits          rateLimitSnapshot            `json:"rateLimits"`
}

type rateLimitSnapshot struct {
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
	Credits   *creditsSnapshot `json:"credits"`
	PlanType  *string          `json:"planType"`
	LimitId   *string          `json:"limitId"`
	LimitName *string          `json:"limitName"`
}

type creditsSnapshot struct {
	Balance   *string `json:"balance"`
	HasCredit bool    `json:"hasCredits"`
	Unlimited bool    `json:"unlimited"`
}

type rateLimitWindow struct {
	UsedPercent        int    `json:"usedPercent"`
	WindowDurationMins *int   `json:"windowDurationMins"`
	ResetsAt           *int64 `json:"resetsAt"`
}

type rpcReq struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   any             `json:"error"`
}

func GetQuota(timeout time.Duration) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "codex", "app-server")
	if home, err := os.UserHomeDir(); err == nil {
		// Use ~/.config/quota/ as CWD to avoid TCC-protected folder access.
		safeDir := filepath.Join(home, ".config", "quota")
		_ = os.MkdirAll(safeDir, 0o755)
		cmd.Dir = safeDir
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	w := bufio.NewWriter(stdin)
	r := bufio.NewScanner(stdout)
	r.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	send := func(req rpcReq) error {
		b, _ := json.Marshal(req)
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		return w.Flush()
	}

	type scanResult struct {
		line string
		ok   bool
	}
	lines := make(chan scanResult, 1)
	go func() {
		defer close(lines)
		for r.Scan() {
			select {
			case lines <- scanResult{r.Text(), true}:
			case <-ctx.Done():
				return
			}
		}
	}()

	readUntil := func(id int) (json.RawMessage, error) {
		for {
			select {
			case <-ctx.Done():
				return nil, errors.New("timeout waiting for rpc response")
			case sr, ok := <-lines:
				if !ok {
					return nil, errors.New("process exited before response")
				}
				line := strings.TrimSpace(sr.line)
				if line == "" {
					continue
				}
				var resp rpcResp
				if err := json.Unmarshal([]byte(line), &resp); err != nil {
					continue
				}
				if resp.ID != id {
					continue
				}
				if resp.Error != nil {
					return nil, fmt.Errorf("rpc error: %v", resp.Error)
				}
				return resp.Result, nil
			}
		}
	}

	// initialize
	if err := send(rpcReq{JSONRPC: "2.0", ID: 1, Method: "initialize", Params: map[string]any{
		"clientInfo":   map[string]any{"name": "quota-cli", "version": "0.1.0"},
		"capabilities": nil,
	}}); err != nil {
		return nil, err
	}
	if _, err := readUntil(1); err != nil {
		return nil, err
	}

	// rate limits
	if err := send(rpcReq{JSONRPC: "2.0", ID: 2, Method: "account/rateLimits/read", Params: nil}); err != nil {
		return nil, err
	}
	resRaw, err := readUntil(2)
	if err != nil {
		return nil, err
	}

	var rr rateLimitsResponse
	if err := json.Unmarshal(resRaw, &rr); err != nil {
		return nil, err
	}

	return buildOutput(rr)
}

func winToEntry(w *rateLimitWindow) map[string]any {
	if w == nil {
		return nil
	}
	left := 100 - w.UsedPercent
	var resetsIn any = nil
	if w.ResetsAt != nil {
		delta := time.Unix(*w.ResetsAt, 0).Sub(time.Now())
		if delta < 0 {
			delta = 0
		}
		mins := int(delta.Minutes())
		d := mins / (60 * 24)
		h := (mins / 60) % 24
		m := mins % 60
		if d > 0 {
			resetsIn = fmt.Sprintf("%dd %dh", d, h)
		} else if h > 0 {
			resetsIn = fmt.Sprintf("%dh %dm", h, m)
		} else {
			resetsIn = fmt.Sprintf("%dm", m)
		}
	}
	return map[string]any{"left": left, "resetsIn": resetsIn}
}

func buildOutput(rr rateLimitsResponse) (map[string]any, error) {
	snap := rr.RateLimits
	if rr.RateLimitsByLimitId != nil {
		if s, ok := rr.RateLimitsByLimitId["codex"]; ok {
			snap = s
		}
	}

	out := map[string]any{}
	if e := winToEntry(snap.Primary); e != nil {
		out["fiveHour"] = e
	}
	if e := winToEntry(snap.Secondary); e != nil {
		out["day"] = e
	}
	if snap.Credits != nil {
		bal := ""
		if snap.Credits.Balance != nil {
			bal = *snap.Credits.Balance
		}
		out["credits"] = map[string]any{"balance": bal, "hasCredits": snap.Credits.HasCredit, "unlimited": snap.Credits.Unlimited}
	}
	if snap.PlanType != nil {
		out["planType"] = *snap.PlanType
	}
	if len(out) == 0 {
		return nil, errors.New("empty codex rate limits")
	}
	return out, nil
}
