package main

import (
	"fmt"
	"math"
	"strings"
)

func quotaFailureSuffix(failures []string) string {
	if len(failures) == 0 {
		return ""
	}
	return fmt.Sprintf("\nquota checks (cache max age %s):\n%s", cliCacheMaxAge, strings.Join(failures, "\n"))
}

func quotaSelectionFailure(key, provider, model string, result quotaProbeResult, minLeftPct float64) string {
	var out strings.Builder
	fmt.Fprintf(&out, "  %s: minLeftPct=%g%%", key, minLeftPct)
	if model != "" {
		fmt.Fprintf(&out, "; model=%s", model)
	}
	if result.err != nil {
		fmt.Fprintf(&out, "\n    quota lookup failed: %s", selectAgentErrorSummary(result.err))
		return out.String()
	}
	windows := quotaWindows(result.quota)
	modelWindow := findRequestedModelWindow(windows, model)
	applicable := 0
	for _, window := range windows {
		windowKey, _ := window["key"].(string)
		label, _ := window["label"].(string)
		fmt.Fprintf(&out, "\n    %s [%s]: ", label, windowKey)
		left, readable := numericValue(window["left"])
		readable = readable && !math.IsNaN(left) && !math.IsInf(left, 0) && left >= 0 && left <= 100
		if readable {
			fmt.Fprintf(&out, "left=%g%%", left)
		} else {
			out.WriteString("left=unreadable")
		}
		if provider == "claude" && windowKey != "session" && windowKey != "weekly_all" && (modelWindow == nil || windowKey != modelWindow["key"]) {
			out.WriteString("; not applicable to requested model")
			continue
		}
		applicable++
		required := minLeftPct
		mins, _ := numericValue(window["windowMins"])
		fiveHour := provider == "claude" && windowKey == "session" || provider == "codex" && mins == 300
		if fiveHour {
			required = math.Max(required, minFiveHourAdmissionPct)
		}
		fmt.Fprintf(&out, "; required>=%g%%", required)
		if fiveHour {
			fmt.Fprintf(&out, " (max(minLeftPct=%g%%, 5-hour minimum=%g%%))", minLeftPct, minFiveHourAdmissionPct)
		}
		switch {
		case !readable:
			out.WriteString("; invalid quota data")
		case left < required:
			out.WriteString("; insufficient")
		default:
			out.WriteString("; pass")
		}
		if reset, ok := window["resetsIn"].(string); ok && reset != "" {
			fmt.Fprintf(&out, "; resets in %s", reset)
		}
	}
	if reason := quotaAdmissionRejection(result.quota, provider); reason != "" {
		fmt.Fprintf(&out, "\n    rejected: %s", reason)
	} else if provider == "claude" && findWindowByKey(windows, "session") == nil && findWindowByKey(windows, "weekly_all") == nil {
		out.WriteString("\n    rejected: missing aggregate quota window (session or weekly_all)")
	} else if applicable == 0 {
		out.WriteString("\n    rejected: missing applicable quota window")
	}
	if provider == "claude" {
		if reason := claudeModelQuotaRejection(result.quota, model); reason != "" {
			fmt.Fprintf(&out, "\n    rejected: %s", reason)
		} else if model == "" {
			out.WriteString("\n    model not specified; model-specific quota not checked")
		} else if modelWindow == nil {
			out.WriteString("\n    matching model quota window not reported")
		}
	}
	return out.String()
}
