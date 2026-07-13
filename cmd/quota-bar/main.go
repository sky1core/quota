package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
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
	"github.com/sky1core/quota/internal/render"
	"github.com/sky1core/quota/internal/ui"
)

// version can be pinned at build time with -ldflags "-X main.version=vX.Y.Z".
// When left empty, versionString() derives it from the embedded build info, so a
// plain `go install <module>/cmd/quota-bar@vX.Y.Z` shows the tag with no ldflags.
var version = ""

// versionString resolves the version shown in the menu.
func versionString() string {
	bi, ok := debug.ReadBuildInfo()
	return resolveVersion(version, bi, ok)
}

// resolveVersion picks the version string from, in order: an explicit ldflags
// override; the module version of a `@version` install; the VCS revision of a
// local build; else "dev". Pure (takes build info as args) so it is testable.
func resolveVersion(ldflag string, bi *debug.BuildInfo, ok bool) string {
	if ldflag != "" {
		return ldflag
	}
	if !ok || bi == nil {
		return "dev"
	}
	if v := bi.Main.Version; v != "" && v != "(devel)" {
		return v // installed via `go install …@vX.Y.Z`
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			rev := s.Value
			if len(rev) > 7 {
				rev = rev[:7]
			}
			return rev // local build: short commit hash
		}
	}
	return "dev"
}

const (
	// defaultRefreshActive/defaultRefreshIdle are the built-in refresh cadences,
	// used when quota-bar.json does not override them (refreshActiveMinutes /
	// refreshIdleMinutes).
	defaultRefreshActive = 3 * time.Minute
	defaultRefreshIdle   = 30 * time.Minute
	idleThreshold        = 10 * time.Minute
	pauseThreshold       = 1 * time.Hour
	// staleMargin is added to the slower of the two (possibly configured) refresh
	// cadences to derive the stale threshold, so no normally-refreshed provider
	// trips a false "stale" warning regardless of which interval is larger.
	staleMargin = 5 * time.Minute
)

// claudeExtraSlots is the number of pre-allocated menu slots per Claude account
// for dynamic usage rows (per-model weekly quotas whose labels change across
// model generations). systray cannot remove items at runtime, so unused slots
// stay hidden.
const claudeExtraSlots = 3

// resetCreditSlots is the number of pre-allocated submenu rows under the Codex
// "Reset credits" item, one per usable reset credit (초기화권). systray cannot add items
// at runtime, so we allocate a fixed pool and hide the unused ones. The parent's
// count reflects the true number even if it exceeds the visible detail rows.
const resetCreditSlots = 8

// codexWindowSlotKeys are quota-bar's fixed Codex-window menu slots per account,
// keyed by a coarse duration bucket. This bucketing is a display/slot concern
// ONLY (systray can't add rows at runtime, and stable keys keep quota-bar.json
// selections valid): the row's on-screen LABEL is the truthful windowLabel from
// the data, never the bucket name. codexWindowBucket maps a window's actual
// duration to one of these slots.
var codexWindowSlotKeys = []string{"5h", "daily", "weekly", "monthly"}

