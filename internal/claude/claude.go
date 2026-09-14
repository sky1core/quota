package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sky1core/quota/internal/childprocess"
	"github.com/sky1core/quota/internal/quotacache"
)

// GetQuota fetches Claude Code quota for the default account, always probing
// live (maxAge 0 disables the shared cache read).
func GetQuota(timeout time.Duration) (map[string]any, error) {
	return GetQuotaForConfigDir(timeout, "", 0)
}

// GetQuotaForConfigDir fetches Claude Code quota for the account identified by
// configDir (its CLAUDE_CONFIG_DIR). An empty configDir queries the default
// account, identical to GetQuota. When the shared cache holds this account's
// last probe no older than maxAge, that raw output is re-parsed and returned
// instead of spawning a new probe; a live probe's result is written back. A
// non-positive maxAge skips the cache read but still refreshes it on success.
func GetQuotaForConfigDir(timeout time.Duration, configDir string, maxAge time.Duration) (map[string]any, error) {
	key := claudeCacheKey(configDir)
	if raw, ok := quotacache.Get(key, maxAge); ok {
		if res, err := parseUsage(raw); err == nil {
			return res, nil
		}
	}

	claudeBin, err := FindBinary()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	home, _ := os.UserHomeDir()
	// Run in ~/.config/quota rather than ~/: the Claude CLI treats its CWD as a
	// project root and runs readdir on it, which from ~ would touch
	// TCC-protected folders (Downloads, Photos, Music, Movies).
	safeDir := filepath.Join(home, ".config", "quota")
	_ = os.MkdirAll(safeDir, 0o755)

	// /usage is a local slash command: it reports the logged-in account's limits
	// without spending a turn (num_turns 0, total_cost_usd 0), so this probe can
	// run on a refresh timer without consuming quota to measure quota.
	cmd := childprocess.CommandContext(ctx, claudeBin, "-p", "/usage", "--output-format", "json")
	cmd.Dir = safeDir
	cmd.Env = fetchEnv(configDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if runErr := childprocess.Run(cmd); runErr != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("claude /usage timed out after %s", timeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if len(msg) > 500 {
			msg = msg[:500]
		}
		if msg != "" {
			return nil, fmt.Errorf("claude /usage failed: %w: %s", runErr, msg)
		}
		return nil, fmt.Errorf("claude /usage failed: %w", runErr)
	}

	text, err := usageText(stdout.Bytes())
	if err != nil {
		return nil, err
	}
	result, err := parseUsage(text)
	if err != nil {
		preview := text
		if len(preview) > 500 {
			preview = preview[:500]
		}
		return nil, fmt.Errorf("%w\n--- output ---\n%s", err, preview)
	}
	// Cache the raw report (parseable, so a success) for other consumers, bounded
	// by the soonest window reset so it is never served past that instant.
	quotacache.Put(key, text, earliestReset(result))
	return result, nil
}

func InvalidateCacheForConfigDir(configDir string) {
	quotacache.Delete(claudeCacheKey(configDir))
}

// earliestReset returns the soonest reset instant among a parsed result's
// windows, or the zero time when none carry one. It bounds the shared-cache
// entry: once the soonest window has reset, the cached raw is pre-reset and must
// not be re-parsed (parseReset would roll its now-past reset forward). Windows
// with only a relative reset carry no resetsAt and are skipped — they cannot
// roll forward, so maxAge alone bounds them.
func earliestReset(result map[string]any) time.Time {
	ws, _ := result["windows"].([]map[string]any)
	var earliest time.Time
	for _, w := range ws {
		at, ok := w["resetsAt"].(time.Time)
		if !ok {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return earliest
}

// claudeCacheKey is the shared-cache key for the account a given configDir
// selects. It is the account's RESOLVED location, not its logical name: an empty
// configDir means "the inherited CLAUDE_CONFIG_DIR, or ~/.claude" — exactly the
// account fetchEnv will probe. Keying on the resolved path is what stops a
// default-account entry cached under one inherited CLAUDE_CONFIG_DIR from being
// served to a run that inherited a different one.
func claudeCacheKey(configDir string) string {
	resolved := configDir
	if resolved == "" {
		resolved = os.Getenv("CLAUDE_CONFIG_DIR")
		if resolved == "" {
			home, _ := os.UserHomeDir()
			resolved = filepath.Join(home, ".claude")
		}
	}
	return "claude:" + filepath.Clean(resolved)
}

func FindBinary() (string, error) {
	if p, err := exec.LookPath("claude"); err == nil {
		return p, nil
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".local", "bin", "claude")
	if executable, err := exec.LookPath(p); err == nil {
		return executable, nil
	}
	return "", errors.New("claude CLI not found in PATH or native installation")
}

func fetchEnv(configDir string) []string {
	return EnvForConfigDir(os.Environ(), configDir)
}

func EnvForConfigDir(base []string, configDir string) []string {
	drop := map[string]bool{
		"CLAUDECODE":                      true,
		"ANTHROPIC_API_HOST":              true,
		"ANTHROPIC_API_KEY":               true,
		"ANTHROPIC_AUTH_TOKEN":            true,
		"ANTHROPIC_BASE_URL":              true,
		"CLAUDE_API_KEY":                  true,
		"CLAUDE_CODE_API_BASE_URL":        true,
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN": true,
		"CLAUDE_CODE_OAUTH_TOKEN":         true,
	}
	if configDir != "" {
		drop["CLAUDE_CONFIG_DIR"] = true
	}
	env := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if eq := strings.IndexByte(kv, '='); eq > 0 && drop[kv[:eq]] {
			continue
		}
		env = append(env, kv)
	}
	if configDir != "" {
		env = append(env, "CLAUDE_CONFIG_DIR="+configDir)
	}
	return env
}

// usageResponse is the subset of `claude -p --output-format json` this package
// consumes. The human-readable /usage screen arrives in Result; is_error is the
// CLI's own verdict on the run and is trusted over guessing from the text.
type usageResponse struct {
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Subtype string `json:"subtype"`
}

// usageText extracts the /usage report from the CLI's JSON envelope.
func usageText(out []byte) (string, error) {
	var resp usageResponse
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		preview := strings.TrimSpace(string(out))
		if len(preview) > 300 {
			preview = preview[:300]
		}
		return "", fmt.Errorf("claude /usage returned unreadable output: %w: %s", err, preview)
	}
	if resp.IsError {
		msg := strings.TrimSpace(resp.Result)
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("claude /usage reported an error: %s", msg)
	}
	return resp.Result, nil
}

// extraSlotVocab is how many per-model slots a slot-limited consumer should
// pre-allocate. It does NOT cap the data: parseUsage reports every row Claude
// shows, and a consumer with a finite menu simply ignores keys it has no slot
// for. Keeping this out of the parser is what stops quota-bar's systray limit
// from silently deleting rows from quota-cli.
const extraSlotVocab = 3

// WindowKeys returns the window keys a slot-limited consumer should pre-allocate
// (quota-bar; systray cannot add rows at runtime), in display order. It is a
// CONSUMER HINT, not a bound on the data: the emitted windows list may contain
// further keys (e.g. "extra_4"), which such a consumer ignores. A key is a
// stable slot/selection identity — never a label.
func WindowKeys() []string {
	keys := []string{"session", "weekly_all"}
	for i := 1; i <= extraSlotVocab; i++ {
		keys = append(keys, fmt.Sprintf("extra_%d", i))
	}
	return keys
}

// qualifierRe splits a trailing "(...)" qualifier off a /usage row label:
// "Current week (Fable)" → base "Current week", qualifier "Fable".
var qualifierRe = regexp.MustCompile(`^(.*?)\s*\((.+)\)\s*$`)

// windowLabel derives a row's display label from the /usage report text — the
// only truth Claude gives us, since Claude reports no window duration. It never
// substitutes a hardcoded vocabulary, so if Claude changes the period the label
// follows automatically:
//
//	"Current session"             → "Session"
//	"Current week (all models)"   → "Week"
//	"Current week (Fable)"        → "Fable"   (per-model row: the model names it)
//	"Current 5 days (all models)" → "5 days"  (period change flows through)
//
// Unrecognized text passes through unchanged rather than being renamed.
func windowLabel(screen string) string {
	s := strings.TrimSpace(screen)
	base := s
	if m := qualifierRe.FindStringSubmatch(s); m != nil {
		qual := strings.TrimSpace(m[2])
		if qual != "" && !strings.EqualFold(qual, "all models") {
			return qual
		}
		// Aggregate row: drop the "(all models)" qualifier, keep the period.
		base = strings.TrimSpace(m[1])
	}
	base = strings.TrimSpace(strings.TrimPrefix(base, "Current "))
	if base == "" {
		return s
	}
	r := []rune(base)
	return string(unicode.ToUpper(r[0])) + string(r[1:])
}

// usageRowRe matches one row of the /usage report, which states a window's
// label, its percentage, and (once the window has started) its reset time on a
// single line:
//
//	"Current week (all models): 35% used · resets Aug 3 at 12pm (Asia/Seoul)"
//	"Current session: 0% used"                       (window not started yet)
//
// Group 3 is everything after "N% used" and is handed to resetsClauseRe rather
// than being pinned here, so the row still parses if Claude restyles the
// separator between the percentage and the reset clause.
//
// The "Current " prefix is required because it is what separates quota rows
// from the rest of the report. Every quota row names a window that is running
// now; the "What's contributing" section below them is full of percentages and
// is one wording change away from colliding with a looser pattern (a line like
// "Last 24h: 73% used by subagent-heavy sessions" would otherwise land in the
// output as a model row).
//
// The trade-off is deliberate, and it is not symmetric. A false match reports a
// number that is not a quota, with nothing to mark it as wrong. A dropped prefix
// costs rows instead: if every row loses it the fetch fails outright, and if
// only some do the rest still come back — this parser allows partial results by
// contract, so those rows go quiet rather than loud. Losing rows that way is
// still the better failure, because the numbers that do arrive are real.
//
// Anchoring on the prefix rather than on the section header also keeps this
// independent of the report's decorative text, which changes between versions.
var usageRowRe = regexp.MustCompile(`^(Current\s+.*?):\s*(\d+)%\s+used\b(.*)$`)

// resetsClauseRe pulls the reset time out of the tail of a usage row. Case
// insensitive because this text is prose, not a field name.
var resetsClauseRe = regexp.MustCompile(`(?i)\bresets?\s+(.+?)\s*$`)

var usageRowLabelRe = regexp.MustCompile(`^(Current\s+[^:]+)(?::|$)`)

// parseUsage parses the /usage report line by line into the shared
// self-describing window list: out["windows"] = [{key,label,used,…}], in report
// order. Each row carries its own label derived from the report text
// (windowLabel), so no consumer holds a label vocabulary. The key is the row's
// structural identity — "session" and "weekly_all" for the two aggregate rows,
// "extra_N" for per-model rows (whose names change across model generations).
func parseUsage(text string) (map[string]any, error) {
	var windows []map[string]any
	var windowErrors []string
	modelWindowErrors := map[string]string{}
	seenExtra := map[string]bool{}
	recordWindowError := func(screen, problem string) {
		label := windowLabel(screen)
		message := fmt.Sprintf("claude quota row %q has %s", label, problem)
		if aggregateWindowKey(screen) != "" {
			windowErrors = append(windowErrors, message)
		} else if !seenExtra[label] {
			seenExtra[label] = true
			modelWindowErrors[label] = message
		}
	}
	seenKey := map[string]bool{}
	extraIdx := 0

	for _, raw := range strings.Split(stripANSI(text), "\n") {
		line := strings.TrimSpace(raw)
		m := usageRowRe.FindStringSubmatch(line)
		if m == nil {
			if mm := usageRowLabelRe.FindStringSubmatch(line); mm != nil {
				recordWindowError(mm[1], "an unreadable percentage")
			}
			continue
		}
		screen := strings.TrimSpace(m[1])
		if screen == "" {
			continue
		}
		pct, err := strconv.Atoi(m[2])
		if err != nil || pct < 0 || pct > 100 {
			recordWindowError(screen, "an out-of-range percentage")
			continue
		}

		entry := map[string]any{"used": pct, "left": 100 - pct}
		if rm := resetsClauseRe.FindStringSubmatch(m[3]); rm != nil {
			val := strings.TrimSpace(rm[1])
			if len(val) > 50 {
				val = val[:50]
			}
			rel, at, hasAt := parseReset(val)
			entry["resetsIn"] = rel
			if hasAt {
				entry["resetsAt"] = at
			}
		}

		label := windowLabel(screen)
		key := aggregateWindowKey(screen)
		if key == "" {
			// Per-model row. Dedupe by label so one model can't take two slots.
			// NOT capped here: the parser reports every row Claude shows. Slot
			// limits belong to consumers that have them (quota-bar), never to the
			// data — quota-cli must not lose a real row to a menu constraint.
			if seenExtra[label] {
				continue
			}
			seenExtra[label] = true
			extraIdx++
			key = fmt.Sprintf("extra_%d", extraIdx)
		}
		if seenKey[key] {
			continue
		}
		seenKey[key] = true
		entry["key"] = key
		entry["label"] = label
		windows = append(windows, entry)
	}

	if len(windows) == 0 {
		var modelErrors []string
		for _, message := range modelWindowErrors {
			modelErrors = append(modelErrors, message)
		}
		sort.Strings(modelErrors)
		if diagnostics := append(windowErrors, modelErrors...); len(diagnostics) > 0 {
			return nil, fmt.Errorf("could not parse claude quota from /usage output: %s", strings.Join(diagnostics, "; "))
		}
		if looksLikeSessionUsageSummary(text) {
			return nil, errors.New("could not find Claude plan quota rows in /usage output; only session usage summary was returned (check claude.ai subscription auth for this CLAUDE_CONFIG_DIR)")
		}
		return nil, errors.New("could not parse claude quota from /usage output")
	}
	out := map[string]any{"windows": windows}
	if len(windowErrors) > 0 {
		out["windowErrors"] = windowErrors
	}
	if len(modelWindowErrors) > 0 {
		out["modelWindowErrors"] = modelWindowErrors
	}
	return out, nil
}

func aggregateWindowKey(screen string) string {
	screen = strings.Join(strings.Fields(screen), " ")
	if strings.Contains(screen, "Current session") {
		return "session"
	}
	if strings.Contains(screen, "all models") {
		return "weekly_all"
	}
	return ""
}

func looksLikeSessionUsageSummary(text string) bool {
	clean := stripANSI(text)
	return strings.Contains(clean, "Total cost:") && strings.Contains(clean, "Usage:")
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// stripANSI removes terminal escape sequences. The JSON envelope has carried
// clean text so far; this keeps a future styled report from failing every row
// match at once, which would take quota down rather than degrade it.
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// parseReset normalizes a resets string into a relative "time left" string and,
// when the input is an absolute timestamp we can parse, the exact reset instant.
//
//	Already relative ("in 4h 30m" → "4h 30m"): at is zero, hasAt is false.
//	Absolute ("Mar 6, 12pm (Asia/Seoul)"): relative is derived and at is the
//	  parsed instant in the given (or local) timezone, hasAt is true.
//	Unparseable: relative is the cleaned string, at is zero, hasAt is false.
func parseReset(s string) (relative string, at time.Time, hasAt bool) {
	// Extract timezone from parens if present, e.g. "(Asia/Seoul)"
	var loc *time.Location
	if i := strings.Index(s, " ("); i > 0 {
		tzName := strings.TrimSpace(s[i+2:])
		tzName = strings.TrimSuffix(tzName, ")")
		if tz, err := time.LoadLocation(tzName); err == nil {
			loc = tz
		}
		s = strings.TrimSpace(s[:i])
	}

	// Already relative: "in 4h 30m" or "4h 30m" — no absolute instant available.
	s = strings.TrimPrefix(s, "in ")
	relRe := regexp.MustCompile(`^\d+[dhm]\s`)
	if relRe.MatchString(s + " ") {
		return s, time.Time{}, false
	}

	// Try to parse absolute time like "Mar 6, 12pm" or "Mar 6 at 12pm"
	s = strings.ReplaceAll(s, " at ", ", ")
	now := time.Now()
	if loc == nil {
		loc = now.Location()
	}
	nowInLoc := now.In(loc)
	formats := []string{
		"Jan 2, 3pm",
		"Jan 2, 3:04pm",
		"Jan 2",
		"3pm",
		"3:04pm",
	}
	timeOnlyFormats := map[string]bool{"3pm": true, "3:04pm": true}
	for _, layout := range formats {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}
		if timeOnlyFormats[layout] {
			// Time-only: use today's date in target timezone
			t = time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		} else {
			// Date format: use parsed month/day with current year in target timezone
			t = time.Date(nowInLoc.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		}
		// If in the past, advance until future
		if timeOnlyFormats[layout] {
			for t.Before(now) {
				t = t.AddDate(0, 0, 1)
			}
		} else {
			for t.Before(now) {
				t = t.AddDate(1, 0, 0)
			}
		}
		return fmtDuration(t.Sub(now)), t, true
	}

	return s, time.Time{}, false
}

// toRelative returns only the relative "time left" portion of parseReset.
// Already relative: "in 4h 30m" → "4h 30m"
// Absolute: "Mar 6, 12pm (Asia/Seoul)" → tries to parse and convert to "2d 5h"
// If parsing fails, returns the original string cleaned up.
func toRelative(s string) string {
	rel, _, _ := parseReset(s)
	return rel
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Minutes())
	days := total / (60 * 24)
	hours := (total / 60) % 24
	mins := total % 60
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}
