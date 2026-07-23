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
	label   string
	left    string
	resets  string
	resetAt time.Time
	hasAt   bool
}

// FormatResetAt renders an absolute reset instant for display: local timezone,
// 24-hour clock, weekday first, then the date, year omitted — e.g.
// "Mon Jul 6 15:04". Shared by the CLI (render) and quota-bar so the format
// stays identical.
//
// The weekday leads because the quotas that matter reset weekly: "which day do
// I get it back" is the question a bare date makes the reader compute. The
// date stays alongside it so a reset more than a week out is unambiguous, and
// the shape never varies with how far away the instant is — one format for
// every row, so nothing shifts as time passes. The year is omitted because the
// longest-lived thing shown here is a 30-day reset credit.
func FormatResetAt(t time.Time) string {
	return t.Local().Format("Mon Jan 2 15:04")
}

// formatError renders one entry from the payload's "errors" list for humans.
// quota-cli emits map[string]any{"provider":.., "error":..}; render that as
// "provider: error" instead of dumping the raw Go map (e.g. "map[error:… provider:…]").
// Anything unexpected falls back to %v so nothing is ever dropped.
func formatError(e any) string {
	m, ok := e.(map[string]any)
	if !ok {
		return fmt.Sprintf("%v", e)
	}
	prov, _ := m["provider"].(string)
	msg, _ := m["error"].(string)
	switch {
	case prov != "" && msg != "":
		return prov + ": " + msg
	case prov != "":
		return prov
	case msg != "":
		return msg
	default:
		return fmt.Sprintf("%v", e)
	}
}

func Text(payload map[string]any) string {
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
		if at, ok := v["resetsAt"].(time.Time); ok {
			it.resetAt = at
			it.hasAt = true
		}
		b.WriteString(fmtLine(it))
	}

	// writeBody renders any provider's account body from the shared
	// self-describing window list: every entry carries its own label, so this has
	// no per-provider branch and no label vocabulary. A provider's extra sections
	// (Codex reset credits) follow its windows.
	writeBody := func(m map[string]any) {
		if ws, ok := m["windows"].([]map[string]any); ok {
			for _, w := range ws {
				if lbl, ok := w["label"].(string); ok && lbl != "" {
					writeEntry(lbl, w)
				}
			}
		}
		if rc, ok := m["resetCredits"].(map[string]any); ok {
			b.WriteString(fmtResetCredits(rc))
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
			writeBody(m)
		}
	}

	// Codex accounts: default "codex" plus any "codex-N", in stable order.
	codexKeys := codexAccountKeys(payload)
	if len(codexKeys) == 0 {
		b.WriteString("\nCodex\n")
		b.WriteString("  (no data)\n")
	} else {
		for _, k := range codexKeys {
			b.WriteString("\n" + codexLabel(k) + "\n")
			m, _ := payload[k].(map[string]any)
			writeBody(m)
		}
	}

	if len(errs) > 0 {
		b.WriteString("\nErrors\n")
		for _, e := range errs {
			b.WriteString("  - " + formatError(e) + "\n")
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

// codexAccountKeys returns the payload keys that hold a Codex account's quota
// ("codex" and any "codex-N"), ordered with the default first then by numeric
// suffix, with a string tiebreak for non-numeric suffixes. Mirrors
// claudeAccountKeys.
func codexAccountKeys(payload map[string]any) []string {
	var keys []string
	for k, v := range payload {
		if k != "codex" && !strings.HasPrefix(k, "codex-") {
			continue
		}
		if _, ok := v.(map[string]any); ok {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, oj := codexOrder(keys[i]), codexOrder(keys[j])
		if oi != oj {
			return oi < oj
		}
		return keys[i] < keys[j]
	})
	return keys
}

func codexOrder(k string) int {
	if k == "codex" {
		return 0
	}
	if strings.HasPrefix(k, "codex-") {
		if n, err := strconv.Atoi(k[len("codex-"):]); err == nil {
			return n
		}
	}
	return 1 << 30 // unknown suffix sorts last
}

// codexLabel renders a Codex account key as a section header.
// "codex" → "Codex", "codex-2" → "Codex 2".
func codexLabel(k string) string {
	if k == "codex" {
		return "Codex"
	}
	if strings.HasPrefix(k, "codex-") {
		return "Codex " + k[len("codex-"):]
	}
	return k
}

// fmtResetCredits renders the Codex reset-credit (초기화권) summary line:
// the number of usable grants plus, when known, the soonest expiry instant.
// "Reset credits" mirrors Codex's own term (the rateLimitResetCredits field).
// items are expected soonest-expiry first (as internal/codex emits them).
func fmtResetCredits(rc map[string]any) string {
	n, _ := rc["available"].(int)
	line := fmt.Sprintf("  Reset credits: %d", n)
	if items, ok := rc["items"].([]map[string]any); ok && len(items) > 0 {
		if at, ok := items[0]["expiresAt"].(time.Time); ok {
			line += fmt.Sprintf("   (expires %s)", FormatResetAt(at))
		}
	}
	return line + "\n"
}

func fmtLine(it item) string {
	line := fmt.Sprintf("  %-9s %4s", it.label, it.left)
	if it.resets != "" {
		// Prefer the exact reset instant (resetsAt); fall back to reconstructing
		// it from the relative string when the source gave us no absolute time.
		at := ""
		if it.hasAt {
			at = FormatResetAt(it.resetAt)
		} else if end, ok := endTime(it.resets); ok {
			at = end
		}
		if at != "" {
			line += fmt.Sprintf("   (%s, at %s)", it.resets, at)
		} else {
			line += fmt.Sprintf("   (%s)", it.resets)
		}
	}
	return line + "\n"
}

var reUnit = regexp.MustCompile(`(\d+)\s*(d|h|m)`)

// endTime parses a remaining-time string like "5d 18h", "1h 4m", "5m" and
// returns the absolute end time in the one display format (FormatResetAt), so
// a row reconstructed here has the same shape as one built on a provider's own
// resetsAt — both appear in the same output and used to differ.
//
// It runs only for rows that reach display without a resetsAt — Claude
// printing "Resets in 4h 30m", or a Codex window whose reset instant has
// already passed — so it never second-guesses a known resetsAt. Its precision
// is the source string's: a coarse "5d 18h" pins the start of the hour-wide
// range it names, not the exact instant.
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
	return FormatResetAt(time.Now().Add(d)), true
}
