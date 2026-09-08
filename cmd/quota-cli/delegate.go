package main

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
)

const (
	delegateProbeTimeout        = 40 * time.Second
	defaultExecPromptMinLeftPct = 5.0
	minFiveHourAdmissionPct     = 25.0
	minExecPromptMinLeftPct     = 0.0
	maxExecPromptMinLeftPct     = 100.0
)

type scoredWindow struct {
	present         bool
	available       float64
	resetKnown      bool
	resetMins       float64
	availablePerMin float64
	compareRate     bool
}

type accountScore struct {
	windows []scoredWindow
}

type quotaProbeResult struct {
	quota map[string]any
	err   error
}

func runClaudePrompt(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load error:", err)
		return 1
	}
	account, err := selectClaudeAccount(cfg, args, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude account selection error:", err)
		return 1
	}
	bin, err := findClaudePromptBinary()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	claude.InvalidateCacheForConfigDir(account.ConfigDir)
	if err := execDelegated(bin, []string{"-p"}, args, claude.EnvForConfigDir(os.Environ(), account.ConfigDir)); err != nil {
		fmt.Fprintln(os.Stderr, "claude exec error:", err)
		return 1
	}
	return 0
}

func runCodexPrompt(args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load error:", err)
		return 1
	}
	account, err := selectCodexAccount(cfg, time.Now())
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex account selection error:", err)
		return 1
	}
	bin, err := exec.LookPath("codex")
	if err != nil {
		fmt.Fprintln(os.Stderr, "codex CLI not found")
		return 1
	}
	codex.InvalidateCacheForHome(account.Home)
	if err := execDelegated(bin, []string{"exec"}, args, codex.EnvForHome(os.Environ(), account.Home)); err != nil {
		fmt.Fprintln(os.Stderr, "codex exec error:", err)
		return 1
	}
	return 0
}

