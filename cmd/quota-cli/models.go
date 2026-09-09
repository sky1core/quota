package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/modelcatalog"
)

type modelOptions struct {
	agent   string
	account string
	refresh bool
	jsonOut bool
}

type accountModels struct {
	Account  string                 `json:"account"`
	Provider string                 `json:"provider"`
	Catalog  *modelcatalog.Snapshot `json:"catalog,omitempty"`
	Error    string                 `json:"error,omitempty"`
}

func parseModelOptions(args []string, out io.Writer) (modelOptions, error) {
	var opts modelOptions
	if len(args) > 0 && args[0] == "refresh" {
		opts.refresh = true
		args = args[1:]
	}
	fs := flag.NewFlagSet("quota-cli models [refresh]", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&opts.agent, "agent", "all", "all, claude, or codex")
	fs.StringVar(&opts.account, "account", "", "configured account key")
	fs.BoolVar(&opts.jsonOut, "json", false, "Output JSON")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if fs.NArg() != 0 {
		return opts, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if opts.agent != "all" && opts.agent != "claude" && opts.agent != "codex" {
		return opts, fmt.Errorf("--agent must be all, claude, or codex")
	}
	return opts, nil
}

func runModels(args []string, stdout, stderr io.Writer) int {
	opts, err := parseModelOptions(args, stderr)
	if err == flag.ErrHelp {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	results, dirs, err := modelAccounts(cfg, opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), delegateProbeTimeout)
			defer cancel()
			snapshot, err := loadAccountModels(ctx, results[i].Provider, dirs[i], opts.refresh)
			if err != nil {
				results[i].Error = err.Error()
				return
			}
			results[i].Catalog = &snapshot
		}(i)
	}
	wg.Wait()
	code := 0
	for _, result := range results {
		if result.Error != "" {
			code = 1
		}
	}
	if opts.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return code
	}
	for _, result := range results {
		if result.Error != "" {
			fmt.Fprintf(stdout, "%s: %s\n", result.Account, result.Error)
			continue
		}
		s := result.Catalog
		fmt.Fprintf(stdout, "%s: %s, fetched %s\n", result.Account, s.CLIVersion, s.FetchedAt.Format(time.RFC3339))
		for _, m := range s.Models {
			efforts := "unknown"
			if m.SupportsEffort != nil && !*m.SupportsEffort {
				efforts = "unsupported"
			} else if len(m.SupportedEfforts) > 0 {
				efforts = strings.Join(m.SupportedEfforts, ", ")
			}
			fmt.Fprintf(stdout, "  %s", m.ID)
			if m.ResolvedModel != "" && m.ResolvedModel != m.ID {
				fmt.Fprintf(stdout, " -> %s", m.ResolvedModel)
			}
			fmt.Fprintf(stdout, " (effort: %s)\n", efforts)
		}
	}
	return code
}

func modelAccounts(cfg config.Config, opts modelOptions) ([]accountModels, []string, error) {
	results := []accountModels{}
	var dirs []string
	appendAccount := func(provider, key, dir string) {
		if opts.account == "" || opts.account == key {
			results = append(results, accountModels{Account: key, Provider: provider})
			dirs = append(dirs, dir)
		}
	}
	if opts.agent == "all" || opts.agent == "claude" {
		accounts, invalid := cfg.ResolveAccounts()
		if len(invalid) != 0 {
			return nil, nil, fmt.Errorf("invalid Claude accounts: %s", strings.Join(invalid, "; "))
		}
		for _, a := range accounts {
			appendAccount("claude", a.Key, a.ConfigDir)
		}
	}
	if opts.agent == "all" || opts.agent == "codex" {
		accounts, invalid := cfg.ResolveCodexAccounts()
		if len(invalid) != 0 {
			return nil, nil, fmt.Errorf("invalid Codex accounts: %s", strings.Join(invalid, "; "))
		}
		for _, a := range accounts {
			appendAccount("codex", a.Key, a.Home)
		}
	}
	if len(results) == 0 {
		return nil, nil, fmt.Errorf("no configured account matches --account=%s and --agent=%s", opts.account, opts.agent)
	}
	return results, dirs, nil
}

func loadAccountModels(ctx context.Context, provider, accountDir string, force bool) (modelcatalog.Snapshot, error) {
	target, err := modelTarget(provider, accountDir)
	if err != nil {
		return modelcatalog.Snapshot{}, err
	}
	cache := modelcatalog.Cache{Dir: filepath.Join(filepath.Dir(config.Path()), "model-cache")}
	return cache.Get(ctx, target, force)
}

func modelTarget(provider, accountDir string) (modelcatalog.Target, error) {
	var binary, envKey string
	var env []string
	var err error
	switch provider {
	case "claude":
		binary, err = findClaudePromptBinary()
		envKey = "CLAUDE_CONFIG_DIR"
	case "codex":
		binary, err = exec.LookPath("codex")
		envKey = "CODEX_HOME"
	default:
		return modelcatalog.Target{}, fmt.Errorf("unknown provider %q", provider)
	}
	if err != nil {
		return modelcatalog.Target{}, err
	}
	if accountDir == "" {
		accountDir = os.Getenv(envKey)
		if accountDir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return modelcatalog.Target{}, err
			}
			accountDir = filepath.Join(home, "."+provider)
		}
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return modelcatalog.Target{}, err
	}
	accountDir, err = filepath.Abs(accountDir)
	if err != nil {
		return modelcatalog.Target{}, err
	}
	if provider == "claude" {
		env = claude.EnvForConfigDir(os.Environ(), accountDir)
	} else {
		env = codex.EnvForHome(os.Environ(), accountDir)
	}
	return modelcatalog.Target{Provider: provider, Binary: binary, ConfigDir: accountDir, Env: env}, nil
}
