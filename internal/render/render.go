package render

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type item struct {
	label  string
	left   string
	resets string
}

func Text(payload map[string]any) string {
	codex := payload["codex"]
	errs, _ := payload["errors"].([]any)

	var b strings.Builder

	writeEntry := func(label string, v map[string]any) {
		it := item{label: label}
		if left, ok := v["left"].(int); ok {
			it.left = fmt.Sprintf("%d%%", left)
		}
		if r, ok := v["resetsIn"].(string); ok {
			it.resets = r
		}
		b.WriteString(fmtLine(it))
	}

	writeClaudeBody := func(m map[string]any) {
		fixed := []struct{ key, label string }{
			{"session", "Session"},
			{"weeklyAll", "Weekly"},
		}
		for _, f := range fixed {
			if v, ok := m[f.key].(map[string]any); ok {
				writeEntry(f.label, v)
			}
		}
		if extras, ok := m["extras"].([]map[string]any); ok {
			for _, e := range extras {
				if lbl, ok := e["label"].(string); ok && lbl != "" {
					writeEntry(lbl, e)
				}
			}
		}
	}

	// Claude accounts: default "claude" plus any "claude-N", in stable order.
	claudeKeys := claudeAccountKeys(payload)
	if len(claudeKeys) == 0 {
		b.WriteString("Claude\n")
		b.WriteString("  (no data)\n")
	} else {
		for i, k := range claudeKeys {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(claudeLabel(k) + "\n")
			m, _ := payload[k].(map[string]any)
			writeClaudeBody(m)
		}
	}

	b.WriteString("\nCodex\n")
	if m, ok := codex.(map[string]any); ok {
		codexItems := []struct{ key, label string }{
			{"fiveHour", "5h"},
			{"day", "Day"},
		}
		for _, ci := range codexItems {
			if v, ok := m[ci.key].(map[string]any); ok {
				writeEntry(ci.label, v)
			}
		}
	} else {
		b.WriteString("  (no data)\n")
	}

	if len(errs) > 0 {
		b.WriteString("\nErrors\n")
		for _, e := range errs {
			b.WriteString(fmt.Sprintf("  - %v\n", e))
		}
	}

	b.WriteString("\n")
	b.WriteString("Generated: " + time.Now().Format(time.RFC3339))
	return b.String()
}

// claudeAccountKeys returns the payload keys that hold a Claude account's quota
// ("claude" and any "claude-N"), ordered with the default first then by numeric
// suffix, with a string tiebreak for non-numeric suffixes.
func claudeAccountKeys(payload map[string]any) []string {
	var keys []string
	for k, v := range payload {
		if k != "claude" && !strings.HasPrefix(k, "claude-") {
			continue
		}
		if _, ok := v.(map[string]any); ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, oj := claudeOrder(keys[i]), claudeOrder(keys[j])
		if oi != oj {
			return oi < oj
		}
		return keys[i] < keys[j]
	})
	return keys
}

func claudeOrder(k string) int {
	if k == "claude" {
		return 0
	}
	if strings.HasPrefix(k, "claude-") {
		if n, err := strconv.Atoi(k[len("claude-"):]); err == nil {
			return n
		}
	}
	return 1 << 30 // unknown suffix sorts last
}

// claudeLabel renders a Claude account key as a section header.
// "claude" → "Claude", "claude-2" → "Claude 2".
func claudeLabel(k string) string {
	if k == "claude" {
		return "Claude"
	}
	if strings.HasPrefix(k, "claude-") {
		return "Claude " + k[len("claude-"):]
	}
	return k
}

func fmtLine(it item) string {
	line := fmt.Sprintf("  %-9s %4s", it.label, it.left)
	if it.resets != "" {
		if end, ok := endTime(it.resets); ok {
			line += fmt.Sprintf("   (%s, at %s)", it.resets, end)
		} else {
			line += fmt.Sprintf("   (%s)", it.resets)
		}
	}
	return line + "\n"
}

var reUnit = regexp.MustCompile(`(\d+)\s*(d|h|m)`)

// endTime parses a remaining-time string like "5d 18h", "1h 4m", "5m"
// and returns the absolute end time formatted as "15:04" or "Jan 2 15:04".
func endTime(remaining string) (string, bool) {
	matches := reUnit.FindAllStringSubmatch(remaining, -1)
	if len(matches) == 0 {
		return "", false
	}
	var d time.Duration
	for _, m := range matches {
		n, _ := strconv.Atoi(m[1])
		switch m[2] {
		case "d":
			d += time.Duration(n) * 24 * time.Hour
		case "h":
			d += time.Duration(n) * time.Hour
		case "m":
			d += time.Duration(n) * time.Minute
		}
	}
	t := time.Now().Add(d)
	if d >= 24*time.Hour {
		return t.Format("Jan 2 15:04"), true
	}
	return t.Format("15:04"), true
}
