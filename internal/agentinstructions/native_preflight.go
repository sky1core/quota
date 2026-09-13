package agentinstructions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/sky1core/quota/internal/agenthooks"
	"github.com/sky1core/quota/internal/overlayruntime"
)

var codexDocumentKeys = []string{"project_doc_max_bytes", "project_doc_fallback_filenames", "project_root_markers"}

type NativeCodexDiscoveryError struct {
	Directory string
	Err       error
}

func (e *NativeCodexDiscoveryError) Error() string {
	return fmt.Sprintf("Codex config preflight at %s: %v", e.Directory, e.Err)
}

func (e *NativeCodexDiscoveryError) Unwrap() error { return e.Err }

func PreflightNativeCodex(ctx context.Context, plan overlayruntime.CodexSetupPlan) (overlayruntime.ValidatedCodexSetup, error) {
	validated := overlayruntime.ValidatedCodexSetup{Plan: plan, Budgets: map[string]int64{}, ActiveHookFiles: map[string][]string{}}
	for _, document := range plan.Documents {
		report, err := InspectNativeCodex(ctx, document.Directory, nil)
		if err != nil {
			return validated, &NativeCodexDiscoveryError{Directory: document.Directory, Err: err}
		}
		var projectConfigs []string
		for _, layer := range report.ConfigLayers {
			if layer.Name.Type != "project" {
				continue
			}
			if layer.Name.DotCodexFolder == "" {
				return validated, &NativeCodexDiscoveryError{Directory: document.Directory, Err: fmt.Errorf("config/read omitted project config path")}
			}
			if layer.DisabledReason == nil || *layer.DisabledReason == "" {
				projectConfigs = append(projectConfigs, filepath.Join(layer.Name.DotCodexFolder, "config.toml"))
			}
		}
		paths, err := nativeCodexHookFiles(report, plan, projectConfigs)
		if err != nil {
			return validated, fmt.Errorf("Codex hook preflight at %s: %w", document.Directory, err)
		}
		validated.ActiveHookFiles[document.Directory] = paths
		if err := validated.Plan.TrackNativeHookFiles(paths); err != nil {
			return validated, err
		}
		if err := reconcilePlannedCodexConfig(&report, plan, document.ConfigPaths); err != nil {
			return validated, fmt.Errorf("Codex config preflight at %s: %w", document.Directory, err)
		}
		if err := plan.CheckNewCodexHookLayers(document.ConfigPaths); err != nil {
			return validated, err
		}
		if err := validateNativeCodexTrust(report, document.TrustPaths); err != nil {
			return validated, err
		}
		if report.ProjectRootMarkersConfigured {
			return validated, fmt.Errorf("%s: effective config explicitly sets project_root_markers and changes project document discovery", document.Directory)
		}
		if len(report.ProjectDocFallbackFilenames) != 0 {
			return validated, fmt.Errorf("%s: effective project_doc_fallback_filenames changes project document discovery", document.Directory)
		}
		if report.ProjectDocMaxBytes == nil || *report.ProjectDocMaxBytes <= 0 {
			return validated, fmt.Errorf("%s: effective Codex project_doc_max_bytes must be positive", document.Directory)
		}
		if document.Bytes > *report.ProjectDocMaxBytes {
			return validated, fmt.Errorf("%s will be %d bytes, over effective Codex project_doc_max_bytes %d at %s", document.Path, document.Bytes, *report.ProjectDocMaxBytes, document.Directory)
		}
		validated.Budgets[document.Directory] = *report.ProjectDocMaxBytes
	}
	return validated, validated.CheckConsistency(ctx)
}