// codexWindowBucket maps a window's actual duration (minutes) to its quota-bar
// slot key. Ranges (not exact values) so a duration tweak still lands in a slot.
// The returned string is one of codexWindowSlotKeys. This never becomes a label.
func codexWindowBucket(mins int) string {
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

// Menu item keys are "<provider>_<suffix>" where provider is an account key
// ("claude", "claude-2", …, "codex", "codex-2", …). Account keys match
// ^claude-\d+$ / ^codex-\d+$ (or the bare defaults "claude"/"codex"), so they
// never contain "_"; the first "_" always separates provider from suffix. Claude
// keeps its historical fixed keys ("claude_session", "claude_weekly_all") plus
// dynamic "claude_extra_N". Codex window slot keys are duration buckets
// ("codex_5h"/"codex_daily"/"codex_weekly"/"codex_monthly") — a stable slot id,
// not a label; the pre-bucket "codex_day" selection migrates to "codex_weekly".

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

// isDynamicSlotKey reports whether key is a dynamic, label-driven row: a Claude
// extra, or any Codex window slot (Codex's only checkbox rows). Its on-screen row
// exists only when the current refresh supplied a label for it (the window is
// present). Fixed rows (Session/Weekly) are not dynamic.
func isDynamicSlotKey(key string) bool {
	if strings.Contains(key, "_extra_") {
		return true
	}
	p := providerOf(key)
	return p == "codex" || strings.HasPrefix(p, "codex-")
}

type settings struct {
	Selected []string `json:"selected"`
	// ShowResetTime displays each row's reset as an absolute clock time
	// (e.g. "Jul 6 15:04") instead of the relative time left. Toggled from the
	// menu; default false keeps the historical relative display.
	ShowResetTime bool `json:"showResetTime"`
	// RefreshActiveMinutes / RefreshIdleMinutes override the built-in refresh
	// cadences (defaultRefreshActive / defaultRefreshIdle). They are only read
	// from the config file — there is no menu control. Absent or <= 0 means
	// "use the built-in default" (explicit default, never an implicit fallback).
	RefreshActiveMinutes int `json:"refreshActiveMinutes,omitempty"`
	RefreshIdleMinutes   int `json:"refreshIdleMinutes,omitempty"`
}

// activeInterval / idleInterval resolve the effective refresh cadences: the
// configured minutes when set to a positive value, otherwise the built-in
// default.
func (s settings) activeInterval() time.Duration {
	if s.RefreshActiveMinutes > 0 {
		return time.Duration(s.RefreshActiveMinutes) * time.Minute
	}
	return defaultRefreshActive
}

func (s settings) idleInterval() time.Duration {
	if s.RefreshIdleMinutes > 0 {
		return time.Duration(s.RefreshIdleMinutes) * time.Minute
	}
	return defaultRefreshIdle
}

// staleThreshold is the age past which a provider's last success is flagged
// stale. It trails the slower of the two cadences by staleMargin so a
// normally-refreshed provider is never marked stale, whichever interval is
// larger.
func (s settings) staleThreshold() time.Duration {
	return max(s.activeInterval(), s.idleInterval()) + staleMargin
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
	p := settingsPath()
	b, err := os.ReadFile(p)
	if err != nil {
		// A missing file is the normal first-run case; anything else (e.g. a
		// permission error) is worth surfacing rather than silently defaulting.
		if !os.IsNotExist(err) {
			log.Printf("loadSettings: read %s: %v (using defaults)", p, err)
		}
		return settings{}
	}
	var s settings
	if err := json.Unmarshal(b, &s); err != nil {
		// The file exists but is corrupt. Do NOT silently reset — a later
		// saveSettings would overwrite it and destroy the user's selections and
		// interval config. Preserve the original and log loudly so it can be
		// recovered.
		bak := p + ".corrupt"
		if renameErr := os.Rename(p, bak); renameErr != nil {
			log.Printf("loadSettings: parse %s: %v (using defaults; could not back up: %v)", p, err, renameErr)
		} else {
			log.Printf("loadSettings: parse %s: %v (using defaults; original preserved at %s)", p, err, bak)
		}
		return settings{}
	}
	return migrateSettings(s)
}

// isCodexDayKey reports whether key is a pre-bucket Codex "day" selection
// ("codex_day" / "codex-N_day"). That slot historically held the weekly
// (secondary) window, so it migrates to the weekly bucket key.
func isCodexDayKey(key string) bool {
	if !strings.HasSuffix(key, "_day") {
		return false
	}
	p := providerOf(key)
	return p == "codex" || strings.HasPrefix(p, "codex-")
}

// migrateSettings rewrites obsolete selection keys, once, and persists the
// result: the old third /usage row ("claude_weekly_sonnet") to the first dynamic
// extra slot, and the old Codex "day" slot (which actually held the weekly
// window) to the weekly duration bucket.
func migrateSettings(s settings) settings {
	changed := false
	for i, k := range s.Selected {
		switch {
		case k == "claude_weekly_sonnet":
			s.Selected[i] = "claude_extra_1"
			changed = true
		case isCodexDayKey(k):
			s.Selected[i] = strings.TrimSuffix(k, "_day") + "_weekly"
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
	values    map[string]string // key -> "95%"
	resets    map[string]string // key -> "4h 30m" (relative time left)
	resetsAbs map[string]string // key -> "Jul 6 15:04" (absolute reset time, when known)
	labels    map[string]string // dynamic slot key -> on-screen label, e.g. "claude_extra_1" -> "Fable"
	errs      map[string]string // provider key (Claude/Codex account key) -> error message
	// Codex reset credits (초기화권), keyed by Codex account key ("codex",
	// "codex-2", …). Display-only, not part of the keyed bar-selection machinery.
	// Each value is that account's usable grants, soonest-expiry first; a
	// missing/empty entry hides that account's Reset credits row. Stored
	// unformatted so the row text can follow the "Reset as clock time" toggle like
	// other rows.
	codexResetRows map[string][]resetRow
}

// resetRow is one usable Codex reset credit for display. rel is the relative
// "expires in" string (e.g. "1d 0h"); abs is the absolute expiry from
// FormatResetAt (e.g. "Jul 12 10:42"), "" when the grant has no expiry epoch.
// title is the grant title, shown only as a fallback when neither time is known.
type resetRow struct {
	rel   string
	abs   string
	title string
}

func newQuotaData() quotaData {
	return quotaData{
		values:         map[string]string{},
		resets:         map[string]string{},
		resetsAbs:      map[string]string{},
		labels:         map[string]string{},
		errs:           map[string]string{},
		codexResetRows: map[string][]resetRow{},
	}
}

// storeEntry copies one quota entry's display values (left/resetsIn/resetsAt)
// into d under outKey. quotaData's maps are reference types, so the value
// receiver mutates the caller's data.
func (d quotaData) storeEntry(e map[string]any, outKey string) {
	if v, ok := e["left"].(int); ok {
		d.values[outKey] = fmt.Sprintf("%d%%", v)
	}
	if v, ok := e["resetsIn"].(string); ok {
		d.resets[outKey] = v
	}
	if v, ok := e["resetsAt"].(time.Time); ok {
		d.resetsAbs[outKey] = render.FormatResetAt(v)
	}
}

// applyCodexWindows fills d with a Codex account's window rows from its
// self-describing windows list. Each window is routed to its duration slot
// (codexWindowBucket) and stored under the account's slot key with the window's
// own truthful label. Windows absent this refresh leave their slot untouched, so
// ANY window set renders with no code change — if the 5h window reappears later
// it simply repopulates the "5h" slot with label "5h"; a new tier (daily,
// monthly) lands in its own slot. Same-slot collisions keep the first window.
func (d quotaData) applyCodexWindows(provider string, data map[string]any) {
	ws, ok := data["windows"].([]map[string]any)
	if !ok {
		return
	}
	for _, w := range ws {
		mins, ok := w["windowMins"].(int)
		if !ok {
			continue
		}
		key := itemKey(provider, codexWindowBucket(mins))
		if _, taken := d.values[key]; taken {
			continue
		}
		if lbl, ok := w["label"].(string); ok && lbl != "" {
			d.labels[key] = lbl
		}
		d.storeEntry(w, key)
	}
}

// fetchQuota queries every Claude account plus every Codex account in parallel
// and stores the results under per-provider keys. Claude accounts write
// "<key>_session", "<key>_weekly_all", and "<key>_extra_N" (+ label); Codex
// accounts write one key per present window slot (see applyCodexWindows) and
// their reset credits under d.codexResetRows[<key>]. A provider's failure is
// recorded under d.errs[<provider key>]. Fetches run concurrently; results are
// consumed serially from a buffered channel, so the store maps are only ever
// touched by this goroutine.
func fetchQuota(accounts []config.ResolvedAccount, codexAccounts []config.ResolvedCodexAccount) quotaData {
	timeout := 90 * time.Second
	d := newQuotaData()

	extract := func(data map[string]any, mapKey, outKey string) {
		if e, ok := data[mapKey].(map[string]any); ok {
			d.storeEntry(e, outKey)
		}
	}

	type result struct {
		provider string // account key ("claude", "claude-2", …, "codex", "codex-2", …)
		claude   bool
		data     map[string]any
		err      error
	}
	ch := make(chan result, len(accounts)+len(codexAccounts))

	for _, a := range accounts {
		go func(a config.ResolvedAccount) {
			cq, err := claude.GetQuotaForConfigDir(timeout, a.ConfigDir)
			ch <- result{provider: a.Key, claude: true, data: cq, err: err}
		}(a)
	}
	for _, a := range codexAccounts {
		go func(a config.ResolvedCodexAccount) {
			kq, err := codex.GetQuotaForHome(timeout, a.Home)
			ch <- result{provider: a.Key, claude: false, data: kq, err: err}
		}(a)
	}

	total := len(accounts) + len(codexAccounts)
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
					d.storeEntry(e, key)
				}
			}
		} else {
			// Codex windows: self-describing list routed to duration slots, shown
			// under each window's own truthful label (see applyCodexWindows).
			d.applyCodexWindows(r.provider, r.data)
			if rc, ok := r.data["resetCredits"].(map[string]any); ok {
				d.codexResetRows[r.provider] = resetCreditRows(rc)
			}
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

// rowTitle composes a checkbox row's on-screen title from its label, value and
// reset string. Empty reset omits the parentheses. This is the exact text the
// menu shows, extracted so the relative↔absolute switch is unit-testable.
func rowTitle(label, value, reset string) string {
	if reset != "" {
		return fmt.Sprintf("%s %s (%s)", label, value, reset)
	}
	return fmt.Sprintf("%s %s", label, value)
}

// resetCreditRows extracts the usable Codex reset credits from the resetCredits
// payload into unformatted display rows (soonest-expiry first, as codex emits
// them). Formatting is deferred to resetRowTitles so the rows can follow the
// "Reset as clock time" toggle. Returns nil when there is nothing usable.
func resetCreditRows(rc map[string]any) []resetRow {
	items, _ := rc["items"].([]map[string]any)
	if len(items) == 0 {
		return nil
	}
	rows := make([]resetRow, 0, len(items))
	for _, it := range items {
		var r resetRow
		r.rel, _ = it["expiresIn"].(string)
		if at, ok := it["expiresAt"].(time.Time); ok {
			r.abs = render.FormatResetAt(at)
		}
		r.title, _ = it["title"].(string)
		rows = append(rows, r)
	}
	return rows
}

// resetRowTitles renders the "Reset credits" parent title and per-credit submenu lines
// for the current display mode. Each row's time follows the same relative↔clock
// switch as every other row (via resetText); a credit with no known expiry falls
// back to its title. The parent shows the usable count plus the soonest expiry.
// Returns ("", nil) when there are no usable credits, so the row stays hidden.
func resetRowTitles(rows []resetRow, showResetTime bool) (string, []string) {
	if len(rows) == 0 {
		return "", nil
	}
	parent := fmt.Sprintf("Reset credits: %d", len(rows))
	if soonest := resetText(showResetTime, rows[0].rel, rows[0].abs); soonest != "" {
		parent += "  (" + soonest + ")"
	}
	children := make([]string, len(rows))
	for i, r := range rows {
		s := resetText(showResetTime, r.rel, r.abs)
		if s == "" {
			// No expiry time known — fall back to the grant title.
			s = r.title
		}
		children[i] = s
	}
	return parent, children
}

// resetText picks the reset string shown in a row. When the user enabled clock
// mode and the row has a known absolute time, that is used; otherwise the
// relative time left (which is also the fallback for rows without a resetsAt,
// e.g. codex responses lacking resetsAt).
func resetText(showResetTime bool, rel, abs string) string {
	if showResetTime && abs != "" {
		return abs
	}
	return rel
}

// providerOf returns the provider portion of a menu key: the text before the
// first "_". Account keys (^claude-\d+$ / ^codex-\d+$ and the bare defaults)
// contain no "_", so "claude_session"→"claude", "claude-2_extra_1"→"claude-2",
// "codex_5h"→"codex", "codex-2_weekly"→"codex-2".
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

	// Effective refresh cadences (config override or built-in default), fixed at
	// startup like the account layout. Editing quota-bar.json requires a restart.
	// The stale threshold trails the *slower* of the two configured cadences by
	// staleMargin, so no normally-refreshed provider is ever marked stale — even
	// if the active interval is configured longer than the idle one.
	refreshActiveDur := cfg.activeInterval()
	refreshIdleDur := cfg.idleInterval()
	staleThresholdDur := cfg.staleThreshold()
	log.Printf("refresh cadence: active=%s idle=%s (stale>%s)", refreshActiveDur, refreshIdleDur, staleThresholdDur)

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
	codexAccounts, codexSkipped := appCfg.ResolveCodexAccounts()
	for _, s := range codexSkipped {
		log.Printf("config: %s", s)
	}

	// providers lists every provider key in refresh/stale order: each Claude
	// account key followed by each Codex account key.
	providers := make([]string, 0, len(accounts)+len(codexAccounts))
	for _, a := range accounts {
		providers = append(providers, a.Key)
	}
	for _, a := range codexAccounts {
		providers = append(providers, a.Key)
	}

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

	// -- Per-account Codex sections --
	// Each Codex account (default "codex" plus any "codex-N") gets its own header,
	// error row, one checkbox per window slot (codexWindowSlotKeys), and a Reset
	// credits parent+submenu. Window rows are keyed "<account>_<bucket>" (e.g.
	// "codex_5h", "codex_weekly") as a stable slot id only; their on-screen label
	// is dynamic (the truthful windowLabel from each refresh), so no static label
	// is set. miResetsByKey/resetChildrenByKey let renderRows paint each account's
	// reset credits independently.
	miResetsByKey := map[string]*systray.MenuItem{}
	resetChildrenByKey := map[string][]*systray.MenuItem{}
	for _, a := range codexAccounts {
		header := systray.AddMenuItem("── "+a.Label+" ──", "")
		header.Disable()

		errItem := systray.AddMenuItem("", "")
		errItem.Hide()
		errItem.Disable()
		errItems[a.Key] = errItem

		// One dynamic checkbox per window slot. Codex changes which windows are
		// present, so these rows are created hidden and shown by renderRows only
		// when the refresh supplied that slot's window (and its label).
		for _, slot := range codexWindowSlotKeys {
			k := itemKey(a.Key, slot)
			mi := systray.AddMenuItemCheckbox("-", "", cfg.isSelected(k))
			mi.Hide()
			allItems = append(allItems, menuItem{k, mi})
			allKeys = append(allKeys, k)
		}

		// Codex reset credits (초기화권): a parent row whose submenu lists each usable
		// credit's expiry. Display-only — not a checkbox, never shown in the top bar.
		// The parent opens the submenu; children are info rows. Both are left enabled
		// (not disabled) so the text renders at full contrast instead of the greyed,
		// hard-to-read disabled style; their clicks simply go unhandled (harmless —
		// systray drops sends on an unread channel). Both start hidden until a
		// refresh brings data. A fixed slot pool (resetCreditSlots) is pre-allocated
		// because systray cannot add items at runtime.
		miResets := systray.AddMenuItem("Reset credits: -", "")
		miResets.Hide()
		children := make([]*systray.MenuItem, resetCreditSlots)
		for i := 0; i < resetCreditSlots; i++ {
			ch := miResets.AddSubMenuItem("", "")
			ch.Hide()
			children[i] = ch
		}
		miResetsByKey[a.Key] = miResets
		resetChildrenByKey[a.Key] = children
	}

	systray.AddSeparator()
	miUpdated := systray.AddMenuItem("Not yet updated", "")
	miUpdated.Disable()
	miResetMode := systray.AddMenuItemCheckbox("Reset as clock time", "리셋을 남은시간 대신 절대 시각으로 표시", cfg.ShowResetTime)
	miRefresh := systray.AddMenuItem("Refresh", "Refresh now")
	miAutoStart := systray.AddMenuItemCheckbox("Start at Login", "", isAutoStartEnabled())
	miVersion := systray.AddMenuItem("quota-bar "+versionString(), "")
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
			if t, ok := lastSuccessAt[p]; ok && now.Sub(t) > staleThresholdDur {
				sp[p] = true
			}
		}
		return sp
	}

	// renderRows repaints only the per-item checkbox titles (label, value,
	// reset). It touches neither the error rows nor the "Updated" line, so a
	// pure display-mode change (showResetTime) can reuse it without wiping the
	// currently shown errors or faking a fresh update time.
	renderRows := func(data quotaData, stale map[string]bool, showResetTime bool) {
		for _, mi := range allItems {
			lbl := staticLabels[mi.key]
			if lbl == "" {
				lbl = data.labels[mi.key]
			}
			if isDynamicSlotKey(mi.key) {
				// Dynamic slot (Claude extra / Codex window): no label means the
				// row (that extra model, or that window) is absent this refresh.
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
			r := resetText(showResetTime, data.resets[mi.key], data.resetsAbs[mi.key])
			mi.item.SetTitle(rowTitle(lbl, val, r))
		}

		// Codex reset-credit rows (one block per Codex account) follow the same
		// relative↔clock switch. Painted here (not in renderMenu) so the toggle
		// repaints them too. Each account's parent + all children hide when that
		// account has no usable credits.
		for _, a := range codexAccounts {
			parent, children := resetRowTitles(data.codexResetRows[a.Key], showResetTime)
			mi := miResetsByKey[a.Key]
			if parent == "" {
				mi.Hide()
			} else {
				mi.SetTitle(parent)
				mi.Show()
			}
			chs := resetChildrenByKey[a.Key]
			for i, ch := range chs {
				if i < len(children) {
					ch.SetTitle(children[i])
					ch.Show()
				} else {
					ch.Hide()
				}
			}
		}
	}

	renderMenu := func(data quotaData) {
		mu.Lock()
		stale := getStaleProviders()
		showResetTime := cfg.ShowResetTime
		mu.Unlock()

		renderRows(data, stale, showResetTime)

		// One error row per provider (each Claude account + each Codex account).
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
		data := fetchQuota(accounts, codexAccounts)
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
				if v, ok := lastOK.resetsAbs[k]; ok {
					if _, exists := data.resetsAbs[k]; !exists {
						data.resetsAbs[k] = v
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
				delete(lastOK.resetsAbs, k)
				delete(lastOK.labels, k)
				if v, ok := data.values[k]; ok {
					lastOK.values[k] = v
				}
				if v, ok := data.resets[k]; ok {
					lastOK.resets[k] = v
				}
				if v, ok := data.resetsAbs[k]; ok {
					lastOK.resetsAbs[k] = v
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
		// Update lastOK and lastSuccessAt for providers that succeeded. Success is
		// "no fetch error", NOT "has ≥1 value": a provider can legitimately return
		// zero rows (e.g. Codex temporarily exposing no classifiable window), and we
		// must still snapshot it — so windows that vanished are cleared from lastOK
		// rather than resurrected by a later carry or toggle repaint — and mark it
		// fresh so a real success is never flagged stale. Claude always yields data
		// on success, so its behavior is unchanged.
		now := time.Now()
		for _, p := range providers {
			if _, hasErr := data.errs[p]; !hasErr {
				lastSuccessAt[p] = now
				if lastOK.values == nil {
					lastOK = newQuotaData()
				}
				snapshotProvider(p + "_")
			}
		}
		// Codex reset credits ride outside the keyed carry machinery. Per Codex
		// account: on success take the fresh value (even empty = credits all
		// gone/expired); on failure keep whatever we last showed for that account.
		for _, a := range codexAccounts {
			if _, failed := data.errs[a.Key]; failed {
				if lastOK.codexResetRows != nil {
					data.codexResetRows[a.Key] = lastOK.codexResetRows[a.Key]
				}
			} else {
				if lastOK.values == nil {
					lastOK = newQuotaData()
				}
				lastOK.codexResetRows[a.Key] = data.codexResetRows[a.Key]
			}
		}
		mu.Unlock()

		renderMenu(data)
		return true
	}

	copyData := func(d quotaData) quotaData {
		c := quotaData{
			values:         make(map[string]string, len(d.values)),
			resets:         make(map[string]string, len(d.resets)),
			resetsAbs:      make(map[string]string, len(d.resetsAbs)),
			labels:         make(map[string]string, len(d.labels)),
			errs:           make(map[string]string, len(d.errs)),
			codexResetRows: make(map[string][]resetRow, len(d.codexResetRows)),
		}
		for k, v := range d.values {
			c.values[k] = v
		}
		for k, v := range d.resets {
			c.resets[k] = v
		}
		for k, v := range d.resetsAbs {
			c.resetsAbs[k] = v
		}
		for k, v := range d.labels {
			c.labels[k] = v
		}
		for k, v := range d.errs {
			c.errs[k] = v
		}
		for k, rows := range d.codexResetRows {
			c.codexResetRows[k] = append([]resetRow(nil), rows...)
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
				interval = refreshIdleDur
			default:
				interval = refreshActiveDur
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
			case <-miResetMode.ClickedCh:
				mu.Lock()
				cfg.ShowResetTime = !cfg.ShowResetTime
				saveSettings(cfg)
				on := cfg.ShowResetTime
				stale := getStaleProviders()
				data := copyData(lastOK)
				mu.Unlock()
				if on {
					miResetMode.Check()
				} else {
					miResetMode.Uncheck()
				}
				// Repaint row titles only — leave error rows, the "Updated"
				// line, bar title and icon (all mode-independent) untouched.
				renderRows(data, stale, on)
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
