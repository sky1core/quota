package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
)

const (
	selectAgentAll    = "all"
	selectAgentClaude = "claude"
	selectAgentCodex  = "codex"

	selectAgentStatusSelected = "selected"
	selectAgentStatusUsable   = "usable"
	selectAgentStatusSkipped  = "skipped"
	selectAgentStatusError    = "error"
)

type selectAgentOptions struct {
	agent   string
	jsonOut bool
	model   string
}

type selectAgentResult struct {
	Selected   *selectAgentCandidate  `json:"selected,omitempty"`
	Candidates []selectAgentCandidate `json:"candidates"`
	Error      string                 `json:"error,omitempty"`
	Generated  time.Time              `json:"generated"`
}

type selectAgentCandidate struct {
	Provider   string              `json:"provider"`
	Key        string              `json:"key"`
	Label      string              `json:"label"`
	Status     string              `json:"status"`
	Reason     string              `json:"reason,omitempty"`
	Error      string              `json:"error,omitempty"`
	MinLeftPct float64             `json:"minLeftPct"`
	Command    []string            `json:"command,omitempty"`
	SetEnv     map[string]string   `json:"setEnv,omitempty"`
	UnsetEnv   []string            `json:"unsetEnv,omitempty"`
	Windows    []selectAgentWindow `json:"windows,omitempty"`
}

type selectAgentWindow struct {
	Key               string  `json:"key"`
	Label             string  `json:"label"`
	LeftPct           float64 `json:"leftPct"`
	SurplusPct        float64 `json:"surplusPct"`
	ResetsIn          string  `json:"resetsIn,omitempty"`
	ResetsAt          string  `json:"resetsAt,omitempty"`
	ResetMins         float64 `json:"resetMins,omitempty"`
	SurplusPctPerHour float64 `json:"surplusPctPerHour,omitempty"`
}

func runSelectAgent(args []string) int {
	return runSelectAgentWithIO(args, os.Stdout, os.Stderr)
}

