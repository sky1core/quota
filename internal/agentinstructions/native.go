package agentinstructions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/sky1core/quota/internal/childprocess"
)

type NativeReport struct {
	config                       map[string]json.RawMessage
	layers                       []nativeConfigLayerData
	hookMetadata                 []nativeHookMetadata
	State                        string              `json:"state"`
	Hooks                        []NativeHook        `json:"hooks"`
	Issues                       []string            `json:"issues,omitempty"`
	ConfigLayers                 []NativeConfigLayer `json:"configLayers"`
	ProjectDocMaxBytes           *int64              `json:"projectDocMaxBytes"`
	ProjectDocFallbackFilenames  []string            `json:"projectDocFallbackFilenames"`
	HooksEnabled                 *bool               `json:"hooksEnabled"`
	ProjectRootMarkers           []string            `json:"projectRootMarkers"`
	ProjectRootMarkersConfigured bool                `json:"projectRootMarkersConfigured"`
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

type NativeConfigLayer struct {
	Name struct {
		Type           string  `json:"type"`
		File           string  `json:"file,omitempty"`
		DotCodexFolder string  `json:"dotCodexFolder,omitempty"`
		Profile        *string `json:"profile,omitempty"`
	} `json:"name"`
	DisabledReason *string `json:"disabledReason,omitempty"`
}

type nativeHookMetadata struct {
	NativeHook
	Key         *string         `json:"key"`
	CurrentHash *string         `json:"currentHash"`
	IsManaged   *bool           `json:"isManaged"`
	AsyncValue  json.RawMessage `json:"async"`
}

type nativeConfigLayerData struct {
	NativeConfigLayer
	Config map[string]json.RawMessage `json:"config"`
}

func InspectNativeCodex(ctx context.Context, cwd string, expected map[string]string) (NativeReport, error) {
	return inspectNativeCodex(ctx, cwd, expected, true)
}

func InspectNativeCodexConfig(ctx context.Context, cwd string) (NativeReport, error) {
	return inspectNativeCodex(ctx, cwd, nil, false)
}

func inspectNativeCodex(ctx context.Context, cwd string, expected map[string]string, inspectHooks bool) (NativeReport, error) {
	report := NativeReport{State: "unknown", Hooks: []NativeHook{}, ConfigLayers: []NativeConfigLayer{}}
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		return report, err
	}
	for event, command := range expected {
		if (event != "sessionStart" && event != "subagentStart") || command == "" {
			return report, fmt.Errorf("invalid expected instruction hook event or command: %s", event)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := childprocess.CommandContext(ctx, "codex", "app-server")
	cmd.Dir = cwd
	cmd.Env = os.Environ()
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
		return report, fmt.Errorf("start Codex native inspection: %w", err)
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
		return report, fmt.Errorf("initialize native inspection: %w", err)
	}
	if err := writer.Encode(map[string]any{"method": "initialized"}); err != nil {
		return report, err
	}
	config, configErr := request(2, "config/read", map[string]any{"cwd": cwd, "includeLayers": true})
	if configErr == nil {
		configErr = parseNativeConfig(config, &report)
	}
	var hooksErr error
	if inspectHooks {
		var hooks json.RawMessage
		hooks, hooksErr = request(3, "hooks/list", map[string]any{"cwds": []string{cwd}})
		if hooksErr == nil {
			hooksErr = parseNativeHooks(hooks, cwd, expected, &report)
		}
	} else {
		report.State = "configured"
	}
	for _, err := range []error{configErr, hooksErr} {
		if err != nil {
			report.Issues = append(report.Issues, err.Error())
		}
	}
	if err := errors.Join(configErr, hooksErr); err != nil {
		report.State = "unknown"
		return report, err
	}
	if len(expected) > 0 && report.HooksEnabled != nil && !*report.HooksEnabled {
		report.State = "blocked"
		report.Issues = append(report.Issues, "effective features.hooks is false")
	}
	if report.ProjectDocMaxBytes != nil && *report.ProjectDocMaxBytes <= 0 {
		report.State = "blocked"
		report.Issues = append(report.Issues, "effective project_doc_max_bytes must be positive")
	}
	if len(report.ProjectDocFallbackFilenames) != 0 {
		report.State = "blocked"
		report.Issues = append(report.Issues, "effective project_doc_fallback_filenames changes project document discovery")
	}
	if report.ProjectRootMarkersConfigured {
		report.State = "blocked"
		report.Issues = append(report.Issues, "effective config explicitly sets project_root_markers and changes project document discovery")
	}
	return report, nil
}

func parseNativeConfig(raw json.RawMessage, report *NativeReport) error {
	var result struct {
		Config map[string]json.RawMessage `json:"config"`
		Layers *[]nativeConfigLayerData   `json:"layers"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("config/read: %w", err)
	}
	if result.Config == nil || result.Layers == nil {
		return errors.New("config/read omitted effective config or layers")
	}
	report.config, report.layers = result.Config, *result.Layers
	for _, layer := range *result.Layers {
		report.ConfigLayers = append(report.ConfigLayers, layer.NativeConfigLayer)
		if layer.Name.Type == "" {
			return errors.New("config/read returned an unidentified config layer")
		}
		if layer.Config == nil {
			return errors.New("config/read omitted layer config")
		}
		if layer.DisabledReason != nil && *layer.DisabledReason != "" {
			report.Issues = append(report.Issues, fmt.Sprintf("config layer %s disabled: %s", layer.Name.Type, *layer.DisabledReason))
			continue
		}
		if raw, present := layer.Config["project_root_markers"]; present && string(raw) != "null" {
			var markers []string
			if err := json.Unmarshal(raw, &markers); err != nil {
				return errors.New("config/read returned invalid layer project_root_markers")
			}
			report.ProjectRootMarkersConfigured = true
		}
	}
	if raw, present := result.Config["project_root_markers"]; !present {
		return errors.New("config/read omitted effective project_root_markers")
	} else if err := json.Unmarshal(raw, &report.ProjectRootMarkers); err != nil {
		return errors.New("config/read returned invalid project_root_markers")
	}
	if err := json.Unmarshal(result.Config["project_doc_max_bytes"], &report.ProjectDocMaxBytes); err != nil || report.ProjectDocMaxBytes == nil || *report.ProjectDocMaxBytes < 0 {
		return errors.New("config/read did not report a valid project_doc_max_bytes")
	}
	if raw, ok := result.Config["project_doc_fallback_filenames"]; !ok || string(raw) == "null" {
		return errors.New("config/read omitted project_doc_fallback_filenames")
	} else if err := json.Unmarshal(raw, &report.ProjectDocFallbackFilenames); err != nil {
		return errors.New("config/read returned invalid project_doc_fallback_filenames")
	}
	if raw, ok := result.Config["features"]; ok && string(raw) != "null" {
		var features map[string]json.RawMessage
		if err := json.Unmarshal(raw, &features); err != nil {
			return errors.New("config/read returned invalid features")
		}
		if raw, ok := features["hooks"]; ok {
			if err := json.Unmarshal(raw, &report.HooksEnabled); err != nil || report.HooksEnabled == nil {
				return errors.New("config/read returned invalid features.hooks")
			}
		}
	}
	return nil
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
