package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// /usage settle timing: minimum grace before the first re-capture, poll
// interval between captures, and the hard cap on total settle wait.
const (
	usageSettleMin  = 2 * time.Second
	usageSettlePoll = 500 * time.Millisecond
	usageSettleMax  = 8 * time.Second
)

// GetQuota fetches Claude Code quota via tmux automation.
func GetQuota(timeout time.Duration) (map[string]any, error) {
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

	session := fmt.Sprintf("quota-%d", os.Getpid())

	cleanup := func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanCancel()
		out, _ := exec.CommandContext(cleanCtx, "tmux", "list-panes", "-t", session, "-F", "#{pane_pid}").Output()
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			pid := strings.TrimSpace(line)
			if pid != "" {
				_ = exec.CommandContext(cleanCtx, "pkill", "-P", pid).Run()
				_ = exec.CommandContext(cleanCtx, "kill", pid).Run()
			}
		}
		_ = exec.CommandContext(cleanCtx, "tmux", "kill-session", "-t", session).Run()
	}
	defer cleanup()

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

	tmuxRun := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "tmux", args...)
		cmd.Env = cleanEnv
		cmd.Stderr = nil
		return cmd.Run()
	}

	tmuxSend := func(keys ...string) {
		args := append([]string{"send-keys", "-t", session}, keys...)
		_ = tmuxRun(args...)
	}

	home, _ := os.UserHomeDir()
	// Use ~/.config/quota/ as CWD instead of ~/ to prevent Claude CLI from
	// scanning TCC-protected folders (Downloads, Photos, Music, Movies).
	// Claude CLI treats CWD as a project root and runs readdir on it.
	safeDir := filepath.Join(home, ".config", "quota")
	_ = os.MkdirAll(safeDir, 0o755)
	if err := tmuxRun("new-session", "-d", "-s", session, "-x", "120", "-y", "40", "-c", safeDir, "env", "-u", "CLAUDECODE", "-u", "ANTHROPIC_AUTH_TOKEN", "-u", "ANTHROPIC_BASE_URL", claudeBin); err != nil {
		return nil, fmt.Errorf("failed to create tmux session: %w", err)
	}

	// waitFor polls the tmux pane until the text matches the predicate.
	waitFor := func(check func(string) bool) (string, error) {
		for {
			select {
			case <-ctx.Done():
				// Capture final state for diagnostics
				out, _ := exec.Command("tmux", "capture-pane", "-t", session, "-p").Output()
				return stripANSI(string(out)), errors.New("timeout")
			default:
			}
			out, err := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", session, "-p").Output()
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

	// Wait for usage data to fully load
	text, err := waitFor(func(t string) bool {
		if strings.Contains(t, "Error:") {
			return true
		}
		return strings.Contains(t, "% used") && strings.Contains(t, "Esc to cancel")
	})
	if err != nil {
		return nil, err
	}

	// /usage rows render asynchronously; wait for the screen to settle
	// (two consecutive captures identical) before parsing, so late rows
	// (e.g. per-model weekly) are included. Bounded so an animated element
	// cannot stall the fetch forever.
	settleDeadline := time.Now().Add(usageSettleMax)
	time.Sleep(usageSettleMin)
	for {
		out, capErr := exec.CommandContext(ctx, "tmux", "capture-pane", "-t", session, "-p").Output()
		if capErr != nil {
			break
		}
		cur := stripANSI(string(out))
		settled := cur == text
		text = cur
		if settled || !time.Now().Before(settleDeadline) {
			break
		}
		time.Sleep(usageSettlePoll)
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

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// usedLineRe matches a usage bar line like "██  4% used".
var usedLineRe = regexp.MustCompile(`(\d+)%\s*used`)

// resetsLineRe matches a reset line like "Resets Jul 6 at 11:59am (Asia/Seoul)".
var resetsLineRe = regexp.MustCompile(`(?i)^Resets?\s+(.+)`)

// weeklyLabelRe extracts the model name from a weekly row label,
// e.g. "Current week (Fable)" → "Fable".
var weeklyLabelRe = regexp.MustCompile(`^Current week \((.+)\)$`)

// parseCaptured parses the /usage screen line by line. "Current session" and
// "Current week (all models)" are structural fixed keys; any other usage row
// (per-model weekly quotas whose names change across model generations) is
// returned in the "extras" array with its on-screen label.
func parseCaptured(text string) (map[string]any, error) {
	lines := strings.Split(text, "\n")

	out := map[string]any{}
	extras := []map[string]any{}
	seen := map[string]bool{}

	for i, line := range lines {
		m := usedLineRe.FindStringSubmatchIndex(line)
		if m == nil {
			continue
		}
		pct := atoi(line[m[2]:m[3]])

		// Label: same-line text before the bar, or the nearest line above.
		label := stripBarChars(line[:m[0]])
		if label == "" {
			label = labelAbove(lines, i)
		}
		if label == "" {
			continue
		}

		entry := map[string]any{"used": pct, "left": 100 - pct}
		if r := resetsBelow(lines, i); r != "" {
			entry["resetsIn"] = toRelative(r)
		}

		switch {
		case strings.Contains(label, "Current session"):
			if !seen["session"] {
				seen["session"] = true
				out["session"] = entry
			}
		case strings.Contains(label, "all models"):
			if !seen["weeklyAll"] {
				seen["weeklyAll"] = true
				out["weeklyAll"] = entry
			}
		default:
			name := extraName(label)
			if !seen["extra:"+name] {
				seen["extra:"+name] = true
				entry["label"] = name
				extras = append(extras, entry)
			}
		}
	}

	if len(extras) > 0 {
		out["extras"] = extras
	}
	if len(out) == 0 {
		return nil, errors.New("could not parse claude quota from captured output")
	}
	return out, nil
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

// extraName returns the display name for a dynamic quota row: the text inside
// "Current week (...)" when present, otherwise the label as shown on screen.
func extraName(label string) string {
	if m := weeklyLabelRe.FindStringSubmatch(label); m != nil {
		if name := strings.TrimSpace(m[1]); name != "" {
			return name
		}
	}
	return label
}

// toRelative converts a resets string to relative time.
// Already relative: "in 4h 30m" → "4h 30m"
// Absolute: "Mar 6, 12pm (Asia/Seoul)" → tries to parse and convert to "2d 5h"
// If parsing fails, returns the original string cleaned up.
func toRelative(s string) string {
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

	// Already relative: "in 4h 30m" or "4h 30m"
	s = strings.TrimPrefix(s, "in ")
	relRe := regexp.MustCompile(`^\d+[dhm]\s`)
	if relRe.MatchString(s + " ") {
		return s
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
		return fmtDuration(t.Sub(now))
	}

	return s
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