func runSelectAgentWithIO(args []string, stdout, stderr io.Writer) int {
	opts, err := parseSelectAgentArgs(args, io.Discard)
	if err == flag.ErrHelp {
		printSelectAgentUsage(stderr)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		printSelectAgentUsage(stderr)
		return 2
	}
	now := time.Now()
	cfg, err := config.Load()
	if err != nil {
		if opts.jsonOut {
			result := selectAgentResult{Candidates: []selectAgentCandidate{}, Error: "config load error: " + err.Error(), Generated: now}
			if encErr := writeSelectAgentJSON(stdout, result); encErr != nil {
				fmt.Fprintf(stderr, "json encode: %v\n", encErr)
			}
			return 1
		}
		fmt.Fprintln(stderr, "config load error:", err)
		return 1
	}
	result, err := buildSelectAgentResult(cfg, opts, now)
	if opts.jsonOut {
		if err != nil {
			result.Error = err.Error()
		}
		if encErr := writeSelectAgentJSON(stdout, result); encErr != nil {
			fmt.Fprintf(stderr, "json encode: %v\n", encErr)
			return 1
		}
		if err != nil {
			return 1
		}
		return 0
	}
	fmt.Fprint(stdout, formatSelectAgentResult(result))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func writeSelectAgentJSON(output io.Writer, result selectAgentResult) error {
	enc := json.NewEncoder(output)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func parseSelectAgentArgs(args []string, output io.Writer) (selectAgentOptions, error) {
	fs := flag.NewFlagSet("quota-cli select-agent", flag.ContinueOnError)
	fs.SetOutput(output)
	agent := fs.String("agent", selectAgentAll, "agent group: all, claude, or codex")
	jsonOut := fs.Bool("json", false, "Output JSON")
	model := fs.String("model", "", "Claude model selector, only with --agent=claude")
	if err := fs.Parse(args); err != nil {
		return selectAgentOptions{}, err
	}
	if fs.NArg() > 0 {
		return selectAgentOptions{}, fmt.Errorf("unexpected argument: %q", fs.Arg(0))
	}
	opts := selectAgentOptions{
		agent:   strings.ToLower(strings.TrimSpace(*agent)),
		jsonOut: *jsonOut,
		model:   strings.ToLower(strings.TrimSpace(*model)),
	}
	switch opts.agent {
	case selectAgentAll, selectAgentClaude, selectAgentCodex:
	default:
		return selectAgentOptions{}, fmt.Errorf("--agent must be all, claude, or codex")
	}
	if opts.model != "" && opts.agent != selectAgentClaude {
		return selectAgentOptions{}, fmt.Errorf("--model is only valid with --agent=claude")
	}
	return opts, nil
}

func printSelectAgentUsage(output io.Writer) {
	fmt.Fprint(output, `usage:
  quota-cli select-agent [--agent=all|claude|codex] [--json]
  quota-cli select-agent --agent=claude --model=fable
`)
}

func buildSelectAgentResult(cfg config.Config, opts selectAgentOptions, now time.Time) (selectAgentResult, error) {
	result := selectAgentResult{Candidates: []selectAgentCandidate{}, Generated: now}
	var scores []accountScore
	var usable []bool
	if opts.agent == selectAgentAll || opts.agent == selectAgentClaude {
		candidates, providerScores, providerUsable, err := collectClaudeSelectAgentCandidates(cfg, opts.model, now)
		if err != nil {
			return result, err
		}
		result.Candidates = append(result.Candidates, candidates...)
		scores = append(scores, providerScores...)
		usable = append(usable, providerUsable...)
	}
	if opts.agent == selectAgentAll || opts.agent == selectAgentCodex {
		candidates, providerScores, providerUsable, err := collectCodexSelectAgentCandidates(cfg, now)
		if err != nil {
			return result, err
		}
		result.Candidates = append(result.Candidates, candidates...)
		scores = append(scores, providerScores...)
		usable = append(usable, providerUsable...)
	}
	best := selectBestScore(scores, usable)
	if best >= 0 {
		result.Candidates[best].Status = selectAgentStatusSelected
		selected := result.Candidates[best]
		result.Selected = &selected
		return result, nil
	}
	err := fmt.Errorf("no account has usable quota%s", selectAgentFailureSuffix(result.Candidates))
	return result, err
}

func collectClaudeSelectAgentCandidates(cfg config.Config, requestedModel string, now time.Time) ([]selectAgentCandidate, []accountScore, []bool, error) {
	accounts, skipped := cfg.ResolveAccounts()
	if len(skipped) > 0 {
		return nil, nil, nil, fmt.Errorf("invalid Claude account config: %s", strings.Join(skipped, "; "))
	}
	minLeftPcts, err := execPromptMinLeftPctByAccount(cfg, claudeAccountKeys(accounts), selectAgentClaude)
	if err != nil {
		return nil, nil, nil, err
	}
	results := make([]quotaProbeResult, len(accounts))
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func(i int, account config.ResolvedAccount) {
			defer wg.Done()
			results[i].quota, results[i].err = claude.GetQuotaForConfigDir(delegateProbeTimeout, account.ConfigDir, cliCacheMaxAge)
		}(i, account)
	}
	wg.Wait()

	compareModel := shouldCompareClaudeModelWindow(results, requestedModel, minLeftPcts, now)
	candidates := make([]selectAgentCandidate, len(accounts))
	scores := make([]accountScore, len(accounts))
	usable := make([]bool, len(accounts))
	for i, account := range accounts {
		candidate := selectAgentCandidate{
			Provider:   selectAgentClaude,
			Key:        account.Key,
			Label:      account.Label,
			Status:     selectAgentStatusUsable,
			MinLeftPct: minLeftPcts[i],
			Command:    []string{"claude", "-p"},
			SetEnv:     selectAgentClaudeSetEnv(account.ConfigDir),
			UnsetEnv:   selectAgentClaudeUnsetEnv(account.ConfigDir),
		}
		if results[i].err != nil {
			candidate.Status = selectAgentStatusError
			candidate.Error = selectAgentErrorSummary(results[i].err)
			candidates[i] = candidate
			continue
		}
		candidate.Windows = claudeSelectAgentWindows(results[i].quota, requestedModel, minLeftPcts[i], now)
		score, ok := scoreClaudeQuota(results[i].quota, requestedModel, compareModel, minLeftPcts[i], now)
		if !ok {
			candidate.Status = selectAgentStatusSkipped
			candidate.Reason = fiveHourQuotaRejection(results[i].quota, selectAgentClaude)
			if candidate.Reason == "" {
				candidate.Reason = selectAgentSkipReason(candidate.Windows, minLeftPcts[i])
			}
			candidates[i] = candidate
			continue
		}
		scores[i] = score
		usable[i] = true
		candidates[i] = candidate
	}
	return candidates, scores, usable, nil
}

func collectCodexSelectAgentCandidates(cfg config.Config, now time.Time) ([]selectAgentCandidate, []accountScore, []bool, error) {
	accounts, skipped := cfg.ResolveCodexAccounts()
	if len(skipped) > 0 {
		return nil, nil, nil, fmt.Errorf("invalid Codex account config: %s", strings.Join(skipped, "; "))
	}
	minLeftPcts, err := execPromptMinLeftPctByAccount(cfg, codexAccountKeys(accounts), selectAgentCodex)
	if err != nil {
		return nil, nil, nil, err
	}
	results := make([]quotaProbeResult, len(accounts))
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func(i int, account config.ResolvedCodexAccount) {
			defer wg.Done()
			results[i].quota, results[i].err = codex.GetQuotaForHome(delegateProbeTimeout, account.Home, cliCacheMaxAge)
		}(i, account)
	}
	wg.Wait()

	compareShortest := shouldCompareCodexShortestWindow(results, minLeftPcts, now)
	candidates := make([]selectAgentCandidate, len(accounts))
	scores := make([]accountScore, len(accounts))
	usable := make([]bool, len(accounts))
	for i, account := range accounts {
		candidate := selectAgentCandidate{
			Provider:   selectAgentCodex,
			Key:        account.Key,
			Label:      account.Label,
			Status:     selectAgentStatusUsable,
			MinLeftPct: minLeftPcts[i],
			Command:    []string{"codex", "exec"},
			SetEnv:     selectAgentCodexSetEnv(account.Home),
			UnsetEnv:   selectAgentCodexUnsetEnv(account.Home),
		}
		if results[i].err != nil {
			candidate.Status = selectAgentStatusError
			candidate.Error = selectAgentErrorSummary(results[i].err)
			candidates[i] = candidate
			continue
		}
		candidate.Windows = selectAgentWindows(quotaWindows(results[i].quota), minLeftPcts[i], now)
		score, ok := scoreCodexQuota(results[i].quota, compareShortest, minLeftPcts[i], now)
		if !ok {
			candidate.Status = selectAgentStatusSkipped
			candidate.Reason = fiveHourQuotaRejection(results[i].quota, selectAgentCodex)
			if candidate.Reason == "" {
				candidate.Reason = selectAgentSkipReason(candidate.Windows, minLeftPcts[i])
			}
			candidates[i] = candidate
			continue
		}
		scores[i] = score
		usable[i] = true
		candidates[i] = candidate
	}
	return candidates, scores, usable, nil
}

