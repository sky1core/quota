package agentinstructions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sky1core/quota/internal/childprocess"
	codexprovider "github.com/sky1core/quota/internal/codex"
	"github.com/sky1core/quota/internal/config"
)

type NativeReport struct {
	hookMetadata []nativeHookMetadata
	State        string       `json:"state"`
	Hooks        []NativeHook `json:"hooks"`
	Issues       []string     `json:"issues,omitempty"`
}

type CodexTrustSyncResult struct {
	Path    string `json:"path"`
	Key     string `json:"key,omitempty"`
	Changed bool   `json:"changed"`
	Skipped bool   `json:"skipped,omitempty"`
}

type NativeHook struct {
	Event                  string  `json:"eventName"`
	Command                string  `json:"command,omitempty"`
	HandlerType            string  `json:"handlerType"`
	Source                 string  `json:"source"`
	SourcePath             string  `json:"sourcePath"`
	Enabled                *bool   `json:"enabled"`
	TrustStatus            string  `json:"trustStatus"`
	Matcher                *string `json:"matcher"`
	Async                  bool    `json:"async"`
	AdditionalContextLimit *uint64 `json:"additionalContextLimit"`
	Matched                bool    `json:"matched"`
	State                  string  `json:"state"`
}

type nativeHookMetadata struct {
	NativeHook
	Key         *string         `json:"key"`
	CurrentHash *string         `json:"currentHash"`
	IsManaged   *bool           `json:"isManaged"`
	AsyncValue  json.RawMessage `json:"async"`
}

func InspectNativeCodexHooksForHome(ctx context.Context, cwd, home string, expected map[string]string) (NativeReport, error) {
	for event, command := range expected {
		if (event != "sessionStart" && event != "subagentStart") || command == "" {
			return NativeReport{State: "unknown", Hooks: []NativeHook{}}, fmt.Errorf("invalid expected instruction hook event or command: %s", event)
		}
	}
	return inspectNativeCodexHooks(ctx, cwd, home, expected)
}

func (i *Installation) SyncCodexHookTrust(ctx context.Context, cwd string) (CodexTrustSyncResult, error) {
	i, err := i.resolveSelectedTargets([]string{"codex"})
	if err != nil {
		return CodexTrustSyncResult{}, err
	}
	result := CodexTrustSyncResult{Path: i.targets.CodexConfig}
	key, hash, syncNeeded, skipped, err := i.codexHookTrustMetadata(ctx, cwd)
	if err != nil {
		return result, err
	}
	result.Key = key
	result.Skipped = skipped
	if skipped || !syncNeeded {
		return result, nil
	}
	changed, err := i.updateTOMLTrust(i.targets.CodexConfig, key, hash)
	if err != nil {
		return result, err
	}
	result.Changed = changed
	return result, nil
}