func nativeCodexHookFiles(report NativeReport, plan overlayruntime.CodexSetupPlan, projectConfigs []string) ([]string, error) {
	paths, err := plan.ProjectHookFileCandidates(projectConfigs)
	if err != nil {
		return nil, err
	}
	projectFiles := map[string]bool{}
	for _, path := range paths {
		projectFiles[path] = true
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	for _, metadata := range report.hookMetadata {
		hook := metadata.NativeHook
		if hook.Source == "project" {
			if !projectFiles[hook.SourcePath] {
				return nil, fmt.Errorf("cannot reliably check native project hook origin %s against config/read layers", hook.SourcePath)
			}
			continue
		}
		if hook.HandlerType != "command" || !suspiciousInstructionCommand(hook.Command, executable) {
			continue
		}
		event := map[string]string{"sessionStart": "SessionStart", "subagentStart": "SubagentStart"}[hook.Event]
		removing, err := plan.RemovesOwnedCodexHook(hook.SourcePath)
		if err != nil {
			return nil, err
		}
		if !removing || !agenthooks.OwnsInstructionCommand(hook.Command, executable, "codex", event) || (metadata.IsManaged != nil && *metadata.IsManaged) {
			return nil, fmt.Errorf("%s: hooks.%s conflicts with native instruction delivery", hook.SourcePath, hook.Event)
		}
		paths = append(paths, hook.SourcePath)
	}
	return paths, nil
}

func reconcilePlannedCodexConfig(report *NativeReport, plan overlayruntime.CodexSetupPlan, candidates []string) error {
	layers := append([]nativeConfigLayerData(nil), report.layers...)
	covered := map[string]bool{}
	changed := map[string]bool{}
	for index, layer := range layers {
		path := layer.Name.File
		if layer.Name.Type == "project" {
			if layer.Name.DotCodexFolder == "" {
				return fmt.Errorf("config/read omitted project config path")
			}
			path = filepath.Join(layer.Name.DotCodexFolder, "config.toml")
		}
		if path == "" {
			continue
		}
		target, future, planned, err := plan.PlannedConfigFile(path)
		if err != nil {
			return err
		}
		if !planned {
			continue
		}
		covered[target] = true
		if layer.DisabledReason != nil && *layer.DisabledReason != "" {
			continue
		}
		current, err := overlayruntime.ReadCodexConfigFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if bytes.Equal(current, future) {
			continue
		}
		before, err := nativeConfigDocument(current)
		if err != nil {
			return fmt.Errorf("cannot reliably check current Codex config %s: %w", path, err)
		}
		after, err := nativeConfigDocument(future)
		if err != nil {
			return fmt.Errorf("cannot reliably check planned Codex config %s: %w", path, err)
		}
		var activeProfile *string
		if raw, ok := report.config["profile"]; ok {
			if err := json.Unmarshal(raw, &activeProfile); err != nil {
				return fmt.Errorf("config/read returned an invalid active profile")
			}
		}
		if layer.Name.Profile != nil || activeProfile != nil {
			return fmt.Errorf("cannot reliably check planned Codex config %s with an active profile", path)
		}
		replacement := make(map[string]json.RawMessage, len(layer.Config))
		for key, value := range layer.Config {
			replacement[key] = value
		}
		for _, key := range codexDocumentKeys {
			if !nativeConfigValueEqual(before[key], layer.Config[key]) {
				return fmt.Errorf("cannot reliably check planned Codex config %s: native layer %s differs from its file", path, key)
			}
			if !nativeConfigValueEqual(before[key], after[key]) {
				changed[key] = true
			}
			delete(replacement, key)
			if value, ok := after[key]; ok {
				replacement[key] = value
			}
			delete(before, key)
			delete(after, key)
		}
		delete(before, "hooks")
		delete(after, "hooks")
		if !reflect.DeepEqual(before, after) {
			return fmt.Errorf("cannot reliably check planned Codex config %s: settings outside document configuration also change", path)
		}
		layers[index].Config = replacement
	}
	for _, path := range candidates {
		target, future, planned, err := plan.PlannedConfigFile(path)
		if err != nil {
			return err
		}
		if !planned || covered[target] {
			continue
		}
		current, err := overlayruntime.ReadCodexConfigFile(path)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if !bytes.Equal(current, future) {
			return fmt.Errorf("cannot reliably check planned Codex config %s: config/read did not identify its layer", path)
		}
	}
	for key := range changed {
		current, present := nativeLayerDocumentValue(report.layers, key)
		if present && !nativeConfigValueEqual(current, report.config[key]) {
			return fmt.Errorf("cannot reliably check planned %s against effective Codex config", key)
		}
		future, present := nativeLayerDocumentValue(layers, key)
		if !present {
			return fmt.Errorf("cannot reliably check runtime default for planned %s", key)
		}
		report.config[key] = future
	}
	raw, err := json.Marshal(map[string]any{"config": report.config, "layers": layers})
	if err != nil {
		return err
	}
	var plannedReport NativeReport
	if err := parseNativeConfig(raw, &plannedReport); err != nil {
		return err
	}
	*report = plannedReport
	return nil
}

func nativeConfigDocument(data []byte) (map[string]json.RawMessage, error) {
	var parsed map[string]any
	if _, err := toml.Decode(string(data), &parsed); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	result := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func nativeConfigValueEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	decode := func(raw json.RawMessage) any {
		var value any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		_ = decoder.Decode(&value)
		return value
	}
	return reflect.DeepEqual(decode(left), decode(right))
}

func nativeLayerDocumentValue(layers []nativeConfigLayerData, key string) (json.RawMessage, bool) {
	for _, layer := range layers {
		if layer.DisabledReason != nil && *layer.DisabledReason != "" {
			continue
		}
		if value, ok := layer.Config[key]; ok {
			return value, true
		}
	}
	return nil, false
}

func validateNativeCodexTrust(report NativeReport, paths []string) error {
	raw, ok := report.config["projects"]
	if !ok || string(raw) == "null" {
		return nil
	}
	var projects map[string]struct {
		TrustLevel string `json:"trust_level"`
	}
	if err := json.Unmarshal(raw, &projects); err != nil {
		return fmt.Errorf("config/read returned invalid project trust: %w", err)
	}
	untrusted := map[string]bool{}
	for key, project := range projects {
		if project.TrustLevel != "untrusted" {
			continue
		}
		untrusted[key] = true
	}
	for _, path := range paths {
		candidates := []string{filepath.Clean(path)}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			candidates = append(candidates, resolved)
		}
		for _, candidate := range candidates {
			if untrusted[candidate] {
				return fmt.Errorf("%s is explicitly untrusted in effective Codex config", candidate)
			}
		}
	}
	return nil
}
