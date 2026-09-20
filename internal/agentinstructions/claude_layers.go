package agentinstructions

import (
	"context"
	"fmt"
	"sort"

	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/overlayruntime"
)

type claudeLayerInstructionHook struct {
	path  string
	event string
	index int
	owned bool
}

func (i *Installation) InspectClaudeRepositoryHooks(ctx context.Context, dir string) ([]string, error) {
	return i.inspectClaudeRepositoryHooks(ctx, dir, overlayruntime.ReadClaudeSettings)
}

func (i *Installation) inspectClaudeRepositoryHooks(ctx context.Context, dir string, readSettings func(string) (map[string]any, error)) ([]string, error) {
	sessions, err := overlayruntime.ClaudeSettingsLayersForConfigDir(ctx, dir, i.targets.ClaudeConfigDir)
	if err != nil {
		return nil, err
	}
	problems := []string{}
	for _, session := range sessions {
		entries := []claudeLayerInstructionHook{}
		counts := map[string]int{}
		for index, path := range session.Paths {
			root, err := readSettings(path)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s (session %s): %v", path, session.Directory, err))
				continue
			}
			found, err := i.claudeLayerInstructionHooks(root, path, index)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s (session %s): %v", path, session.Directory, err))
				continue
			}
			for _, entry := range found {
				counts[entry.event]++
			}
			entries = append(entries, found...)
		}
		for _, entry := range entries {
			if entry.index == 0 {
				continue
			}
			ownership := "unknown instruction command ownership"
			if entry.owned {
				ownership = "instruction hook belongs in account settings"
			}
			problems = append(problems, fmt.Sprintf("%s (session %s): hooks.%s conflicts with the fixed account installation (%s; %d instruction entries across active settings layers)", entry.path, session.Directory, entry.event, ownership, counts[entry.event]))
		}
	}
	sort.Strings(problems)
	return uniqueInstallStrings(problems), nil
}

func (i *Installation) claudeLayerInstructionHooks(root map[string]any, path string, index int) ([]claudeLayerInstructionHook, error) {
	hooks, err := agenthooks.ClaudeInstructionHooks(root, i.executable)
	if err != nil {
		return nil, err
	}
	var entries []claudeLayerInstructionHook
	for _, hook := range hooks {
		entries = append(entries, claudeLayerInstructionHook{path, hook.Event, index, hook.Owned})
	}
	return entries, nil
}
