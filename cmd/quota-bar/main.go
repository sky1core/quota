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
	"time"

	"github.com/getlantern/systray"

	"github.com/sky1core/quota/internal/claude"
	"github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/ui"
)

const refreshEvery = 60 * time.Second
const staleThreshold = 5 * time.Minute

// menu item keys in display order
var allKeys = []string{
	"claude_session",
	"claude_weekly_all",
	"claude_weekly_sonnet",
	"claude_extra",
	"codex_5h",
	"codex_day",
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
	errs   map[string]string // "claude" or "codex" -> error message
}

func fetchQuota() quotaData {
	timeout := 90 * time.Second
	d := quotaData{
		values: map[string]string{},
		resets: map[string]string{},
		errs:   map[string]string{},
	}

	type result struct {
		provider string
		data     map[string]any
		err      error
	}
	ch := make(chan result, 2)

	go func() {
		cq, err := claude.GetQuota(timeout)
		ch <- result{"claude", cq, err}
	}()
	go func() {
		kq, err := codex.GetQuota(timeout)
		ch <- result{"codex", kq, err}
	}()

	for i := 0; i < 2; i++ {
		r := <-ch
		if r.err != nil {
			log.Printf("fetch %s error: %v", r.provider, r.err)
			d.errs[r.provider] = r.err.Error()
			continue
		}
		switch r.provider {
		case "claude":
			extract := func(mapKey, outKey string) {
				if s, ok := r.data[mapKey].(map[string]any); ok {
					if v, ok := s["left"].(int); ok {
						d.values[outKey] = fmt.Sprintf("%d%%", v)
					}
					if v, ok := s["resetsIn"].(string); ok {
						d.resets[outKey] = v
					}
				}
			}
			extract("session", "claude_session")
			extract("weeklyAll", "claude_weekly_all")
			extract("weeklySonnet", "claude_weekly_sonnet")
			extract("extra", "claude_extra")
		case "codex":
			extract := func(mapKey, outKey string) {
				if s, ok := r.data[mapKey].(map[string]any); ok {
					if v, ok := s["left"].(int); ok {
						d.values[outKey] = fmt.Sprintf("%d%%", v)
					}
					if v, ok := s["resetsIn"].(string); ok {
						d.resets[outKey] = v
					}
				}
			}
			extract("fiveHour", "codex_5h")
			extract("day", "codex_day")
		}
	}

	return d
}

