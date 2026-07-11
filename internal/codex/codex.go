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
	"sort"
	"strings"
	"time"
)

type rateLimitsResponse struct {
	RateLimitsByLimitId map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
	RateLimits          rateLimitSnapshot            `json:"rateLimits"`
	// ResetCredits are one-shot "rate limit reset" grants (초기화권) that sit at
	// the top level of the response, alongside (not inside) the per-limit
	// snapshots. Each grant has its own expiry.
	ResetCredits *resetCreditsSnapshot `json:"rateLimitResetCredits"`
}

type resetCreditsSnapshot struct {
	AvailableCount int                `json:"availableCount"`
	Credits        []resetCreditEntry `json:"credits"`
}

type resetCreditEntry struct {
	Title     string `json:"title"`
	Status    string `json:"status"`
	ExpiresAt *int64 `json:"expiresAt"`
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
	// resetsIn is only added when known; an unknown reset omits the key entirely
	// (like resetsAt) rather than emitting a JSON null, which would violate the
	// string contract. Consumers already treat a missing key as "unknown".
	entry := map[string]any{"left": left}
	if w.ResetsAt != nil {
		at := time.Unix(*w.ResetsAt, 0)
		delta := at.Sub(time.Now())
		if delta <= 0 {
			// Window already reset; a stale past instant is meaningless, so
			// report 0m and omit resetsAt (matches the Claude path, which only
			// ever yields a future reset instant).
			entry["resetsIn"] = "0m"
			return entry
		}
		entry["resetsIn"] = fmtDurMins(int(delta.Minutes()))
		// Exact future reset instant, preserved alongside the relative string.
		entry["resetsAt"] = at
	}
	return entry
}

// fmtDurMins renders a non-negative minute count as a coarse "Nd Nh" / "Nh Nm" /
// "Nm" remaining-time string (the same shape used for rate-limit resets).
func fmtDurMins(mins int) string {
	if mins < 0 {
		mins = 0
	}
	d := mins / (60 * 24)
	h := (mins / 60) % 24
	m := mins % 60
	if d > 0 {
		return fmt.Sprintf("%dd %dh", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
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
	if rc := buildResetCredits(rr.ResetCredits); rc != nil {
		out["resetCredits"] = rc
	}
	if len(out) == 0 {
		return nil, errors.New("empty codex rate limits")
	}
	return out, nil
}

// buildResetCredits projects the top-level reset-credit grants (초기화권) into the
// output shape. Only grants that are still usable (status "available") are
// listed, sorted soonest-expiry first. Returns nil when there is nothing usable
// to show, so the key is omitted entirely (matching the other optional keys).
func buildResetCredits(rc *resetCreditsSnapshot) map[string]any {
	if rc == nil {
		return nil
	}
	now := time.Now()
	var items []map[string]any
	for _, c := range rc.Credits {
		if c.Status != "available" {
			continue
		}
		item := map[string]any{"title": c.Title}
		if c.ExpiresAt != nil {
			at := time.Unix(*c.ExpiresAt, 0)
			// Exact expiry instant, preserved alongside the relative string.
			item["expiresAt"] = at
			item["expiresIn"] = fmtDurMins(int(at.Sub(now).Minutes()))
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		ti, iok := items[i]["expiresAt"].(time.Time)
		tj, jok := items[j]["expiresAt"].(time.Time)
		if iok && jok {
			return ti.Before(tj)
		}
		// Grants with a known expiry sort ahead of those without.
		return iok && !jok
	})
	// available counts exactly the usable grants we list, so the count can never
	// contradict items (e.g. no "0 available" alongside a populated list). We do
	// not trust the response's availableCount here: it is sourced independently
	// of the per-grant status filter and could diverge.
	return map[string]any{
		"available": len(items),
		"items":     items,
	}
}