func (i *Installation) PlanCodexHookTrust(ctx context.Context, cwd string, installHookChanged bool) (*InstallChange, error) {
	i, err := i.resolveSelectedTargets([]string{"codex"})
	if err != nil {
		return nil, err
	}
	if _, err := exec.LookPath("codex"); err != nil {
		return nil, nil
	}
	if err := checkInstallPath(i.targets.CodexConfig); err != nil {
		return nil, err
	}
	if installHookChanged {
		if _, err := inspectNativeCodexHooks(ctx, cwd, i.targets.CodexHome, nil); err != nil {
			return nil, err
		}
		return &InstallChange{Account: i.targets.CodexAccount, Agent: "codex", Path: i.targets.CodexConfig, Changed: true, Operation: "sync managed hook trust"}, nil
	}
	key, hash, syncNeeded, skipped, err := i.codexHookTrustMetadata(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if skipped || !syncNeeded {
		return nil, nil
	}
	before, err := readInstallFile(i.targets.CodexConfig)
	if err != nil {
		return nil, err
	}
	after, err := i.trustCodexTOML(before, key, hash)
	if err != nil {
		return nil, err
	}
	return &InstallChange{Account: i.targets.CodexAccount, Agent: "codex", Path: i.targets.CodexConfig, Changed: string(before) != string(after), Operation: "sync managed hook trust"}, nil
}

func (i *Installation) codexHookTrustMetadata(ctx context.Context, cwd string) (key, hash string, syncNeeded, skipped bool, err error) {
	if _, err := exec.LookPath("codex"); err != nil {
		return "", "", false, true, nil
	}
	report, err := inspectNativeCodexHooks(ctx, cwd, i.targets.CodexHome, i.ExpectedCodexHooks())
	if err != nil {
		return "", "", false, false, err
	}
	var matched *nativeHookMetadata
	expected := i.ExpectedCodexHooks()
	for n := range report.hookMetadata {
		hook := &report.hookMetadata[n]
		if hook.Event != "sessionStart" || hook.Command != expected["sessionStart"] || hook.HandlerType != "command" || !sameNativeCodexHookSource(hook.SourcePath, i.targets.CodexHooks) {
			continue
		}
		if matched != nil {
			return "", "", false, false, errors.New("multiple managed Codex instruction hooks matched")
		}
		matched = hook
	}
	if matched == nil {
		return "", "", false, false, errors.New("managed Codex instruction hook was not discovered")
	}
	switch matched.TrustStatus {
	case "trusted", "managed":
		return *matched.Key, *matched.CurrentHash, false, false, nil
	case "untrusted", "modified":
		return *matched.Key, *matched.CurrentHash, true, false, nil
	default:
		return "", "", false, false, errors.New("managed Codex instruction hook has unknown trust status")
	}
}

func sameNativeCodexHookSource(actual, expected string) bool {
	if filepath.Clean(actual) == filepath.Clean(expected) {
		return true
	}
	if actualResolved, err := filepath.EvalSymlinks(actual); err == nil {
		if expectedResolved, err := filepath.EvalSymlinks(expected); err == nil && filepath.Clean(actualResolved) == filepath.Clean(expectedResolved) {
			return true
		}
	}
	return false
}

func inspectNativeCodexHooks(ctx context.Context, cwd, home string, expected map[string]string) (NativeReport, error) {
	report := NativeReport{State: "unknown", Hooks: []NativeHook{}}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return report, err
	}
	home, err = config.CanonicalAccountDirectory(home)
	if err != nil {
		return report, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := childprocess.CommandContext(ctx, "codex", "app-server")
	cmd.Dir = cwd
	cmd.Env = codexprovider.EnvForHome(os.Environ(), home)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return report, err
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return report, err
	}
	if err := cmd.Start(); err != nil {
		return report, fmt.Errorf("start Codex native hook inspection: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(time.Second):
			cancel()
			<-done
		}
	}()
	reader := bufio.NewScanner(stdout)
	reader.Buffer(make([]byte, 64*1024), 4*1024*1024)
	writer := json.NewEncoder(stdin)
	request := func(id int, method string, params any) (json.RawMessage, error) {
		if err := writer.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, err
		}
		for reader.Scan() {
			var response struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(reader.Bytes(), &response); err != nil {
				return nil, fmt.Errorf("decode %s response: %w", method, err)
			}
			if response.ID == nil || *response.ID != id {
				continue
			}
			if len(response.Error) != 0 && string(response.Error) != "null" {
				var rpcError struct {
					Code int `json:"code"`
				}
				_ = json.Unmarshal(response.Error, &rpcError)
				return nil, fmt.Errorf("%s unavailable (RPC error %d)", method, rpcError.Code)
			}
			if len(response.Result) == 0 || string(response.Result) == "null" {
				return nil, fmt.Errorf("%s returned no result", method)
			}
			return response.Result, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := reader.Err(); err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}
	if _, err = request(1, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "quota-cli", "version": "0.1.0"},
		"capabilities": map[string]bool{"experimentalApi": true},
	}); err != nil {
		return report, fmt.Errorf("initialize native hook inspection: %w", err)
	}
	if err := writer.Encode(map[string]any{"method": "initialized"}); err != nil {
		return report, err
	}
	raw, err := request(2, "hooks/list", map[string]any{"cwds": []string{cwd}})
	if err != nil {
		return report, err
	}
	if err := parseNativeHooks(raw, cwd, expected, &report); err != nil {
		return report, err
	}
	return report, nil
}

