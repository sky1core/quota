package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"text/template"
	"time"

	"github.com/getlantern/systray"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/idle"
	"github.com/sky1core/quota/internal/ui"
)

var version = "dev"

const (
	refreshActive  = 3 * time.Minute
	refreshIdle    = 30 * time.Minute
	idleThreshold  = 10 * time.Minute
	pauseThreshold = 1 * time.Hour
	staleThreshold = 35 * time.Minute // > refreshIdle to avoid false stale during idle
)

// claudeExtraSlots is the number of pre-allocated menu slots per Claude account
// for dynamic usage rows (per-model weekly quotas whose labels change across
// model generations). systray cannot remove items at runtime, so unused slots
// stay hidden.
const claudeExtraSlots = 3

// Menu item keys are "<provider>_<suffix>" where provider is an account key
// ("claude", "claude-2", …) or "codex". Account keys match ^claude-\d+$, so
// they never contain "_"; the first "_" always separates provider from suffix.
// The default account keeps the historical keys ("claude_session",
// "claude_weekly_all", "claude_extra_1"..) so existing quota-bar.json selections
// stay valid.

// itemKey builds a menu key for a provider and fixed suffix, e.g.
// itemKey("claude-2", "session") == "claude-2_session".
func itemKey(provider, suffix string) string {
	return provider + "_" + suffix
}

// extraItemKey builds the key for a Claude account's i-th (0-based) dynamic
// extra slot, e.g. extraItemKey("claude", 0) == "claude_extra_1".
func extraItemKey(provider string, i int) string {
	return fmt.Sprintf("%s_extra_%d", provider, i+1)
}

// isClaudeExtraKey reports whether key is a dynamic extra slot for any account.
func isClaudeExtraKey(key string) bool {
	return strings.Contains(key, "_extra_")
}

type settings struct {
	Selected []string `json:"selected"`
}

func (s settings) isSelected(key string) bool {
	for _, k := range s.Selected {
		if k == key {
			return true
		}
	}
	return false
}

func (s *settings) toggle(key string) {
	if s.isSelected(key) {
		var out []string
		for _, k := range s.Selected {
			if k != key {
				out = append(out, k)
			}
		}
		s.Selected = out
	} else {
		s.Selected = append(s.Selected, key)
	}
}

func settingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "quota-bar.json")
}

func loadSettings() settings {
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		return settings{}
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		return settings{}
	}
	return migrateSettings(s)
}

