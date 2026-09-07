package agentoverlay

import "fmt"

func blockingBool(root map[string]any, key string, blocked bool) []string {
	value, exists := root[key]
	if !exists {
		return nil
	}
	b, ok := value.(bool)
	if !ok {
		return []string{key + " must be a boolean"}
	}
	if b == blocked {
		return []string{fmt.Sprintf("%s is %t; managed hooks are disabled", key, b)}
	}
	return nil
}

func groupArray(value any) []any {
	switch groups := value.(type) {
	case []any:
		return groups
	case []map[string]any:
		out := make([]any, len(groups))
		for i, group := range groups {
			out[i] = group
		}
		return out
	}
	return nil
}

func inspectHookEntry(groups any, expectedGroup map[string]any, command string, replaces []string) (string, string) {
	expectedHook := groupArray(expectedGroup["hooks"])[0].(map[string]any)
	replaceSet := map[string]bool{}
	for _, old := range replaces {
		replaceSet[old] = true
	}
	count := 0
	stale := false
	reason := ""
	for _, rawGroup := range groupArray(groups) {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			continue
		}
		for _, rawHook := range groupArray(group["hooks"]) {
			cmd, ok := hookAnyFormCommand(rawHook)
			if !ok {
				continue
			}
			if cmd != command {
				if replaceSet[cmd] {
					stale = true
				}
				continue
			}
			count++
			if diff := groupDifference(group, expectedGroup); diff != "" {
				reason = diff
				continue
			}
			hook, ok := rawHook.(map[string]any)
			if !ok {
				reason = "hook must use the command object form"
				continue
			}
			if diff := hookDifference(hook, expectedHook); diff != "" {
				reason = diff
			}
		}
	}
	if count > 1 {
		return StatusMismatch, "multiple entries match this managed command"
	}
	if reason != "" {
		return StatusMismatch, reason
	}
	if stale {
		return StatusMismatch, "a command listed in replaces is still present"
	}
	if count == 1 {
		return StatusPresent, ""
	}
	return StatusMissing, "hook entry is missing"
}

func groupDifference(actual, expected map[string]any) string {
	for _, key := range sortedKeys(actual) {
		if key == "hooks" {
			continue
		}
		if key == "matcher" && actual[key] == "" {
			continue
		}
		if want, ok := expected[key]; !ok || !settingsValueEqual(want, actual[key]) {
			return "unsupported group field: " + key
		}
	}
	for _, key := range sortedKeys(expected) {
		if key != "hooks" && !settingsValueEqual(expected[key], actual[key]) {
			return "group field differs: " + key
		}
	}
	return ""
}

func hookDifference(actual, expected map[string]any) string {
	for _, key := range sortedKeys(expected) {
		value, exists := actual[key]
		if !exists || !settingsValueEqual(expected[key], value) {
			return "hook field differs: " + key
		}
	}
	for _, key := range sortedKeys(actual) {
		if _, exists := expected[key]; exists {
			continue
		}
		if key == "statusMessage" {
			if _, ok := actual[key].(string); ok {
				continue
			}
		}
		return "unsupported hook field: " + key
	}
	return ""
}