// iconPct returns the lowest percentage among selected items, or 50 if none.
func iconPct(cfg settings, data quotaData) int {
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
func hasProviderData(d quotaData, prefix string) bool {
	for _, k := range allKeys {
		if strings.HasPrefix(k, prefix) {
			if _, ok := d.values[k]; ok {
				return true
			}
		}
	}
	return false
}

// providerOf returns "claude" or "codex" for a given key.
func providerOf(key string) string {
	if strings.HasPrefix(key, "claude_") {
		return "claude"
	}
	return "codex"
}

func barTitle(cfg settings, data quotaData, staleProviders map[string]bool) string {
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
	// Write PID
	_ = syscall.Ftruncate(fd, 0)
	pid := fmt.Sprintf("%d\n", os.Getpid())
	_, _ = syscall.Write(fd, []byte(pid))
	return fd
}

func main() {
	if os.Getenv("_QUOTA_BAR_DAEMON") != "1" {
		// Fork self into background and exit parent
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		cmd := exec.Command(exe, os.Args[1:]...)
		cmd.Env = append(os.Environ(), "_QUOTA_BAR_DAEMON=1")
		cmd.Stdout = nil
		cmd.Stderr = nil
		cmd.Stdin = nil
		if err := cmd.Start(); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}
	setupLog()
	if fd := acquireLock(); fd < 0 {
		log.Printf("another instance is already running, exiting")
		os.Exit(1)
	}
	log.Printf("quota-bar started (pid=%d)", os.Getpid())
	systray.Run(onReady, onExit)
}

func onExit() {}

func onReady() {
	icon := ui.GenIcon(50)
	systray.SetTemplateIcon(icon, icon)
	systray.SetTooltip("quota-bar")

	cfg := loadSettings()

	// -- Claude section --
	miClaudeHeader := systray.AddMenuItem("── Claude ──", "")
	miClaudeHeader.Disable()

	miClaudeErr := systray.AddMenuItem("", "")
	miClaudeErr.Hide()
	miClaudeErr.Disable()

	claudeItems := []menuItem{
		{"claude_session", systray.AddMenuItemCheckbox("Session  -", "", cfg.isSelected("claude_session"))},
		{"claude_weekly_all", systray.AddMenuItemCheckbox("Weekly  -", "", cfg.isSelected("claude_weekly_all"))},
		{"claude_weekly_sonnet", systray.AddMenuItemCheckbox("Sonnet  -", "", cfg.isSelected("claude_weekly_sonnet"))},
		{"claude_extra", systray.AddMenuItemCheckbox("Extra  -", "", cfg.isSelected("claude_extra"))},
	}

	// -- Codex section --
	miCodexHeader := systray.AddMenuItem("── Codex ──", "")
	miCodexHeader.Disable()

	miCodexErr := systray.AddMenuItem("", "")
	miCodexErr.Hide()
	miCodexErr.Disable()

	codexItems := []menuItem{
		{"codex_5h", systray.AddMenuItemCheckbox("5h: -", "", cfg.isSelected("codex_5h"))},
		{"codex_day", systray.AddMenuItemCheckbox("Day: -", "", cfg.isSelected("codex_day"))},
	}

	allItems := append(claudeItems, codexItems...)

	systray.AddSeparator()
	miUpdated := systray.AddMenuItem("Not yet updated", "")
	miUpdated.Disable()
	miRefresh := systray.AddMenuItem("Refresh", "Refresh now")
	miQuit := systray.AddMenuItem("Quit", "Quit")

	var (
		mu            sync.Mutex
		running       bool
		lastOK        quotaData
		lastSuccessAt = map[string]time.Time{}
	)

	labels := map[string]string{
		"claude_session":       "Session",
		"claude_weekly_all":    "Weekly",
		"claude_weekly_sonnet": "Sonnet",
		"claude_extra":         "Extra",
		"codex_5h":             "5h",
		"codex_day":            "Day",
	}

	getStaleProviders := func() map[string]bool {
		now := time.Now()
		sp := map[string]bool{}
		for _, p := range []string{"claude", "codex"} {
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
			lbl := labels[mi.key]
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

		if e, ok := data.errs["claude"]; ok {
			if len(e) > 120 {
				e = e[:120] + "…"
			}
			miClaudeErr.SetTitle("  Error: " + e)
			miClaudeErr.Show()
		} else {
			miClaudeErr.Hide()
		}

		if e, ok := data.errs["codex"]; ok {
			if len(e) > 120 {
				e = e[:120] + "…"
			}
			miCodexErr.SetTitle("  Error: " + e)
			miCodexErr.Show()
		} else {
			miCodexErr.Hide()
		}

		mu.Lock()
		cfgSnap := cfg
		mu.Unlock()
		systray.SetTitle(barTitle(cfgSnap, data, stale))
		iconData := ui.GenIcon(iconPct(cfgSnap, data))
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

	refresh := func() {
		mu.Lock()
		if running {
			mu.Unlock()
			return
		}
		running = true
		mu.Unlock()

		defer func() {
			mu.Lock()
			running = false
			mu.Unlock()
		}()

		data := fetchQuota()

		mu.Lock()
		// Keep last successful values for providers that failed
		if lastOK.values != nil {
			if _, hasErr := data.errs["claude"]; hasErr {
				for _, k := range allKeys {
					if strings.HasPrefix(k, "claude_") {
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
					}
				}
			}
			if _, hasErr := data.errs["codex"]; hasErr {
				for _, k := range allKeys {
					if strings.HasPrefix(k, "codex_") {
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
					}
				}
			}
		}
		// Update lastOK and lastSuccessAt for successful providers
		now := time.Now()
		if _, hasErr := data.errs["claude"]; !hasErr && hasProviderData(data, "claude_") {
			lastSuccessAt["claude"] = now
			if lastOK.values == nil {
				lastOK = quotaData{values: map[string]string{}, resets: map[string]string{}, errs: map[string]string{}}
			}
			for _, k := range allKeys {
				if strings.HasPrefix(k, "claude_") {
					if v, ok := data.values[k]; ok {
						lastOK.values[k] = v
					}
					if v, ok := data.resets[k]; ok {
						lastOK.resets[k] = v
					}
				}
			}
		}
		if _, hasErr := data.errs["codex"]; !hasErr && hasProviderData(data, "codex_") {
			lastSuccessAt["codex"] = now
			if lastOK.values == nil {
				lastOK = quotaData{values: map[string]string{}, resets: map[string]string{}, errs: map[string]string{}}
			}
			for _, k := range allKeys {
				if strings.HasPrefix(k, "codex_") {
					if v, ok := data.values[k]; ok {
						lastOK.values[k] = v
					}
					if v, ok := data.resets[k]; ok {
						lastOK.resets[k] = v
					}
				}
			}
		}
		mu.Unlock()

		renderMenu(data)
	}

	copyData := func(d quotaData) quotaData {
		c := quotaData{
			values: make(map[string]string, len(d.values)),
			resets: make(map[string]string, len(d.resets)),
			errs:   make(map[string]string, len(d.errs)),
		}
		for k, v := range d.values {
			c.values[k] = v
		}
		for k, v := range d.resets {
			c.resets[k] = v
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
			systray.SetTitle(barTitle(cfgSnap, data, stale))
			iconData := ui.GenIcon(iconPct(cfgSnap, data))
			systray.SetTemplateIcon(iconData, iconData)
		} else {
			systray.SetTitle("")
			iconData := ui.GenIcon(50)
			systray.SetTemplateIcon(iconData, iconData)
		}
	}

	go refresh()
	ticker := time.NewTicker(refreshEvery)
	go func() {
		for range ticker.C {
			refresh()
		}
	}()

	go func() {
		for {
			select {
			case <-claudeItems[0].item.ClickedCh:
				handleToggle(claudeItems[0])
			case <-claudeItems[1].item.ClickedCh:
				handleToggle(claudeItems[1])
			case <-claudeItems[2].item.ClickedCh:
				handleToggle(claudeItems[2])
			case <-claudeItems[3].item.ClickedCh:
				handleToggle(claudeItems[3])
			case <-codexItems[0].item.ClickedCh:
				handleToggle(codexItems[0])
			case <-codexItems[1].item.ClickedCh:
				handleToggle(codexItems[1])
			case <-miRefresh.ClickedCh:
				go refresh()
			case <-miQuit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}