// migrateSettings rewrites the old fixed key for the third /usage row
// ("claude_weekly_sonnet") to the first dynamic extra slot, once, and
// persists the result.
func migrateSettings(s settings) settings {
	changed := false
	for i, k := range s.Selected {
		if k == "claude_weekly_sonnet" {
			s.Selected[i] = "claude_extra_1"
			changed = true
		}
	}
	if !changed {
		return s
	}
	seen := map[string]bool{}
	var out []string
	for _, k := range s.Selected {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	s.Selected = out
	saveSettings(s)
	return s
}

func saveSettings(s settings) {
	if err := os.MkdirAll(filepath.Dir(settingsPath()), 0o755); err != nil {
		log.Printf("saveSettings: mkdir: %v", err)
		return
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		log.Printf("saveSettings: marshal: %v", err)
		return
	}
	if err := os.WriteFile(settingsPath(), b, 0o644); err != nil {
		log.Printf("saveSettings: write: %v", err)
	}
}

type quotaData struct {
	values map[string]string // key -> "95%"
	resets map[string]string // key -> "in 4h 30m"
	labels map[string]string // dynamic slot key -> on-screen label, e.g. "claude_extra_1" -> "Fable"
	errs   map[string]string // provider key (account key or "codex") -> error message
}

func newQuotaData() quotaData {
	return quotaData{
		values: map[string]string{},
		resets: map[string]string{},
		labels: map[string]string{},
		errs:   map[string]string{},
	}
}

// fetchQuota queries every Claude account plus Codex in parallel and stores the
// results under per-provider keys. Claude accounts write "<key>_session",
// "<key>_weekly_all", and "<key>_extra_N" (+ label); Codex writes "codex_5h" /
// "codex_day". A provider's failure is recorded under d.errs[<provider key>].
// Fetches run concurrently; results are consumed serially from a buffered
// channel, so the store maps are only ever touched by this goroutine.
func fetchQuota(accounts []config.ResolvedAccount) quotaData {
	timeout := 90 * time.Second
	d := newQuotaData()

	// storeEntry copies one quota entry's display values into d under outKey.
	storeEntry := func(e map[string]any, outKey string) {
		if v, ok := e["left"].(int); ok {
			d.values[outKey] = fmt.Sprintf("%d%%", v)
		}
		if v, ok := e["resetsIn"].(string); ok {
			d.resets[outKey] = v
		}
	}
	extract := func(data map[string]any, mapKey, outKey string) {
		if e, ok := data[mapKey].(map[string]any); ok {
			storeEntry(e, outKey)
		}
	}

	type result struct {
		provider string // account key ("claude", "claude-2", …) or "codex"
		claude   bool
		data     map[string]any
		err      error
	}
	ch := make(chan result, len(accounts)+1)

	for _, a := range accounts {
		go func(a config.ResolvedAccount) {
			cq, err := claude.GetQuotaForConfigDir(timeout, a.ConfigDir)
			ch <- result{provider: a.Key, claude: true, data: cq, err: err}
		}(a)
	}
	go func() {
		kq, err := codex.GetQuota(timeout)
		ch <- result{provider: "codex", claude: false, data: kq, err: err}
	}()

	total := len(accounts) + 1
	for i := 0; i < total; i++ {
		r := <-ch
		if r.err != nil {
			log.Printf("fetch %s error: %v", r.provider, r.err)
			d.errs[r.provider] = r.err.Error()
			continue
		}
		if r.claude {
			extract(r.data, "session", itemKey(r.provider, "session"))
			extract(r.data, "weeklyAll", itemKey(r.provider, "weekly_all"))
			if extras, ok := r.data["extras"].([]map[string]any); ok {
				for j, e := range extras {
					if j >= claudeExtraSlots {
						break
					}
					key := extraItemKey(r.provider, j)
					if lbl, ok := e["label"].(string); ok && lbl != "" {
						d.labels[key] = lbl
					}
					storeEntry(e, key)
				}
			}
		} else {
			extract(r.data, "fiveHour", "codex_5h")
			extract(r.data, "day", "codex_day")
		}
	}

	return d
}

// iconPct returns the lowest percentage among selected items, or 50 if none.
func iconPct(cfg settings, data quotaData, allKeys []string) int {
	min := -1
	for _, key := range allKeys {
		if cfg.isSelected(key) {
			if v, ok := data.values[key]; ok {
				var n int
				fmt.Sscanf(v, "%d%%", &n)
				if min < 0 || n < min {
					min = n
				}
			}
		}
	}
	if min < 0 {
		return 50
	}
	return min
}

// hasProviderData returns true if quotaData has at least one value for the given prefix.
func hasProviderData(d quotaData, prefix string, allKeys []string) bool {
	for _, k := range allKeys {
		if strings.HasPrefix(k, prefix) {
			if _, ok := d.values[k]; ok {
				return true
			}
		}
	}
	return false
}

// providerOf returns the provider portion of a menu key: the text before the
// first "_". Account keys (^claude-\d+$) and "codex" contain no "_", so
// "claude_session"→"claude", "claude-2_extra_1"→"claude-2", "codex_5h"→"codex".
func providerOf(key string) string {
	return strings.SplitN(key, "_", 2)[0]
}

func barTitle(cfg settings, data quotaData, staleProviders map[string]bool, allKeys []string) string {
	var parts []string
	for _, key := range allKeys {
		if cfg.isSelected(key) {
			if v, ok := data.values[key]; ok {
				if staleProviders[providerOf(key)] {
					v += "?"
				}
				parts = append(parts, v)
			}
		}
	}
	return strings.Join(parts, " ")
}

// shortReset trims verbose reset strings for menu display.
// "5:59pm (Asia/Seoul)" → "5:59pm"
// "Mar 6, 12pm (Asia/Seoul)" → "Mar 6, 12pm"
// "2h 39m" → "2h 39m" (unchanged)
func shortReset(s string) string {
	if s == "" {
		return ""
	}
	if i := strings.Index(s, " ("); i > 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

type menuItem struct {
	key  string
	item *systray.MenuItem
}

func logPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "quota-bar.log")
}

