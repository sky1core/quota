package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/modelcatalog"
)

var errAutoPromptModels = errors.New("automatic exec-prompt requires one model per provider")

type promptModel struct {
	model  string
	effort string
}

type autoPromptOptions struct {
	models   [2]promptModel
	prompt   string
	readOnly bool
}

type autoPromptAccount struct {
	provider   string
	key        string
	dir        string
	model      promptModel
	minLeftPct float64
}

func parseAutoPromptArgs(args []string) (autoPromptOptions, error) {
	var opts autoPromptOptions
	count := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			if count != 2 || len(args[i+1:]) != 1 {
				return opts, fmt.Errorf("automatic exec-prompt requires exactly two --model MODEL:EFFORT options and exactly one prompt after --")
			}
			opts.prompt = args[i+1]
			return opts, nil
		}
		var value string
		switch {
		case arg == "--read-only":
			if opts.readOnly {
				return opts, fmt.Errorf("duplicate --read-only")
			}
			opts.readOnly = true
			continue
		case arg == "--model":
			if i+1 == len(args) {
				return opts, fmt.Errorf("--model requires MODEL:EFFORT")
			}
			i++
			value = args[i]
		case strings.HasPrefix(arg, "--model="):
			value = strings.TrimPrefix(arg, "--model=")
		default:
			return opts, fmt.Errorf("unexpected automatic exec-prompt argument: %q", arg)
		}
		if count == 2 {
			return opts, fmt.Errorf("automatic exec-prompt requires exactly two --model options")
		}
		model, effort, found := strings.Cut(value, ":")
		if !found || strings.TrimSpace(model) == "" {
			return opts, fmt.Errorf("--model requires a nonempty MODEL:EFFORT pair")
		}
		switch effort {
		case "low", "medium", "high", "xhigh", "max":
		default:
			return opts, fmt.Errorf("effort must be low, medium, high, xhigh, or max: %q", effort)
		}
		if count > 0 && opts.models[0].model == model {
			return opts, fmt.Errorf("automatic exec-prompt requires distinct models")
		}
		opts.models[count] = promptModel{model: model, effort: effort}
		count++
	}
	return opts, fmt.Errorf("automatic exec-prompt requires -- followed by exactly one prompt")
}

func loadAutoPromptCatalogs(ctx context.Context, cfg config.Config) ([]accountModels, error) {
	results, dirs, err := modelAccounts(cfg, modelOptions{agent: "codex"})
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, delegateProbeTimeout)
			defer cancel()
			snapshot, err := loadAccountModels(probeCtx, "codex", dirs[i], false)
			if err != nil {
				results[i].Error = err.Error()
				return
			}
			results[i].Catalog = &snapshot
		}(i)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func classifyAutoPromptModels(opts autoPromptOptions, catalogs []accountModels) (map[string]promptModel, error) {
	ids := make(map[string]bool)
	if len(catalogs) == 0 {
		return nil, fmt.Errorf("no Codex model catalogs")
	}
	for _, catalog := range catalogs {
		if catalog.Error != "" {
			return nil, fmt.Errorf("%s model catalog: %s", catalog.Account, catalog.Error)
		}
		if catalog.Provider != "codex" || catalog.Catalog == nil || len(catalog.Catalog.Models) == 0 {
			return nil, fmt.Errorf("%s: missing Codex model catalog", catalog.Account)
		}
		for _, model := range catalog.Catalog.Models {
			ids[model.ID] = true
		}
	}
	models := make(map[string]promptModel, 2)
	for _, model := range opts.models {
		provider := "claude"
		if ids[model.model] {
			provider = "codex"
		}
		if _, exists := models[provider]; exists {
			return nil, fmt.Errorf("%w; both models classify as %s", errAutoPromptModels, provider)
		}
		models[provider] = model
	}
	return models, nil
}

func catalogSupportsPromptModel(catalog modelcatalog.Snapshot, requested promptModel) bool {
	for _, model := range catalog.Models {
		if model.ID != requested.model || model.SupportsEffort == nil || !*model.SupportsEffort {
			continue
		}
		for _, effort := range model.SupportedEfforts {
			if effort == requested.effort {
				return true
			}
		}
	}
	return false
}

func autoPromptAccounts(cfg config.Config, opts autoPromptOptions, catalogs []accountModels) ([]autoPromptAccount, error) {
	accounts, dirs, err := modelAccounts(cfg, modelOptions{agent: "all"})
	if err != nil {
		return nil, err
	}
	models, err := classifyAutoPromptModels(opts, catalogs)
	if err != nil {
		return nil, err
	}
	byAccount := make(map[string]*modelcatalog.Snapshot, len(catalogs))
	for _, catalog := range catalogs {
		byAccount[catalog.Account] = catalog.Catalog
	}
	floors := make(map[string]float64, len(accounts))
	for _, provider := range []string{"claude", "codex"} {
		var keys []string
		for _, account := range accounts {
			if account.Provider == provider {
				keys = append(keys, account.Account)
			}
		}
		pcts, err := execPromptMinLeftPctByAccount(cfg, keys, provider)
		if err != nil {
			return nil, err
		}
		for i, key := range keys {
			floors[key] = pcts[i]
		}
	}
	var candidates []autoPromptAccount
	for i, account := range accounts {
		model := models[account.Provider]
		if account.Provider == "codex" {
			catalog := byAccount[account.Account]
			if catalog == nil {
				return nil, fmt.Errorf("%s: missing Codex model catalog", account.Account)
			}
			if !catalogSupportsPromptModel(*catalog, model) {
				continue
			}
		}
		candidates = append(candidates, autoPromptAccount{
			provider: account.Provider, key: account.Account, dir: dirs[i], model: model, minLeftPct: floors[account.Account],
		})
	}
	return candidates, nil
}