func claudeSelectAgentWindows(quota map[string]any, requestedModel string, minLeftPct float64, now time.Time) []selectAgentWindow {
	windows := quotaWindows(quota)
	priority := make([]map[string]any, 0, 3)
	if requestedModel != "" {
		priority = append(priority, findRequestedModelWindow(windows, requestedModel))
	}
	priority = append(priority, findWindowByKey(windows, "weekly_all"), findWindowByKey(windows, "session"))
	return selectAgentWindows(priority, minLeftPct, now)
}

func selectAgentWindows(windows []map[string]any, minLeftPct float64, now time.Time) []selectAgentWindow {
	out := make([]selectAgentWindow, 0, len(windows))
	for _, window := range windows {
		rw, ok := selectAgentWindowFromQuota(window, minLeftPct, now)
		if ok {
			out = append(out, rw)
		}
	}
	return out
}

func selectAgentWindowFromQuota(window map[string]any, minLeftPct float64, now time.Time) (selectAgentWindow, bool) {
	if window == nil {
		return selectAgentWindow{}, false
	}
	left, ok := numericValue(window["left"])
	if !ok {
		return selectAgentWindow{}, false
	}
	sw := scoreQuotaWindow(window, minLeftPct, now)
	key, _ := window["key"].(string)
	label, _ := window["label"].(string)
	if label == "" {
		label = key
	}
	rw := selectAgentWindow{
		Key:        key,
		Label:      label,
		LeftPct:    left,
		SurplusPct: sw.available,
	}
	if resetsIn, ok := window["resetsIn"].(string); ok {
		rw.ResetsIn = resetsIn
	}
	if at, ok := window["resetsAt"].(time.Time); ok && !at.IsZero() {
		rw.ResetsAt = at.Format(time.RFC3339)
	}
	if sw.resetKnown {
		rw.ResetMins = sw.resetMins
		rw.SurplusPctPerHour = sw.available / (sw.resetMins / 60)
		if rw.ResetsIn == "" {
			rw.ResetsIn = selectAgentDuration(sw.resetMins)
		}
	}
	return rw, true
}

func selectAgentSkipReason(windows []selectAgentWindow, minLeftPct float64) string {
	for _, window := range windows {
		if window.LeftPct < minLeftPct {
			return fmt.Sprintf("%s left %s%% is below minLeftPct %s%%", window.Label, selectAgentNumber(window.LeftPct), selectAgentNumber(minLeftPct))
		}
	}
	return "missing applicable quota window"
}

