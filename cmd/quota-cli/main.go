package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/render"
	"github.com/sky1core/quota/internal/update"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "update" {
		// No arguments: reject anything extra (including -h) instead of
		// silently ignoring it and running a network check/install.
		if len(os.Args) > 2 {
			fmt.Fprintln(os.Stderr, "usage: quota-cli update   (인자 없음 — 최신 릴리스로 재설치)")
			os.Exit(2)
		}
		os.Exit(runUpdate())
	}
	if len(os.Args) > 1 && os.Args[1] == "account" {
		os.Exit(runAccount(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "models" {
		os.Exit(runModels(os.Args[2:], os.Stdout, os.Stderr))
	}
	if len(os.Args) > 1 && os.Args[1] == "exec-prompt" {
		os.Exit(runExecPrompt(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "select-agent" {
		os.Exit(runSelectAgent(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "session-log" {
		os.Exit(runSessionLog(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		os.Exit(runAgent(os.Args[2:]))
	}
	runQuery()
}

func printExecPromptUsage() {
	fmt.Fprint(os.Stderr, `usage:
  quota-cli exec-prompt --model MODEL:EFFORT --model MODEL:EFFORT -- PROMPT
  quota-cli exec-prompt --agent=claude [args...]
  quota-cli exec-prompt --agent=codex  [args...]
`)
}

func runExecPrompt(args []string) int {
	return runExecPromptWith(args, runClaudePrompt, runCodexPrompt)
}

func runExecPromptWith(args []string, claudeRunner, codexRunner func([]string) int) int {
	if len(args) == 0 {
		printExecPromptUsage()
		return 2
	}

	switch args[0] {
	case "--agent=claude":
		return claudeRunner(args[1:])
	case "--agent=codex":
		return codexRunner(args[1:])
	default:
		opts, err := parseAutoPromptArgs(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			printExecPromptUsage()
			return 2
		}
		return runAutoPrompt(opts)
	}
}

// cliCacheMaxAge is how fresh a shared-cache entry must be for a CLI query to
// reuse it instead of probing live. Short, because a terminal query wants
// near-current numbers; the window still absorbs back-to-back runs.
const cliCacheMaxAge = 75 * time.Second

type queryOptions struct {
	jsonOut bool
	timeout time.Duration
}

func parseQueryArgs(args []string) (queryOptions, error) {
	fs, jsonOut, timeoutS := newQueryFlagSet(io.Discard)
	if err := fs.Parse(args); err != nil {
		return queryOptions{}, err
	}
	if fs.NArg() > 0 {
		return queryOptions{}, fmt.Errorf("unexpected argument: %q", fs.Arg(0))
	}
	return queryOptions{jsonOut: *jsonOut, timeout: time.Duration(*timeoutS) * time.Second}, nil
}

func newQueryFlagSet(output io.Writer) (*flag.FlagSet, *bool, *int) {
	fs := flag.NewFlagSet("quota-cli", flag.ContinueOnError)
	fs.SetOutput(output)
	jsonOut := fs.Bool("json", false, "Output JSON")
	timeoutS := fs.Int("timeout", 40, "Timeout seconds")
	return fs, jsonOut, timeoutS
}

func printQueryUsage() {
	fs, _, _ := newQueryFlagSet(os.Stderr)
	fs.Usage()
}

func runQuery() {
	opts, err := parseQueryArgs(os.Args[1:])
	if err == flag.ErrHelp {
		printQueryUsage()
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printQueryUsage()
		os.Exit(2)
	}

	out := map[string]any{}
	var errs []any
	var mu sync.Mutex

	addErr := func(provider, msg string) {
		mu.Lock()
		errs = append(errs, map[string]any{"provider": provider, "error": msg})
		mu.Unlock()
	}

	cfg, cerr := config.Load()
	if cerr != nil {
		addErr("config", cerr.Error())
	}
	accounts, skipped := cfg.ResolveAccounts()
	for _, s := range skipped {
		addErr("config", s)
	}
	codexAccounts, codexSkipped := cfg.ResolveCodexAccounts()
	for _, s := range codexSkipped {
		addErr("config", s)
	}

	var wg sync.WaitGroup

	// Query all Claude accounts in parallel.
	for _, a := range accounts {
		wg.Add(1)
		go func(a config.ResolvedAccount) {
			defer wg.Done()
			q, err := claude.GetQuotaForConfigDir(opts.timeout, a.ConfigDir, cliCacheMaxAge)
			if err != nil {
				addErr(a.Key, err.Error())
				return
			}
			mu.Lock()
			out[a.Key] = q
			mu.Unlock()
		}(a)
	}

	// Query all Codex accounts in parallel with Claude.
	for _, a := range codexAccounts {
		wg.Add(1)
		go func(a config.ResolvedCodexAccount) {
			defer wg.Done()
			kq, err := codex.GetQuotaForHome(opts.timeout, a.Home, cliCacheMaxAge)
			if err != nil {
				addErr(a.Key, err.Error())
				return
			}
			mu.Lock()
			out[a.Key] = kq
			mu.Unlock()
		}(a)
	}

	wg.Wait()

	if errs == nil {
		errs = []any{}
	}
	out["errors"] = errs

	if opts.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "json encode: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Println(render.Text(out))
}

// --- account subcommand ---------------------------------------------------

func printAccountUsage() {
	fmt.Fprint(os.Stderr, `usage:
  quota-cli account list              등록된 계정 목록 (Claude / Codex)
  quota-cli account add <key> <dir>   계정 추가
                                      key는 claude-<N> 또는 codex-<N> 형식
                                      dir은 Claude=CLAUDE_CONFIG_DIR, Codex=CODEX_HOME (~ 확장)
  quota-cli account rm  <key>         계정 제거

예시:
  quota-cli account add claude-2 ~/.claude-2
  quota-cli account add codex-2  ~/.codex-alt
`)
}

func runAccount(args []string) int {
	if len(args) == 0 {
		printAccountUsage()
		return 2
	}
	switch args[0] {
	case "list", "ls":
		return accountList()
	case "add":
		return accountAdd(args[1:])
	case "rm", "remove":
		return accountRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown account command: %q\n\n", args[0])
		printAccountUsage()
		return 2
	}
}

func accountList() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config load error:", err)
		return 1
	}
	fmt.Println("config:", config.Path())
	fmt.Println("Claude")
	fmt.Printf("  %-12s %s\n", "claude", "(기본 계정)")
	for _, a := range cfg.ClaudeAccounts {
		fmt.Printf("  %-12s %s\n", a.Key, a.ConfigDir)
	}
	fmt.Println("Codex")
	fmt.Printf("  %-12s %s\n", "codex", "(기본 계정)")
	for _, a := range cfg.CodexAccounts {
		fmt.Printf("  %-12s %s\n", a.Key, a.Home)
	}
	if len(cfg.ClaudeAccounts) == 0 && len(cfg.CodexAccounts) == 0 {
		fmt.Println("(추가 계정 없음 — 'quota-cli account add <claude-N|codex-N> <dir>'로 등록)")
	}
	return 0
}

// validateNewAccount checks a new account against existing entries. The key
// format rule is shared with query-time resolution via config.ClaudeExtraKeyRe.
// configDir uniqueness is compared after tilde expansion.
func validateNewAccount(existing []config.ClaudeAccount, key, dir string) error {
	if key == "" || dir == "" {
		return fmt.Errorf("key와 configDir 모두 필요")
	}
	if !config.ClaudeExtraKeyRe.MatchString(key) {
		return fmt.Errorf("key %q는 claude-<N> 형식이어야 함 (예: claude-2)", key)
	}
	exp := config.ExpandTilde(dir)
	for _, a := range existing {
		if a.Key == key {
			return fmt.Errorf("key %q는 이미 등록됨", key)
		}
		if config.ExpandTilde(a.ConfigDir) == exp {
			return fmt.Errorf("configDir가 기존 계정 %q와 동일한 위치를 가리킴", a.Key)
		}
	}
	return nil
}

// validateNewCodexAccount is the Codex sibling of validateNewAccount. The key
// format rule is shared with query-time resolution via config.CodexExtraKeyRe.
// home uniqueness is compared after tilde expansion.
func validateNewCodexAccount(existing []config.CodexAccount, key, home string) error {
	if key == "" || home == "" {
		return fmt.Errorf("key와 home 모두 필요")
	}
	if !config.CodexExtraKeyRe.MatchString(key) {
		return fmt.Errorf("key %q는 codex-<N> 형식이어야 함 (예: codex-2)", key)
	}
	exp := config.ExpandTilde(home)
	for _, a := range existing {
		if a.Key == key {
			return fmt.Errorf("key %q는 이미 등록됨", key)
		}
		if config.ExpandTilde(a.Home) == exp {
			return fmt.Errorf("home이 기존 계정 %q와 동일한 위치를 가리킴", a.Key)
		}
	}
	return nil
}

// accountAdd routes by key prefix: claude-<N> adds a Claude account, codex-<N> a
// Codex account. The provider is encoded in the key, matching the config schema.
func accountAdd(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: quota-cli account add <key> <dir>")
		return 2
	}
	key, dir := args[0], args[1]

	switch {
	case config.ClaudeExtraKeyRe.MatchString(key):
		return claudeAccountAdd(key, dir)
	case config.CodexExtraKeyRe.MatchString(key):
		return codexAccountAdd(key, dir)
	default:
		fmt.Fprintf(os.Stderr, "거부: key %q는 claude-<N> 또는 codex-<N> 형식이어야 함 (예: claude-2, codex-2)\n", key)
		return 1
	}
}

func claudeAccountAdd(key, dir string) int {
	err := updateAccountConfig(func(cfg config.Config, root map[string]any) error {
		if err := validateNewAccount(cfg.ClaudeAccounts, key, dir); err != nil {
			return err
		}
		rows, _ := root["claudeAccounts"].([]any)
		root["claudeAccounts"] = append(rows, map[string]any{"key": key, "configDir": dir})
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "config update error:", err)
		return 1
	}
	// Warn (do not fail) if the config dir doesn't exist yet.
	exp := config.ExpandTilde(dir)
	if fi, statErr := os.Stat(exp); statErr != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "경고: %s 가 없거나 디렉터리가 아님 — 해당 계정이 로그인돼 있는지 확인하세요\n", exp)
	}
	fmt.Printf("등록됨: %s → %s\n", key, dir)
	fmt.Println("확인: quota-cli   (계정이 로그인 안 돼 있으면 해당 계정만 errors로 표시됨)")
	return 0
}

func codexAccountAdd(key, dir string) int {
	err := updateAccountConfig(func(cfg config.Config, root map[string]any) error {
		if err := validateNewCodexAccount(cfg.CodexAccounts, key, dir); err != nil {
			return err
		}
		rows, _ := root["codexAccounts"].([]any)
		root["codexAccounts"] = append(rows, map[string]any{"key": key, "home": dir})
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "config update error:", err)
		return 1
	}
	// Warn (do not fail) if the CODEX_HOME doesn't exist yet.
	exp := config.ExpandTilde(dir)
	if fi, statErr := os.Stat(exp); statErr != nil || !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "경고: %s 가 없거나 디렉터리가 아님 — 해당 CODEX_HOME에 로그인돼 있는지 확인하세요\n", exp)
	}
	fmt.Printf("등록됨: %s → %s\n", key, dir)
	fmt.Println("확인: quota-cli   (해당 CODEX_HOME에 로그인 안 돼 있으면 그 계정만 errors로 표시됨)")
	return 0
}

// accountRemove routes by key prefix: codex-<N> removes a Codex account,
// everything else is looked up among Claude accounts (an unknown key simply is
// not found).
func accountRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: quota-cli account rm <key>")
		return 2
	}
	key := args[0]

	err := updateAccountConfig(func(_ config.Config, root map[string]any) error {
		field := "claudeAccounts"
		if config.CodexExtraKeyRe.MatchString(key) {
			field = "codexAccounts"
		}
		rows, _ := root[field].([]any)
		kept := make([]any, 0, len(rows))
		found := false
		for _, row := range rows {
			account, _ := row.(map[string]any)
			rowKey, _ := account["key"].(string)
			if rowKey == key {
				found = true
				continue
			}
			kept = append(kept, row)
		}
		if !found {
			return fmt.Errorf("계정 %q 없음", key)
		}
		root[field] = kept
		removeExecPromptAccountSetting(root, key)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "config update error:", err)
		return 1
	}
	fmt.Printf("제거됨: %s\n", key)
	return 0
}

func updateAccountConfig(update func(config.Config, map[string]any) error) error {
	return config.Update(func(root map[string]any) error {
		b, err := json.Marshal(root)
		if err != nil {
			return err
		}
		var cfg config.Config
		if err := json.Unmarshal(b, &cfg); err != nil {
			return err
		}
		return update(cfg, root)
	})
}

func removeExecPromptAccountSetting(root map[string]any, key string) {
	execPrompt, _ := root["execPrompt"].(map[string]any)
	settings, _ := execPrompt["accountSettings"].(map[string]any)
	if len(settings) == 0 {
		return
	}
	delete(settings, key)
	if len(settings) == 0 {
		delete(execPrompt, "accountSettings")
	}
	if len(execPrompt) == 0 {
		delete(root, "execPrompt")
	}
}

// --- update subcommand ----------------------------------------------------

// runUpdate installs the latest release tag of quota-cli over this binary.
// Manual only: quota-cli never updates itself as a side effect of anything
// else, and it never touches quota-bar (and vice versa).
func runUpdate() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cur := update.CurrentVersion()
	latest, err := update.Latest(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "최신 버전 확인 실패: %v\n", err)
		return 1
	}
	if cur == latest {
		fmt.Printf("이미 최신 버전입니다 (%s)\n", cur)
		return 0
	}
	fmt.Printf("업데이트: %s → %s\n", cur, latest)
	path, err := update.Install(ctx, "quota-cli", latest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "설치 실패: %v\n", err)
		return 1
	}
	fmt.Printf("설치 완료: %s (%s)\n", path, latest)
	return 0
}