func setupLog() {
	p := logPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	log.SetOutput(f)
}

func pidLockPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "quota", "quota-bar.pid")
}

// acquireLock tries to acquire an exclusive flock on the PID file.
// Returns the file descriptor on success, or -1 if another instance is running.
func acquireLock() int {
	p := pidLockPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		log.Printf("acquireLock: mkdir: %v", err)
		return -1
	}
	fd, err := syscall.Open(p, syscall.O_CREAT|syscall.O_RDWR, 0o644)
	if err != nil {
		log.Printf("acquireLock: open: %v", err)
		return -1
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		syscall.Close(fd)
		return -1
	}
	syscall.CloseOnExec(fd)
	// Write PID
	_ = syscall.Ftruncate(fd, 0)
	pid := fmt.Sprintf("%d\n", os.Getpid())
	_, _ = syscall.Write(fd, []byte(pid))
	return fd
}

const launchLabel = "com.sky1core.quota-bar"

var plistTmpl = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{ .Label }}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{ .ExePath }}</string>
	</array>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>{{ .Path }}</string>
		<key>QUOTA_BAR_DAEMON</key>
		<string>1</string>
	</dict>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>WorkingDirectory</key>
	<string>{{ .Home }}</string>
	<key>ProcessType</key>
	<string>Interactive</string>
	<key>AssociatedBundleIdentifiers</key>
	<array>
		<string>com.sky1core.quota-bar</string>
	</array>