func selectClaudeAccount(cfg config.Config, args []string, now time.Time) (config.ResolvedAccount, error) {
	accounts, skipped := cfg.ResolveAccounts()
	if len(skipped) > 0 {
		return config.ResolvedAccount{}, fmt.Errorf("invalid Claude account config: %s", strings.Join(skipped, "; "))
	}
	minLeftPcts, err := execPromptMinLeftPctByAccount(cfg, claudeAccountKeys(accounts), "claude")
	if err != nil {
		return config.ResolvedAccount{}, err
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

	requestedModel := claudeRequestedModel(args)
	compareModel := shouldCompareClaudeModelWindow(results, requestedModel, minLeftPcts, now)
	var failures []string
	scores := make([]accountScore, len(results))
	usable := make([]bool, len(results))
	for i, result := range results {
		if result.err != nil {
			failures = append(failures, accounts[i].Key+": "+result.err.Error())
			continue
		}
		score, ok := scoreClaudeQuota(result.quota, requestedModel, compareModel, minLeftPcts[i], now)
		if !ok {
			continue
		}
		scores[i] = score
		usable[i] = true
	}
	best := selectBestScore(scores, usable)
	if best >= 0 {
		return accounts[best], nil
	}
	if requestedModel != "" {
		return config.ResolvedAccount{}, fmt.Errorf("no account has enough applicable Claude quota for --model %s%s", requestedModel, failureSuffix(failures))
	}
	return config.ResolvedAccount{}, fmt.Errorf("no account has usable quota%s", failureSuffix(failures))
}

func selectCodexAccount(cfg config.Config, now time.Time) (config.ResolvedCodexAccount, error) {
	accounts, skipped := cfg.ResolveCodexAccounts()
	if len(skipped) > 0 {
		return config.ResolvedCodexAccount{}, fmt.Errorf("invalid Codex account config: %s", strings.Join(skipped, "; "))
	}
	minLeftPcts, err := execPromptMinLeftPctByAccount(cfg, codexAccountKeys(accounts), "codex")
	if err != nil {
		return config.ResolvedCodexAccount{}, err
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
	var failures []string
	scores := make([]accountScore, len(results))
	usable := make([]bool, len(results))
	for i, result := range results {
		if result.err != nil {
			failures = append(failures, accounts[i].Key+": "+result.err.Error())
			continue
		}
		score, ok := scoreCodexQuota(result.quota, compareShortest, minLeftPcts[i], now)
		if !ok {
			continue
		}
		scores[i] = score
		usable[i] = true
	}
	best := selectBestScore(scores, usable)
	if best >= 0 {
		return accounts[best], nil
	}
	return config.ResolvedCodexAccount{}, fmt.Errorf("no account has usable quota%s", failureSuffix(failures))
}

func scoreClaudeQuota(quota map[string]any, requestedModel string, compareModel bool, minLeftPct float64, now time.Time) (accountScore, bool) {
	if quotaAdmissionRejection(quota, "claude") != "" {
		return accountScore{}, false
	}
	windows := quotaWindows(quota)
	session := findWindowByKey(windows, "session")
	weekly := findWindowByKey(windows, "weekly_all")
	var modelScore scoredWindow
	if requestedModel != "" {
		model := findRequestedModelWindow(windows, requestedModel)
		modelScore = scoreQuotaWindow(model, minLeftPct, now)
		if belowDelegatedPromptFloor(modelScore) {
			return accountScore{}, false
		}
	}
	score, ok := buildAccountScore([]map[string]any{weekly, session}, minLeftPct, now)
	if !ok {
		return accountScore{}, false
	}
	if requestedModel != "" && compareModel && modelScore.present {
		score.windows = append([]scoredWindow{modelScore}, score.windows...)
	}
	return score, true
}

func shouldCompareClaudeModelWindow(results []quotaProbeResult, requestedModel string, minLeftPcts []float64, now time.Time) bool {
	if requestedModel == "" {
		return false
	}
	usable := 0
	withModel := 0
	for i, result := range results {
		if result.err != nil {
			continue
		}
		if _, ok := scoreClaudeQuota(result.quota, requestedModel, false, minLeftPcts[i], now); !ok {
			continue
		}
		usable++
		if scoreQuotaWindow(findRequestedModelWindow(quotaWindows(result.quota), requestedModel), minLeftPcts[i], now).present {
			withModel++
		}
	}
	return usable > 0 && usable == withModel
}

func scoreCodexQuota(quota map[string]any, compareShortest bool, minLeftPct float64, now time.Time) (accountScore, bool) {
	if quotaAdmissionRejection(quota, "codex") != "" {
		return accountScore{}, false
	}
	windows := quotaWindows(quota)
	if len(windows) == 0 {
		return accountScore{}, false
	}
	longest, shortest, longestMins, shortestMins, ok := codexWindowExtremes(windows)
	if !ok {
		return accountScore{}, false
	}
	for _, window := range windows {
		score := scoreQuotaWindow(window, minLeftPct, now)
		if belowDelegatedPromptFloor(score) {
			return accountScore{}, false
		}
	}
	priority := []map[string]any{longest}
	if compareShortest && shortestMins != longestMins {
		priority = append(priority, shortest)
	}
	return buildAccountScore(priority, minLeftPct, now)
}

func quotaAdmissionRejection(quota map[string]any, provider string) string {
	if raw, exists := quota["windowErrors"]; exists {
		windowErrors, ok := raw.([]string)
		if !ok || len(windowErrors) == 0 {
			return "invalid quota window diagnostics"
		}
		return "incomplete quota window data: " + strings.Join(windowErrors, "; ")
	}
	for _, window := range quotaWindows(quota) {
		if provider == "claude" && window["key"] != "session" && window["key"] != "weekly_all" {
			continue
		}
		left, ok := numericValue(window["left"])
		if !ok || math.IsNaN(left) || math.IsInf(left, 0) || left < 0 || left > 100 {
			return "quota window remaining is unreadable or invalid"
		}
		fiveHour := false
		switch provider {
		case "claude":
			fiveHour = window["key"] == "session"
		case "codex":
			mins, ok := numericValue(window["windowMins"])
			if !ok || math.IsNaN(mins) || mins <= 0 || mins >= math.Exp2(strconv.IntSize-1) || math.Trunc(mins) != mins {
				return "quota window duration is unreadable or invalid"
			}
			fiveHour = mins == 300
		}
		if fiveHour && left < minFiveHourAdmissionPct {
			return fmt.Sprintf("5-hour quota left %g%% is below admission minimum %g%%", left, minFiveHourAdmissionPct)
		}
	}
	return ""
}

func shouldCompareCodexShortestWindow(results []quotaProbeResult, minLeftPcts []float64, now time.Time) bool {
	usable := 0
	withShortest := 0
	for i, result := range results {
		if result.err != nil {
			continue
		}
		if _, ok := scoreCodexQuota(result.quota, false, minLeftPcts[i], now); !ok {
			continue
		}
		_, shortest, longestMins, shortestMins, ok := codexWindowExtremes(quotaWindows(result.quota))
		if !ok {
			continue
		}
		usable++
		if shortestMins != longestMins && scoreQuotaWindow(shortest, minLeftPcts[i], now).present {
			withShortest++
		}
	}
	return usable > 0 && usable == withShortest
}

func codexWindowExtremes(windows []map[string]any) (longest, shortest map[string]any, longestMins, shortestMins int, ok bool) {
	for _, window := range windows {
		mins, ok := numericValue(window["windowMins"])
		if !ok {
			continue
		}
		m := int(mins)
		if shortest == nil || m < shortestMins {
			shortest = window
			shortestMins = m
		}
		if longest == nil || m > longestMins {
			longest = window
			longestMins = m
		}
	}
	return longest, shortest, longestMins, shortestMins, longest != nil
}

func buildAccountScore(priority []map[string]any, minLeftPct float64, now time.Time) (accountScore, bool) {
	score := accountScore{windows: make([]scoredWindow, 0, len(priority))}
	anyPresent := false
	for _, window := range priority {
		sw := scoreQuotaWindow(window, minLeftPct, now)
		if belowDelegatedPromptFloor(sw) {
			return accountScore{}, false
		}
		anyPresent = anyPresent || sw.present
		score.windows = append(score.windows, sw)
	}
	return score, anyPresent
}

func selectBestScore(scores []accountScore, usable []bool) int {
	markRateComparable(scores, usable)
	best := -1
	var bestScore accountScore
	for i, score := range scores {
		if !usable[i] {
			continue
		}
		if best < 0 || compareAccountScore(score, bestScore) > 0 {
			best = i
			bestScore = score
		}
	}
	return best
}

func markRateComparable(scores []accountScore, usable []bool) {
	maxLen := 0
	for i, score := range scores {
		if !usable[i] {
			continue
		}
		if len(score.windows) > maxLen {
			maxLen = len(score.windows)
		}
	}
	for slot := 0; slot < maxLen; slot++ {
		anyPresent := false
		allKnown := true
		for i, score := range scores {
			if !usable[i] || slot >= len(score.windows) || !score.windows[slot].present {
				continue
			}
			anyPresent = true
			if !score.windows[slot].resetKnown {
				allKnown = false
				break
			}
		}
		if !anyPresent || !allKnown {
			continue
		}
		for i := range scores {
			if !usable[i] || slot >= len(scores[i].windows) || !scores[i].windows[slot].present {
				continue
			}
			scores[i].windows[slot].compareRate = true
		}
	}
}

func belowDelegatedPromptFloor(score scoredWindow) bool {
	return score.present && score.available < 0
}

func scoreQuotaWindow(window map[string]any, minLeftPct float64, now time.Time) scoredWindow {
	if window == nil {
		return scoredWindow{}
	}
	left, ok := numericValue(window["left"])
	if !ok {
		return scoredWindow{}
	}
	score := scoredWindow{present: true, available: left - minLeftPct}
	remaining, known := quotaResetRemaining(window, now)
	if known {
		mins := remaining.Minutes()
		if mins < 1 {
			mins = 1
		}
		score.resetKnown = true
		score.resetMins = mins
		score.availablePerMin = score.available / mins
	}
	return score
}

func compareAccountScore(a, b accountScore) int {
	maxLen := len(a.windows)
	if len(b.windows) > maxLen {
		maxLen = len(b.windows)
	}
	for i := 0; i < maxLen; i++ {
		var aw, bw scoredWindow
		if i < len(a.windows) {
			aw = a.windows[i]
		}
		if i < len(b.windows) {
			bw = b.windows[i]
		}
		if cmp := compareScoredWindow(aw, bw); cmp != 0 {
			return cmp
		}
	}
	return 0
}

func compareScoredWindow(a, b scoredWindow) int {
	if a.present != b.present {
		if a.present {
			return 1
		}
		return -1
	}
	if !a.present {
		return 0
	}
	if a.compareRate && b.compareRate && a.availablePerMin != b.availablePerMin {
		if a.availablePerMin > b.availablePerMin {
			return 1
		}
		return -1
	}
	if a.available != b.available {
		if a.available > b.available {
			return 1
		}
		return -1
	}
	if a.compareRate && b.compareRate && a.resetMins != b.resetMins {
		if a.resetMins < b.resetMins {
			return 1
		}
		return -1
	}
	return 0
}

func claudeAccountKeys(accounts []config.ResolvedAccount) []string {
	keys := make([]string, len(accounts))
	for i, account := range accounts {
		keys[i] = account.Key
	}
	return keys
}

func codexAccountKeys(accounts []config.ResolvedCodexAccount) []string {
	keys := make([]string, len(accounts))
	for i, account := range accounts {
		keys[i] = account.Key
	}
	return keys
}

func execPromptMinLeftPctByAccount(cfg config.Config, accountKeys []string, provider string) ([]float64, error) {
	minLeftPcts := make([]float64, len(accountKeys))
	allowed := make(map[string]int, len(accountKeys))
	for i, key := range accountKeys {
		minLeftPcts[i] = defaultExecPromptMinLeftPct
		allowed[key] = i
	}
	if cfg.ExecPrompt == nil || len(cfg.ExecPrompt.AccountSettings) == 0 {
		return minLeftPcts, nil
	}
	for key, settings := range cfg.ExecPrompt.AccountSettings {
		if !execPromptAccountSettingKeyShapeValid(key) {
			return nil, fmt.Errorf("execPrompt.accountSettings key %q must be claude, claude-<N>, codex, or codex-<N>", key)
		}
		if !execPromptAccountKeyForProvider(key, provider) {
			continue
		}
		i, ok := allowed[key]
		if !ok {
			return nil, fmt.Errorf("execPrompt.accountSettings.%s references an unconfigured %s account", key, provider)
		}
		if settings.MinLeftPct == nil {
			continue
		}
		pct := *settings.MinLeftPct
		if pct < minExecPromptMinLeftPct || pct > maxExecPromptMinLeftPct {
			return nil, fmt.Errorf("execPrompt.accountSettings.%s.minLeftPct must be between 0 and 100", key)
		}
		minLeftPcts[i] = pct
	}
	return minLeftPcts, nil
}

func execPromptAccountSettingKeyShapeValid(key string) bool {
	return key == "claude" || key == "codex" || config.ClaudeExtraKeyRe.MatchString(key) || config.CodexExtraKeyRe.MatchString(key)
}

func execPromptAccountKeyForProvider(key, provider string) bool {
	return key == provider || strings.HasPrefix(key, provider+"-")
}

func quotaWindows(quota map[string]any) []map[string]any {
	windows, _ := quota["windows"].([]map[string]any)
	return windows
}

func findWindowByKey(windows []map[string]any, key string) map[string]any {
	for _, window := range windows {
		if window["key"] == key {
			return window
		}
	}
	return nil
}

func findRequestedModelWindow(windows []map[string]any, requestedModel string) map[string]any {
	if strings.TrimSpace(requestedModel) == "" {
		return nil
	}
	for _, window := range windows {
		key, _ := window["key"].(string)
		if !strings.HasPrefix(key, "extra_") {
			continue
		}
		windowLabel, _ := window["label"].(string)
		if requestedModelMatchesLabel(requestedModel, windowLabel) {
			return window
		}
	}
	return nil
}

func requestedModelMatchesLabel(requestedModel, label string) bool {
	modelTokens := modelNameTokens(requestedModel)
	labelTokens := modelNameTokens(label)
	if len(modelTokens) == 0 || len(labelTokens) == 0 {
		return false
	}
	modelSet := make(map[string]bool, len(modelTokens))
	for _, token := range modelTokens {
		modelSet[token] = true
	}
	for _, token := range labelTokens {
		for modelToken := range modelSet {
			if modelToken == token || strings.HasPrefix(modelToken, token) || strings.HasPrefix(token, modelToken) {
				return true
			}
		}
	}
	return false
}

func modelNameTokens(s string) []string {
	raw := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	tokens := make([]string, 0, len(raw))
	for _, token := range raw {
		switch token {
		case "", "claude", "latest", "model", "only", "plan":
			continue
		}
		if len(token) < 3 {
			continue
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

func quotaResetRemaining(window map[string]any, now time.Time) (time.Duration, bool) {
	if at, ok := window["resetsAt"].(time.Time); ok {
		return at.Sub(now), true
	}
	relative, ok := window["resetsIn"].(string)
	if !ok {
		return 0, false
	}
	return parseQuotaDuration(relative)
}

func parseQuotaDuration(value string) (time.Duration, bool) {
	var total time.Duration
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return 0, false
	}
	for _, field := range fields {
		if len(field) < 2 {
			return 0, false
		}
		n, err := strconv.Atoi(field[:len(field)-1])
		if err != nil || n < 0 {
			return 0, false
		}
		switch field[len(field)-1] {
		case 'd':
			total += time.Duration(n) * 24 * time.Hour
		case 'h':
			total += time.Duration(n) * time.Hour
		case 'm':
			total += time.Duration(n) * time.Minute
		default:
			return 0, false
		}
	}
	return total, true
}

func claudeRequestedModel(args []string) string {
	model := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		switch {
		case arg == "-m" || arg == "--model":
			if i+1 < len(args) {
				model = args[i+1]
				i++
			}
		case strings.HasPrefix(arg, "--model="):
			model = strings.TrimPrefix(arg, "--model=")
		case strings.HasPrefix(arg, "-m="):
			model = strings.TrimPrefix(arg, "-m=")
		}
	}
	return strings.ToLower(strings.TrimSpace(model))
}

func failureSuffix(failures []string) string {
	if len(failures) == 0 {
		return ""
	}
	return " (probe failures: " + strings.Join(failures, "; ") + ")"
}

func findClaudePromptBinary() (string, error) {
	if path, err := exec.LookPath("claude"); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".local", "bin", "claude")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("claude CLI not found")
	}
	return path, nil
}

func delegatedArgv(bin string, prefix, forwarded []string) []string {
	argv := make([]string, 0, 1+len(prefix)+len(forwarded))
	argv = append(argv, bin)
	argv = append(argv, prefix...)
	argv = append(argv, forwarded...)
	return argv
}

func execDelegated(bin string, prefix, forwarded, env []string) error {
	return syscall.Exec(bin, delegatedArgv(bin, prefix, forwarded), env)
}
