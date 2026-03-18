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

	// Build a clean environment without CLAUDECODE to avoid nested session detection.
	cleanEnv := os.Environ()
	for i := 0; i < len(cleanEnv); {
		if strings.HasPrefix(cleanEnv[i], "CLAUDECODE=") {
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
	if err := tmuxRun("new-session", "-d", "-s", session, "-x", "120", "-y", "40", "-c", home, "env", "-u", "CLAUDECODE", claudeBin); err != nil {
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

func parseCaptured(text string) (map[string]any, error) {
	usedRe := regexp.MustCompile(`(\d+)%\s*used`)
	matches := usedRe.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil, errors.New("could not parse claude quota from captured output")
	}

	type labelDef struct {
		search string
		key    string
	}
	labels := []labelDef{
		{"Current session", "session"},
		{"all models", "weeklyAll"},
		{"Sonnet only", "weeklySonnet"},
		{"Extra usage", "extra"},
	}

	spentRe := regexp.MustCompile(`(\$[\d.]+\s*/\s*\$[\d.]+)\s*spent`)
	resetsRe := regexp.MustCompile(`(?i)Resets?\s+(.+)`)

	out := map[string]any{}
	seen := map[string]bool{}

	for _, m := range matches {
		pos := m[0]
		pctStr := text[m[2]:m[3]]
		pct := atoi(pctStr)

		start := pos - 200
		if start < 0 {
			start = 0
		}
		before := text[start:pos]

		var key string
		bestPos := -1
		for _, l := range labels {
			idx := strings.LastIndex(before, l.search)
			if idx > bestPos {
				bestPos = idx
				key = l.key
			}
		}
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true

		entry := map[string]any{"used": pct, "left": 100 - pct}

		end := m[1] + 200
		if end > len(text) {
			end = len(text)
		}
		after := text[m[1]:end]
		for _, l := range labels {
			if idx := strings.Index(after, l.search); idx > 0 && l.key != key {
				after = after[:idx]
			}
		}

		if sm := spentRe.FindStringSubmatch(after); sm != nil {
			entry["spent"] = strings.ReplaceAll(sm[1], " ", "")
		}
		if rm := resetsRe.FindStringSubmatch(after); rm != nil {
			val := strings.TrimSpace(rm[1])
			if len(val) > 50 {
				val = val[:50]
			}
			entry["resetsIn"] = toRelative(val)
		}

		out[key] = entry
	}

	if len(out) == 0 {
		return nil, errors.New("could not parse claude quota from captured output")
	}
	return out, nil
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