func codexQuotedKeySegment(key string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range key {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func parseNativeHooks(raw json.RawMessage, cwd string, expected map[string]string, report *NativeReport) error {
	var result struct {
		Data []struct {
			Cwd      string                `json:"cwd"`
			Hooks    *[]nativeHookMetadata `json:"hooks"`
			Warnings *[]string             `json:"warnings"`
			Errors   *[]struct {
				Message string `json:"message"`
				Path    string `json:"path"`
			} `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("hooks/list: %w", err)
	}
	if len(result.Data) != 1 || filepath.Clean(result.Data[0].Cwd) != filepath.Clean(cwd) {
		return errors.New("hooks/list did not return the requested working directory")
	}
	entry := result.Data[0]
	if entry.Hooks == nil || entry.Errors == nil || entry.Warnings == nil {
		return errors.New("hooks/list omitted hooks or diagnostics")
	}
	for _, metadata := range *entry.Hooks {
		if metadata.Key == nil || *metadata.Key == "" || metadata.CurrentHash == nil || *metadata.CurrentHash == "" || metadata.IsManaged == nil {
			return errors.New("hooks/list omitted hook identity or management metadata")
		}
		if len(metadata.AsyncValue) != 0 {
			var async *bool
			if err := json.Unmarshal(metadata.AsyncValue, &async); err != nil || async == nil {
				return errors.New("hooks/list returned invalid async metadata")
			}
			metadata.NativeHook.Async = *async
		}
		report.hookMetadata = append(report.hookMetadata, metadata)
		report.Hooks = append(report.Hooks, metadata.NativeHook)
	}
	report.Issues = append(report.Issues, (*entry.Warnings)...)
	for _, issue := range *entry.Errors {
		report.Issues = append(report.Issues, fmt.Sprintf("hooks/list %s: %s", issue.Path, issue.Message))
	}
	report.State = "configured"
	counts := make(map[string]int)
	for i := range report.Hooks {
		hook := &report.Hooks[i]
		hook.State = "unknown"
		hook.Matched = hook.Command != "" && expected[hook.Event] == hook.Command && hook.HandlerType == "command"
		if hook.Enabled == nil || hook.Event == "" || hook.Source == "" || hook.SourcePath == "" {
			return errors.New("hooks/list returned incomplete hook metadata")
		}
		switch hook.HandlerType {
		case "command", "mcpTool", "prompt", "agent":
		default:
			return errors.New("hooks/list returned an unknown hook handler type")
		}
		switch hook.TrustStatus {
		case "trusted", "managed":
			hook.State = "configured"
		case "untrusted", "modified":
			hook.State = "needs-trust"
		default:
			return errors.New("hooks/list returned an unknown hook trust status")
		}
		if !*hook.Enabled {
			hook.State = "blocked"
		}
		if !hook.Matched {
			if hook.HandlerType == "command" && suspiciousInstructionCommand(hook.Command) {
				hook.State = "blocked"
				report.State = "blocked"
				report.Issues = append(report.Issues, fmt.Sprintf("%s: unexpected instruction hook from %s at %s; ownership or duplicate delivery requires review", hook.Event, hook.Source, hook.SourcePath))
			}
			hook.Command = ""
			continue
		}
		counts[hook.Event]++
		if hook.Async || (hook.Matcher != nil && *hook.Matcher != "" && *hook.Matcher != "*") {
			hook.State = "blocked"
			report.Issues = append(report.Issues, hook.Event+" instruction hook is asynchronous or restricts event matching")
		}
		if hook.State == "blocked" || (hook.State == "needs-trust" && report.State == "configured") {
			report.State = hook.State
		}
	}
	events := make([]string, 0, len(expected))
	for event := range expected {
		events = append(events, event)
	}
	sort.Strings(events)
	for _, event := range events {
		if counts[event] != 1 {
			report.State = "blocked"
			report.Issues = append(report.Issues, fmt.Sprintf("%s: expected exactly one instruction hook, found %d", event, counts[event]))
		}
	}
	if len(*entry.Errors) != 0 {
		return errors.New("hooks/list reported errors; discovery is incomplete")
	}
	return nil
}