func selectAutoPromptAccount(ctx context.Context, cfg config.Config, opts autoPromptOptions, catalogs []accountModels) (autoPromptAccount, error) {
	accounts, err := autoPromptAccounts(cfg, opts, catalogs)
	if err != nil {
		return autoPromptAccount{}, err
	}
	results := make([]quotaProbeResult, len(accounts))
	var wg sync.WaitGroup
	for i, account := range accounts {
		wg.Add(1)
		go func(i int, account autoPromptAccount) {
			defer wg.Done()
			if account.provider == "claude" {
				results[i].quota, results[i].validity, results[i].err = claude.GetQuotaForConfigDirWithValidity(ctx, delegateProbeTimeout, account.dir, cliCacheMaxAge)
			} else {
				results[i].quota, results[i].validity, results[i].err = codex.GetQuotaForHomeWithValidity(ctx, delegateProbeTimeout, account.dir, cliCacheMaxAge)
			}
		}(i, account)
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return autoPromptAccount{}, err
	}
	now := time.Now()
	rejectExpiredQuotaResults(results, now)
	scores := make([]accountScore, len(accounts))
	usable := make([]bool, len(accounts))
	minLeftPcts := make([]float64, len(accounts))
	accountWindows := make([]map[int]map[string]any, len(accounts))
	var failures []string
	for i, result := range results {
		account := accounts[i]
		if result.err != nil {
			failures = append(failures, quotaSelectionFailure(account.key, account.provider, account.model.model, result, account.minLeftPct))
			continue
		}
		if account.provider == "claude" {
			scores[i], usable[i] = scoreClaudeQuota(result.quota, account.model.model, false, account.minLeftPct, now)
		} else {
			scores[i], usable[i] = scoreCodexQuota(result.quota, true, account.minLeftPct, now)
		}
		if !usable[i] {
			failures = append(failures, quotaSelectionFailure(account.key, account.provider, account.model.model, result, account.minLeftPct))
			continue
		}
		accountWindows[i] = aggregateQuotaWindowsByDuration(account.provider, result.quota)
		minLeftPcts[i] = account.minLeftPct
	}
	scores, err = scoreCommonQuotaPeriods(accountWindows, usable, minLeftPcts, now)
	if err != nil {
		return autoPromptAccount{}, err
	}
	best := selectBestScore(scores, usable)
	if best < 0 {
		return autoPromptAccount{}, fmt.Errorf("no account has usable quota for requested models and efforts%s", quotaFailureSuffix(failures))
	}
	return accounts[best], nil
}

func scoreCommonQuotaPeriods(accountWindows []map[int]map[string]any, usable []bool, minLeftPcts []float64, now time.Time) ([]accountScore, error) {
	var commonPeriods []int
	initialized := false
	for i, windows := range accountWindows {
		if !usable[i] {
			continue
		}
		if !initialized {
			for period := range windows {
				commonPeriods = append(commonPeriods, period)
			}
			initialized = true
		} else {
			var retained []int
			for _, period := range commonPeriods {
				if windows[period] != nil {
					retained = append(retained, period)
				}
			}
			commonPeriods = retained
		}
	}
	if initialized && len(commonPeriods) == 0 {
		return nil, fmt.Errorf("eligible accounts have no common quota window duration")
	}
	sort.Sort(sort.Reverse(sort.IntSlice(commonPeriods)))
	scores := make([]accountScore, len(accountWindows))
	for i, windows := range accountWindows {
		if !usable[i] {
			continue
		}
		var priority []map[string]any
		for _, period := range commonPeriods {
			priority = append(priority, windows[period])
		}
		scores[i], usable[i] = buildAccountScore(priority, minLeftPcts[i], now)
	}
	return scores, nil
}

func aggregateQuotaWindowsByDuration(provider string, quota map[string]any) map[int]map[string]any {
	windows := make(map[int]map[string]any)
	for _, window := range quotaWindows(quota) {
		if provider == "claude" {
			switch window["key"] {
			case "weekly_all":
				windows[10080] = window
			case "session":
				windows[300] = window
			}
		} else if mins, ok := numericValue(window["windowMins"]); ok && mins > 0 {
			windows[int(mins)] = window
		}
	}
	return windows
}

func autoPromptArgs(account autoPromptAccount, opts autoPromptOptions) []string {
	var args []string
	if account.provider == "claude" {
		args = []string{"-p", "--model", account.model.model, "--effort", account.model.effort}
		if opts.readOnly {
			args = append(args, "--tools", "Read,Glob,Grep", "--disallowedTools", "mcp__*")
		}
	} else {
		args = []string{"exec", "--model", account.model.model, "-c", `model_reasoning_effort="` + account.model.effort + `"`}
		if opts.readOnly {
			args = append(args, "--sandbox", "read-only")
		}
	}
	return append(args, "--", opts.prompt)
}

func runAutoPrompt(ctx context.Context, opts autoPromptOptions) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load error:", err)
		return 1
	}
	catalogs, err := loadAutoPromptCatalogs(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	account, err := selectAutoPromptAccount(ctx, cfg, opts, catalogs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errAutoPromptModels) {
			return 2
		}
		return 1
	}
	var bin string
	if account.provider == "claude" {
		bin, err = findClaudePromptBinary()
	} else {
		bin, err = exec.LookPath("codex")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := execDelegated(ctx, bin, nil, autoPromptArgs(account, opts), autoPromptEnv(account, os.Environ())); err != nil {
		fmt.Fprintln(os.Stderr, account.provider+" exec error:", err)
		return 1
	}
	return 0
}
