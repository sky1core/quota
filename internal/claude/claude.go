package claude

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// /usage settle timing: minimum grace before the first re-capture, poll
// interval between captures, and the hard cap on total settle wait.
const (
	usageSettleMin  = 2 * time.Second
	usageSettlePoll = 500 * time.Millisecond
	usageSettleMax  = 8 * time.Second
)

// GetQuota fetches Claude Code quota for the default account via tmux automation.
func GetQuota(timeout time.Duration) (map[string]any, error) {
	return GetQuotaForConfigDir(timeout, "")
}

// GetQuotaForConfigDir fetches Claude Code quota for the account identified by
// configDir (its CLAUDE_CONFIG_DIR). An empty configDir queries the default
// account, identical to GetQuota.
func GetQuotaForConfigDir(timeout time.Duration, configDir string) (map[string]any, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, errors.New("tmux not found in PATH")
	}

	claudeBin := "claude"
	if _, err := exec.LookPath(claudeBin); err != nil {
		home, _ := os.UserHomeDir()
		claudeBin = filepath.Join(home, ".local", "bin", "claude")
		if _, err := os.Stat(claudeBin); err != nil {
			return nil, errors.New("claude CLI not found")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Unique session name per fetch (pid + random nonce): parallel account
	// fetches in this process, other quota processes, and leftovers from a
	// crashed run can never collide on the name. The name is informational —
	// every tmux command below targets the session ID, never the name.
	session := fmt.Sprintf("quota-%d-%08x", os.Getpid(), rand.Uint32())

	// Build a clean environment for the spawned Claude CLI:
	//   - CLAUDECODE: drop to avoid nested session detection.
	//   - ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL: drop so quota is read from
	//     the user's logged-in Claude account rather than a custom endpoint.
	scrubKeys := map[string]bool{
		"CLAUDECODE":           true,
		"ANTHROPIC_AUTH_TOKEN": true,
		"ANTHROPIC_BASE_URL":   true,
	}
	cleanEnv := os.Environ()
	for i := 0; i < len(cleanEnv); {
		eq := strings.IndexByte(cleanEnv[i], '=')
		if eq > 0 && scrubKeys[cleanEnv[i][:eq]] {
			cleanEnv = append(cleanEnv[:i], cleanEnv[i+1:]...)
		} else {
			i++
		}
	}

	home, _ := os.UserHomeDir()
	// Use ~/.config/quota/ as CWD instead of ~/ to prevent Claude CLI from
	// scanning TCC-protected folders (Downloads, Photos, Music, Movies).
	// Claude CLI treats CWD as a project root and runs readdir on it.
	safeDir := filepath.Join(home, ".config", "quota")
	_ = os.MkdirAll(safeDir, 0o755)

	target, err := createClaudeSession(ctx, cleanEnv, session, safeDir, configDir, claudeBin)
	if err != nil {
		return nil, err
	}
	// Deferred only once the session exists, and scoped to its ID: this can
	// kill this fetch's session and nothing else. (It used to be deferred
	// before creation and targeted by name, so a failed or already-ended fetch
	// could tear down a sibling account's live session via prefix matching.)
	defer killTmuxSession(target)

	tmuxSend := func(keys ...string) {
		args := append([]string{"send-keys", "-t", target}, keys...)
		cmd := exec.CommandContext(ctx, "tmux", args...)
		cmd.Env = cleanEnv
		cmd.Stderr = nil
		_ = cmd.Run()
	}

	// waitFor polls the tmux pane until the text matches the predicate.
	waitFor := func(check func(string) bool) (string, error) {
		for {
			select {
			case <-ctx.Done():
				// Capture final state for diagnostics
				out, _ := exec.Command("tmux", "capture-pane", "-t", target, "-p").Output()
				return stripANSI(string(out)), errors.New("timeout")
			default:
			}
			out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", target, "-p").Output()
			if err != nil {
				return "", fmt.Errorf("failed to capture tmux pane: %w", err)
			}
			text := stripANSI(string(out))
			if check(text) {
				return text, nil
			}
			time.Sleep(500 * time.Millisecond)
		}
	}

	// Wait for Claude CLI to be ready (prompt appears)
	if _, err := waitFor(func(t string) bool {
		return strings.Contains(t, "Claude Code")
	}); err != nil {
		return nil, fmt.Errorf("waiting for claude to start: %w", err)
	}

	// Dismiss any initial prompt
	tmuxSend("Enter")
	time.Sleep(500 * time.Millisecond)

	// Send /usage command
	tmuxSend("/usage")
	time.Sleep(300 * time.Millisecond)
	tmuxSend("Enter")

	// Wait for usage data to appear. The gate is the data itself ("% used"),
	// never dialog chrome: /usage footer text changes across Claude versions
	// (v2.1.212 dropped "Esc to cancel", which this gate used to require —
	// every probe then timed out with the data fully on screen). Whether ALL
	// rows have rendered is the settle loop's job below.
	text, err := waitFor(func(t string) bool {
		if strings.Contains(t, "Error:") {
			return true
		}
		return strings.Contains(t, "% used")
	})
	if err != nil {
		return nil, err
	}

	// /usage rows render asynchronously; wait for the screen to settle —
	// two consecutive captures identical AND structurally complete (every
	// usage bar has its Resets line) — before parsing, so late rows (e.g.
	// per-model weekly) are included. Stability alone is not enough: the
	// screen can pause mid-render with bars drawn but reset lines still
	// loading, and parsing that frame silently emits rows with no reset time.
	// Bounded so an animated element (or a future layout whose bars have no
	// Resets lines at all) cannot stall the fetch forever: at the deadline the
	// latest capture is parsed as-is.
	settleDeadline := time.Now().Add(usageSettleMax)
	time.Sleep(usageSettleMin)
	captureDied := false
	for {
		out, capErr := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", target, "-p").Output()
		if capErr != nil {
			captureDied = true
			break
		}
		cur := stripANSI(string(out))
		settled := cur == text && screenComplete(cur)
		text = cur
		if settled || !time.Now().Before(settleDeadline) {
			break
		}
		time.Sleep(usageSettlePoll)
	}
	// A capture failure mid-settle means our session died under us (claude
	// exited, or the timeout hit). An incomplete leftover frame must fail
	// loudly here — parsing it would report real rows missing their reset
	// times, with nothing in the log.
	if captureDied && !screenComplete(text) {
		return nil, errors.New("usage capture interrupted before the screen finished rendering")
	}

	// Exit claude
	tmuxSend("Escape")
	time.Sleep(300 * time.Millisecond)
	tmuxSend("/exit", "Enter")
	time.Sleep(300 * time.Millisecond)

	result, err := parseCaptured(text)
	if err != nil {
		// Truncate for logging; keep first 500 chars of captured text.
		preview := text
		if len(preview) > 500 {
			preview = preview[:500]
		}
		return nil, fmt.Errorf("%w\n--- captured ---\n%s", err, preview)
	}
	return result, nil
}

// claudeSessionArgs builds the `tmux new-session` argument list that launches
// the Claude CLI. When configDir is non-empty it injects CLAUDE_CONFIG_DIR to
// select a specific account. Ordering is load-bearing:
//   - env's -u options must precede any NAME=VALUE assignment (macOS/BSD env
//     stops option parsing at the first non-option argument).
//   - CLAUDE_CONFIG_DIR is passed on the command line rather than inherited,
//     because an existing tmux server does not forward it to the new pane via
//     update-environment; command-line injection is server-state-independent.
//   - -P -F '#{session_id}' prints the new session's ID, which the caller must
//     use as the target of every later tmux command (see createClaudeSession).
func claudeSessionArgs(session, safeDir, configDir, claudeBin string) []string {
	args := []string{"new-session", "-d", "-P", "-F", "#{session_id}",
		"-s", session, "-x", "120", "-y", "40", "-c", safeDir,
		"env", "-u", "CLAUDECODE", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL"}
	if configDir != "" {
		args = append(args, "CLAUDE_CONFIG_DIR="+configDir)
	}
	return append(args, claudeBin)
}

// createClaudeSession launches the Claude CLI in a detached tmux session and
// returns the session ID (e.g. "$12"). Every later tmux command MUST target
// this ID, never the session name: tmux resolves a name target by prefix when
// no exact match exists, so once a session named "quota-1" is gone, commands
// aimed at it silently land on a session named "quota-1-<anything>" — another
// account's live fetch. That redirection is how one account's cleanup could
// kill the other account's probe mid-capture, and how one account could
// capture (and report) the other account's usage screen. A session ID is never
// reused, so a dead session is a hard error instead of someone else's data.
func createClaudeSession(ctx context.Context, env []string, session, safeDir, configDir, claudeBin string) (string, error) {
	// On failure the server may still have created the session (e.g. the
	// timeout expired mid-call, killing only the client). Remove it by exact
	// name — "=" disables prefix matching, and the name is unique to this
	// fetch, so this can never hit another session.
	killByExactName := func() {
		kctx, kcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer kcancel()
		_ = exec.CommandContext(kctx, "tmux", "kill-session", "-t", "="+session).Run()
	}
	cmd := exec.CommandContext(ctx, "tmux", claudeSessionArgs(session, safeDir, configDir, claudeBin)...)
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		killByExactName()
		return "", fmt.Errorf("failed to create tmux session: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if !strings.HasPrefix(id, "$") {
		killByExactName()
		return "", fmt.Errorf("tmux returned unexpected session id %q", id)
	}
	return id, nil
}

// killTmuxSession terminates one fetch's tmux session: the pane's process tree
// first, then the session itself. target must be a session ID from
// createClaudeSession; if that session is already gone, the pane lookup fails
// and nothing is killed — an ID never resolves to another session, unlike a
// name.
func killTmuxSession(target string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", "list-panes", "-t", target, "-F", "#{pane_pid}").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid := strings.TrimSpace(line)
		if pid != "" {
			_ = exec.CommandContext(ctx, "pkill", "-P", pid).Run()
			_ = exec.CommandContext(ctx, "kill", pid).Run()
		}
	}
	_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", target).Run()
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// usedLineRe matches a usage bar line like "██  4% used".
var usedLineRe = regexp.MustCompile(`(\d+)%\s*used`)

// resetsLineRe matches a reset line like "Resets Jul 6 at 11:59am (Asia/Seoul)".
var resetsLineRe = regexp.MustCompile(`(?i)^Resets?\s+(.+)`)

// extraSlotVocab is how many per-model slots a slot-limited consumer should
// pre-allocate. It does NOT cap the data: parseCaptured reports every row Claude
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

// windowLabel derives a row's display label from the /usage screen text — the
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

// parseCaptured parses the /usage screen line by line into the shared
// self-describing window list: out["windows"] = [{key,label,used,left,…}], in
// screen order. Each row carries its own label derived from the screen text
// (windowLabel), so no consumer holds a label vocabulary. The key is the row's
// structural identity — "session" and "weekly_all" for the two aggregate rows,
// "extra_N" for per-model rows (whose names change across model generations).
func parseCaptured(text string) (map[string]any, error) {
	lines := strings.Split(text, "\n")

	var windows []map[string]any
	seenKey := map[string]bool{}
	seenExtra := map[string]bool{}
	extraIdx := 0

	for i, line := range lines {
		m := usedLineRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		pct := atoi(line[m[2]:m[3]])

		// Screen text: same-line text before the bar, or the nearest line above.
		screen := stripBarChars(line[:m[0]])
		if screen == "" {
			screen = labelAbove(lines, i)
		}
		if screen == "" {
			continue
		}

		entry := map[string]any{"used": pct, "left": 100 - pct}
		if r := resetsBelow(lines, i); r != "" {
			rel, at, hasAt := parseReset(r)
			entry["resetsIn"] = rel
			if hasAt {
				entry["resetsAt"] = at
			}
		}

		label := windowLabel(screen)
		var key string
		switch {
		case strings.Contains(screen, "Current session"):
			key = "session"
		case strings.Contains(screen, "all models"):
			key = "weekly_all"
		default:
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
		return nil, errors.New("could not parse claude quota from captured output")
	}
	return map[string]any{"windows": windows}, nil
}

// screenComplete reports whether every usage bar on the captured /usage screen
// has its Resets line rendered below it. The screen paints progressively (a
// bar can appear a beat before its reset line), so a frame can be stable for a
// settle poll yet still incomplete — parsing it silently emits rows with no
// reset time. The settle loop uses this as a gate, not a requirement: at its
// deadline an incomplete screen is still parsed, so a future layout whose bars
// have no Resets lines degrades to a slower fetch, never to lost rows.
func screenComplete(text string) bool {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if usedLineRe.MatchString(line) && resetsBelow(lines, i) == "" {
			return false
		}
	}
	return true
}

// stripBarChars removes progress-bar block characters (U+2580–U+259F) and
// whitespace, leaving any same-line label text.
func stripBarChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x2580 && r <= 0x259F {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// labelAbove returns the nearest non-blank line above lines[i], which in the
// /usage layout is the row label. Looks at most 3 lines up; a bar or Resets
// line there means the block is malformed, so no label.
func labelAbove(lines []string, i int) string {
	for j := i - 1; j >= 0 && j >= i-3; j-- {
		t := strings.TrimSpace(lines[j])
		if t == "" {
			continue
		}
		if usedLineRe.MatchString(t) || resetsLineRe.MatchString(t) {
			return ""
		}
		return t
	}
	return ""
}

// resetsBelow returns the reset text from the nearest non-blank line below
// lines[i], or "" if that line is not a Resets row.
func resetsBelow(lines []string, i int) string {
	for j := i + 1; j < len(lines) && j <= i+3; j++ {
		t := strings.TrimSpace(lines[j])
		if t == "" {
			continue
		}
		rm := resetsLineRe.FindStringSubmatch(t)
		if rm == nil {
			return ""
		}
		val := strings.TrimSpace(rm[1])
		if len(val) > 50 {
			val = val[:50]
		}
		return val
	}
	return ""
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

func atoi(s string) int {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
