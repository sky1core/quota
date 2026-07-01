package render

import (
	"fmt"
	"regexp"
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
	claude := payload["claude"]
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

	b.WriteString("Claude\n")
	if m, ok := claude.(map[string]any); ok {
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
	} else {
		b.WriteString("  (no data)\n")
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