func selectAgentFailureSuffix(candidates []selectAgentCandidate) string {
	var failures []string
	for _, candidate := range candidates {
		if candidate.Status == selectAgentStatusError {
			failures = append(failures, candidate.Key+": "+candidate.Error)
		}
	}
	if len(failures) == 0 {
		return ""
	}
	return " (probe failures: " + strings.Join(failures, "; ") + ")"
}

func selectAgentErrorSummary(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if head, _, ok := strings.Cut(msg, "\n--- output ---"); ok {
		msg = strings.TrimSpace(head)
	}
	msg = strings.Join(strings.Fields(msg), " ")
	const maxLen = 300
	if len(msg) > maxLen {
		msg = strings.TrimSpace(msg[:maxLen]) + "..."
	}
	return msg
}

func selectAgentClaudeSetEnv(configDir string) map[string]string {
	if configDir == "" {
		return nil
	}
	return map[string]string{"CLAUDE_CONFIG_DIR": configDir}
}

func selectAgentCodexSetEnv(home string) map[string]string {
	if home == "" {
		return nil
	}
	return map[string]string{"CODEX_HOME": home}
}

func selectAgentClaudeUnsetEnv(configDir string) []string {
	base := os.Environ()
	return selectAgentRemovedEnvKeys(base, claude.EnvForConfigDir(base, configDir))
}

func selectAgentCodexUnsetEnv(home string) []string {
	base := os.Environ()
	return selectAgentRemovedEnvKeys(base, codex.EnvForHome(base, home))
}

func selectAgentRemovedEnvKeys(base, selected []string) []string {
	kept := map[string]bool{}
	for _, kv := range selected {
		if key, _, ok := strings.Cut(kv, "="); ok {
			kept[key] = true
		}
	}
	var removed []string
	seen := map[string]bool{}
	for _, kv := range base {
		key, _, ok := strings.Cut(kv, "=")
		if !ok || kept[key] || seen[key] {
			continue
		}
		seen[key] = true
		removed = append(removed, key)
	}
	sort.Strings(removed)
	return removed
}

func formatSelectAgentResult(result selectAgentResult) string {
	var b strings.Builder
	if result.Selected != nil {
		b.WriteString("Selected: " + result.Selected.Key + " (" + result.Selected.Provider + ")\n")
		b.WriteString("Run: " + strings.Join(result.Selected.Command, " ") + "\n")
		b.WriteString("Set env: " + selectAgentSetEnvText(result.Selected.SetEnv) + "\n")
		b.WriteString("Unset env: " + selectAgentUnsetEnvText(result.Selected.UnsetEnv) + "\n\n")
	} else {
		b.WriteString("Selected: none\n\n")
	}
	b.WriteString("Candidates\n")
	for _, candidate := range result.Candidates {
		line := fmt.Sprintf("  %-9s %-7s %-8s %s", candidate.Key, candidate.Provider, candidate.Status, selectAgentWindowSummary(candidate.Windows))
		if candidate.Reason != "" {
			line += " - " + candidate.Reason
		}
		if candidate.Error != "" {
			line += " - " + candidate.Error
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\nGenerated: " + result.Generated.Format(time.RFC3339) + "\n")
	return b.String()
}

func selectAgentSetEnvText(env map[string]string) string {
	if len(env) == 0 {
		return "none (default/inherited account)"
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+env[key])
	}
	return strings.Join(parts, " ")
}

func selectAgentUnsetEnvText(keys []string) string {
	if len(keys) == 0 {
		return "none"
	}
	return strings.Join(keys, " ")
}

func selectAgentWindowSummary(windows []selectAgentWindow) string {
	if len(windows) == 0 {
		return "(no quota windows)"
	}
	parts := make([]string, 0, len(windows))
	for _, window := range windows {
		part := fmt.Sprintf("%s %s%% left", window.Label, selectAgentNumber(window.LeftPct))
		if window.ResetsIn != "" {
			part += "/" + window.ResetsIn
		}
		if window.ResetMins > 0 {
			part += " (" + selectAgentNumber(window.SurplusPct) + "% surplus"
			part += ", " + selectAgentNumber(window.SurplusPctPerHour) + "%/h)"
		} else {
			part += " (" + selectAgentNumber(window.SurplusPct) + "% surplus)"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

func selectAgentDuration(mins float64) string {
	total := int(math.Ceil(mins))
	if total < 0 {
		total = 0
	}
	days := total / (24 * 60)
	total %= 24 * 60
	hours := total / 60
	minutes := total % 60
	var parts []string
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	return strings.Join(parts, " ")
}

func selectAgentNumber(value float64) string {
	if math.Abs(value-math.Round(value)) < 0.05 {
		return fmt.Sprintf("%.0f", value)
	}
	return fmt.Sprintf("%.1f", value)
}