</dict>
</plist>
`))

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", launchLabel+".plist")
}

func isAutoStartEnabled() bool {
	_, err := os.Stat(launchAgentPath())
	return err == nil
}

func enableAutoStart() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	// Resolve symlinks to get the real path
	exePath, err = filepath.EvalSymlinks(exePath)
	if err != nil {
		return err
	}
	p := launchAgentPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	}
	home, _ := os.UserHomeDir()
	if err := plistTmpl.Execute(f, struct{ Label, ExePath, Path, Home string }{launchLabel, exePath, pathEnv, home}); err != nil {
		os.Remove(p)
		return err
	}
	if err := exec.Command("launchctl", "load", p).Run(); err != nil {
		os.Remove(p)
		return err
	}
	return nil
}

func disableAutoStart() error {
	p := launchAgentPath()
	_ = exec.Command("launchctl", "unload", p).Run()
	return os.Remove(p)
}

func main() {
	if os.Getenv("QUOTA_BAR_DAEMON") != "1" {
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Env = append(os.Environ(), "QUOTA_BAR_DAEMON=1")
		cmd.Stdout = nil
		cmd.Stderr = nil
		cmd.Stdin = nil
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}
	// Set CWD to home directory to prevent macOS permission dialogs
	// (launchd starts with CWD=/ which can trigger media/downloads access prompts)
	if home, err := os.UserHomeDir(); err == nil {
		_ = os.Chdir(home)
	}
	setupLog()
	if fd := acquireLock(); fd < 0 {
		log.Printf("another instance is already running, exiting")
		os.Exit(1)
	}
	log.Printf("quota-bar started (pid=%d)", os.Getpid())
	systray.Run(onReady, onExit)
}

var intentionalQuit bool

func onExit() {
	if !intentionalQuit {
		log.Printf("unexpected exit, exiting with code 1 for launchd restart")
		os.Exit(1)
	}
	log.Printf("intentional quit, exiting normally")
}

func onReady() {
	icon := ui.GenIcon(50)
	systray.SetTemplateIcon(icon, icon)
	systray.SetTooltip("quota-bar")

	cfg := loadSettings()

	// Resolve the Claude accounts to show. systray cannot add or remove menu
	// items at runtime, so the account set (and therefore the menu layout) is
	// fixed here at onReady from the config as it exists now. Editing
	// ~/.config/quota/config.json requires restarting quota-bar to take effect.
	appCfg, cfgErr := config.Load()
	if cfgErr != nil {
		log.Printf("config load: %v (default account only)", cfgErr)
	}
	accounts, skipped := appCfg.ResolveAccounts()
	for _, s := range skipped {
		log.Printf("config: %s", s)
	}

	// providers lists every provider key in refresh/stale order: each Claude
	// account key followed by "codex".
	providers := make([]string, 0, len(accounts)+1)
	for _, a := range accounts {
		providers = append(providers, a.Key)
	}
	providers = append(providers, "codex")

	var (
		allItems     []menuItem                       // every checkbox row, in display order
		allKeys      []string                         // every menu key, in display order
		errItems     = map[string]*systray.MenuItem{} // provider key -> hidden error row
		staticLabels = map[string]string{}            // fixed-label keys -> on-screen label
	)

	// -- Per-account Claude sections --
	for _, a := range accounts {
		header := systray.AddMenuItem("── "+a.Label+" ──", "")
		header.Disable()

		errItem := systray.AddMenuItem("", "")
		errItem.Hide()
		errItem.Disable()
		errItems[a.Key] = errItem

		sessionKey := itemKey(a.Key, "session")
		weeklyKey := itemKey(a.Key, "weekly_all")
		staticLabels[sessionKey] = "Session"
		staticLabels[weeklyKey] = "Weekly"

		allItems = append(allItems,
			menuItem{sessionKey, systray.AddMenuItemCheckbox("Session  -", "", cfg.isSelected(sessionKey))},
			menuItem{weeklyKey, systray.AddMenuItemCheckbox("Weekly  -", "", cfg.isSelected(weeklyKey))},
		)
		allKeys = append(allKeys, sessionKey, weeklyKey)

		for i := 0; i < claudeExtraSlots; i++ {
			key := extraItemKey(a.Key, i)
			mi := systray.AddMenuItemCheckbox("-", "", cfg.isSelected(key))
			mi.Hide()
			allItems = append(allItems, menuItem{key, mi})
			allKeys = append(allKeys, key)
		}
	}

	// -- Codex section --
	codexHeader := systray.AddMenuItem("── Codex ──", "")
	codexHeader.Disable()

	codexErr := systray.AddMenuItem("", "")
	codexErr.Hide()
	codexErr.Disable()
	errItems["codex"] = codexErr

	staticLabels["codex_5h"] = "5h"
	staticLabels["codex_day"] = "Day"
	allItems = append(allItems,
		menuItem{"codex_5h", systray.AddMenuItemCheckbox("5h: -", "", cfg.isSelected("codex_5h"))},
		menuItem{"codex_day", systray.AddMenuItemCheckbox("Day: -", "", cfg.isSelected("codex_day"))},
	)
	allKeys = append(allKeys, "codex_5h", "codex_day")

	systray.AddSeparator()
	miUpdated := systray.AddMenuItem("Not yet updated", "")
	miUpdated.Disable()
	miRefresh := systray.AddMenuItem("Refresh", "Refresh now")
	miAutoStart := systray.AddMenuItemCheckbox("Start at Login", "", isAutoStartEnabled())
	miVersion := systray.AddMenuItem("quota-bar "+version, "")
	miVersion.Disable()
	miQuit := systray.AddMenuItem("Quit", "Quit")

	var (
		mu            sync.Mutex
		running       bool
		lastOK        quotaData
		lastSuccessAt = map[string]time.Time{}
	)

	getStaleProviders := func() map[string]bool {
		now := time.Now()
		sp := map[string]bool{}
		for _, p := range providers {
			if t, ok := lastSuccessAt[p]; ok && now.Sub(t) > staleThreshold {
				sp[p] = true
			}
		}
		return sp
	}

	renderMenu := func(data quotaData) {
		mu.Lock()
		stale := getStaleProviders()
		mu.Unlock()

		for _, mi := range allItems {
			lbl := staticLabels[mi.key]
			if lbl == "" {
				lbl = data.labels[mi.key]
			}
			if isClaudeExtraKey(mi.key) {
				// Dynamic slot: no label means no such row on screen.
				if lbl == "" {
					mi.item.Hide()
					continue
				}
				mi.item.Show()
			}
			val := data.values[mi.key]
			if val == "" {
				val = "-"
			}
			if stale[providerOf(mi.key)] && val != "-" {
				val += "?"
			}
			r := data.resets[mi.key]
			if r != "" {
				mi.item.SetTitle(fmt.Sprintf("%s %s (%s)", lbl, val, r))
			} else {
				mi.item.SetTitle(fmt.Sprintf("%s %s", lbl, val))
			}
		}

		// One error row per provider (each Claude account + codex).
		for prov, item := range errItems {
			if e, ok := data.errs[prov]; ok {
				if len(e) > 120 {
					e = e[:120] + "…"
				}
				item.SetTitle("  Error: " + e)
				item.Show()
			} else {
				item.Hide()
			}
		}

		mu.Lock()
		cfgSnap := cfg
		mu.Unlock()
		systray.SetTitle(barTitle(cfgSnap, data, stale, allKeys))
		iconData := ui.GenIcon(iconPct(cfgSnap, data, allKeys))
		systray.SetTemplateIcon(iconData, iconData)

		updatedText := "Updated " + time.Now().Format("15:04")
		if len(stale) > 0 {
			var staleNames []string
			for p := range stale {
				mu.Lock()
				ago := time.Since(lastSuccessAt[p]).Truncate(time.Minute)
				mu.Unlock()
				staleNames = append(staleNames, fmt.Sprintf("%s %s ago", p, ago))
			}
			updatedText += " (" + strings.Join(staleNames, ", ") + "!)"
		}
		miUpdated.SetTitle(updatedText)
	}

	refresh := func() bool {
		mu.Lock()
		if running {
			mu.Unlock()
			return false
		}
		running = true
		mu.Unlock()

		defer func() {
			mu.Lock()
			running = false
			mu.Unlock()
		}()

		log.Printf("refresh start")
		data := fetchQuota(accounts)
		log.Printf("refresh done")

		mu.Lock()
		// carryProvider fills a failed provider's missing keys from the last
		// successful snapshot. prefix is exactly "<provider>_"; since account
		// keys never contain "_", "claude_" cannot match "claude-2_" rows.
		carryProvider := func(prefix string) {
			for _, k := range allKeys {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				if v, ok := lastOK.values[k]; ok {
					if _, exists := data.values[k]; !exists {
						data.values[k] = v
					}
				}
				if v, ok := lastOK.resets[k]; ok {
					if _, exists := data.resets[k]; !exists {
						data.resets[k] = v
					}
				}
				if v, ok := lastOK.labels[k]; ok {
					if _, exists := data.labels[k]; !exists {
						data.labels[k] = v
					}
				}
			}
		}
		// snapshotProvider replaces the provider's keys in lastOK with the
		// fresh result. Delete first so keys that vanished from the screen
		// (e.g. a retired extras row) don't linger and resurrect on a later
		// failed refresh.
		snapshotProvider := func(prefix string) {
			for _, k := range allKeys {
				if !strings.HasPrefix(k, prefix) {
					continue
				}
				delete(lastOK.values, k)
				delete(lastOK.resets, k)
				delete(lastOK.labels, k)
				if v, ok := data.values[k]; ok {
					lastOK.values[k] = v
				}
				if v, ok := data.resets[k]; ok {
					lastOK.resets[k] = v
				}
				if v, ok := data.labels[k]; ok {
					lastOK.labels[k] = v
				}
			}
		}
		// Keep last successful values for providers that failed this round.
		if lastOK.values != nil {
			for _, p := range providers {
				if _, hasErr := data.errs[p]; hasErr {
					carryProvider(p + "_")
				}
			}
		}
		// Update lastOK and lastSuccessAt for providers that succeeded.
		now := time.Now()
		for _, p := range providers {
			if _, hasErr := data.errs[p]; !hasErr && hasProviderData(data, p+"_", allKeys) {
				lastSuccessAt[p] = now
				if lastOK.values == nil {
					lastOK = newQuotaData()
				}
				snapshotProvider(p + "_")
			}
		}
		mu.Unlock()

		renderMenu(data)
		return true
	}

	copyData := func(d quotaData) quotaData {
		c := quotaData{
			values: make(map[string]string, len(d.values)),
			resets: make(map[string]string, len(d.resets)),
			labels: make(map[string]string, len(d.labels)),
			errs:   make(map[string]string, len(d.errs)),
		}
		for k, v := range d.values {
			c.values[k] = v
		}
		for k, v := range d.resets {
			c.resets[k] = v
		}
		for k, v := range d.labels {
			c.labels[k] = v
		}
		for k, v := range d.errs {
			c.errs[k] = v
		}
		return c
	}

	handleToggle := func(mi menuItem) {
		mu.Lock()
		cfg.toggle(mi.key)
		saveSettings(cfg)
		selected := cfg.isSelected(mi.key)
		cfgSnap := cfg
		data := copyData(lastOK)
		stale := getStaleProviders()
		mu.Unlock()
		if selected {
			mi.item.Check()
		} else {
			mi.item.Uncheck()
		}
		if data.values != nil {
			systray.SetTitle(barTitle(cfgSnap, data, stale, allKeys))
			iconData := ui.GenIcon(iconPct(cfgSnap, data, allKeys))
			systray.SetTemplateIcon(iconData, iconData)
		} else {
			systray.SetTitle("")
			iconData := ui.GenIcon(50)
			systray.SetTemplateIcon(iconData, iconData)
		}
	}

	go refresh()
	go func() {
		lastRefresh := time.Now()
		for {
			time.Sleep(30 * time.Second)
			idleSec := idle.Seconds()
			var interval time.Duration
			switch {
			case idleSec > pauseThreshold.Seconds():
				continue // paused, just re-check idle
			case idleSec > idleThreshold.Seconds():
				interval = refreshIdle
			default:
				interval = refreshActive
			}
			if time.Since(lastRefresh) >= interval {
				if refresh() {
					lastRefresh = time.Now()
				}
			}
		}
	}()

	// Funnel all checkbox clicks into one channel so toggles are handled
	// serially, alongside the other menu actions.
	toggleCh := make(chan menuItem)
	for _, mi := range allItems {
		go func(mi menuItem) {
			for range mi.item.ClickedCh {
				toggleCh <- mi
			}
		}(mi)
	}

	go func() {
		for {
			select {
			case mi := <-toggleCh:
				handleToggle(mi)
			case <-miRefresh.ClickedCh:
				go refresh()
			case <-miAutoStart.ClickedCh:
				if isAutoStartEnabled() {
					if err := disableAutoStart(); err != nil {
						log.Printf("disableAutoStart: %v", err)
					}
				} else {
					if err := enableAutoStart(); err != nil {
						log.Printf("enableAutoStart: %v", err)
					}
				}
				// Reflect actual state regardless of error
				if isAutoStartEnabled() {
					miAutoStart.Check()
				} else {
					miAutoStart.Uncheck()
				}
			case <-miQuit.ClickedCh:
				intentionalQuit = true
				systray.Quit()
				return
			}
		}
	}()
}
