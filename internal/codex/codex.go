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

// GetQuota fetches Codex quota for the default account (the process's CODEX_HOME,
// or ~/.codex when unset).
func GetQuota(timeout time.Duration) (map[string]any, error) {
	return GetQuotaForHome(timeout, "")
}

// GetQuotaForHome fetches Codex quota for the account identified by codexHome
// (its CODEX_HOME). An empty codexHome queries the default account, identical to
// GetQuota. codexHome must be an already-expanded absolute path; callers expand
// "~" via config.ExpandTilde before passing it here (mirrors the Claude
// GetQuotaForConfigDir contract).
func GetQuotaForHome(timeout time.Duration, codexHome string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "codex", "app-server")
	if home, err := os.UserHomeDir(); err == nil {
		// Use ~/.config/quota/ as CWD to avoid TCC-protected folder access.
		safeDir := filepath.Join(home, ".config", "quota")
		_ = os.MkdirAll(safeDir, 0o755)
		cmd.Dir = safeDir
	}
	if codexHome != "" {
		// Select a specific account by CODEX_HOME. Appended last so it wins over
		// any inherited CODEX_HOME (exec uses the last value for a duplicate key).
		cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
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

// WindowKeys returns every window key this provider can emit, in display order.
// Consumers that must pre-allocate a slot per key (quota-bar; systray cannot add
// rows at runtime) enumerate this. A key is a stable slot/selection identity —
// a coarse duration bucket — and is NEVER shown; the row's text is the window's
// own truthful label.
func WindowKeys() []string { return []string{"5h", "daily", "weekly", "monthly"} }

// windowKey maps a window's actual duration (minutes) to its stable slot key.
// Ranges (not exact values) so a duration tweak still lands in a slot. The
// returned string is always one of WindowKeys(). This never becomes a label.
func windowKey(mins int) string {
	switch {
	case mins <= 12*60: // ≤12h: short rolling window (historically 5h)
		return "5h"
	case mins <= 2*1440: // ≤2d
		return "daily"
	case mins <= 14*1440: // ≤14d
		return "weekly"
	default: // >14d
		return "monthly"
	}
}

// windowLabel renders a rate-limit window's display label from its ACTUAL
// duration (minutes) — never a coarse bucket. So a 300-minute window is "5h" and
// a 600-minute window is "10h"; the label can never claim a duration the window
// does not have. Zero components are dropped: 300→"5h", 10080→"7d", 1440→"1d",
// 43200→"30d", 90→"1h 30m". This is the only truthful source for the label,
// because the Codex response carries no per-window name — only windowDurationMins.
func windowLabel(mins int) string {
	if mins <= 0 {
		return "0m"
	}
	d := mins / 1440
	h := (mins % 1440) / 60
	m := mins % 60
	var parts []string
	if d > 0 {
		parts = append(parts, fmt.Sprintf("%dd", d))
	}
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%dh", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%dm", m))
	}
	return strings.Join(parts, " ")
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
	// Codex places windows in primary/secondary positionally and changes which
	// window sits in each slot (and which are present) over time — e.g. around a
	// GPT model launch the 5h window can disappear and the weekly window can move
	// into primary. So the contract is the shared SELF-DESCRIBING LIST: each entry
	// carries its stable slot key, its ACTUAL duration (windowMins), and a label
	// derived truthfully from that duration. Consumers iterate and display the
	// label as given; they never assume a position or hold a label vocabulary. A
	// window with no windowDurationMins can't be labeled truthfully and is omitted
	// (positional/bucketed labeling is the bug this shape removes).
	var windows []map[string]any
	seen := map[int]bool{}
	for _, w := range []*rateLimitWindow{snap.Primary, snap.Secondary} {
		if w == nil || w.WindowDurationMins == nil {
			continue
		}
		e := winToEntry(w)
		if e == nil {
			continue
		}
		mins := *w.WindowDurationMins
		// Dedupe the SAME window (identical duration) only. Two different
		// durations are two different windows and are both emitted, even if they
		// share a slot key — the data must never lose a real window to a
		// consumer's slot limit. Slot collisions are the consumer's to resolve.
		if seen[mins] {
			continue
		}
		seen[mins] = true
		e["key"] = windowKey(mins)
		e["windowMins"] = mins
		e["label"] = windowLabel(mins)
		windows = append(windows, e)
	}
	// Stable order by actual duration (shortest first), independent of response
	// position.
	sort.SliceStable(windows, func(i, j int) bool {
		return windows[i]["windowMins"].(int) < windows[j]["windowMins"].(int)
	})
	if len(windows) > 0 {
		out["windows"] = windows
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
