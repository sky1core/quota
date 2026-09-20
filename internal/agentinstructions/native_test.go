package agentinstructions

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func syntheticNativeHook() NativeHook {
	enabled := true
	return NativeHook{Event: "sessionStart", Command: "example-hook", HandlerType: "command", Source: "user", SourcePath: "/example/hooks.json", Enabled: &enabled, TrustStatus: "trusted"}
}

func syntheticNativeHooks(t *testing.T, hooks []NativeHook) json.RawMessage {
	t.Helper()
	metadata := make([]map[string]any, 0, len(hooks))
	for _, hook := range hooks {
		raw, err := json.Marshal(hook)
		if err != nil {
			t.Fatal(err)
		}
		var entry map[string]any
		if err := json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		entry["key"], entry["currentHash"], entry["isManaged"] = "example-key", "example-hash", false
		metadata = append(metadata, entry)
	}
	raw, err := json.Marshal(map[string]any{"data": []any{map[string]any{"cwd": "/example/repo", "hooks": metadata, "errors": []any{}, "warnings": []string{}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestNativeHookMetadataBoundaries(t *testing.T) {
	for _, tc := range []struct {
		key           string
		value         any
		remove, valid bool
	}{
		{key: "key", remove: true}, {key: "currentHash", remove: true},
		{key: "isManaged", remove: true}, {key: "enabled", value: nil},
		{key: "async", value: nil}, {key: "async", value: "false"},
		{key: "async", remove: true, valid: true},
		{key: "handlerType", value: "future-handler"},
		{key: "futureOptionalField", value: true, valid: true},
	} {
		raw := syntheticNativeHooks(t, []NativeHook{syntheticNativeHook()})
		var decoded map[string]any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		hook := decoded["data"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
		if tc.remove {
			delete(hook, tc.key)
		} else {
			hook[tc.key] = tc.value
		}
		raw, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		err = parseNativeHooks(raw, "/example/repo", map[string]string{"sessionStart": "example-hook"}, &NativeReport{})
		if (err == nil) != tc.valid {
			t.Fatalf("key=%s remove=%t valid=%t err=%v", tc.key, tc.remove, tc.valid, err)
		}
	}
}

func TestNativeHookReadiness(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*NativeHook)
		state  string
	}{
		{"trusted", func(*NativeHook) {}, "configured"},
		{"managed", func(h *NativeHook) { h.TrustStatus = "managed" }, "configured"},
		{"untrusted", func(h *NativeHook) { h.TrustStatus = "untrusted" }, "needs-trust"},
		{"modified", func(h *NativeHook) { h.TrustStatus = "modified" }, "needs-trust"},
		{"disabled", func(h *NativeHook) { disabled := false; h.Enabled = &disabled }, "blocked"},
		{"async", func(h *NativeHook) { h.Async = true }, "blocked"},
		{"restricted matcher", func(h *NativeHook) { matcher := "startup"; h.Matcher = &matcher }, "blocked"},
		{"substring is not ownership", func(h *NativeHook) { h.Command += " --other" }, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := syntheticNativeHook()
			tc.mutate(&hook)
			report := NativeReport{}
			if err := parseNativeHooks(syntheticNativeHooks(t, []NativeHook{hook}), "/example/repo", map[string]string{"sessionStart": "example-hook"}, &report); err != nil {
				t.Fatal(err)
			}
			if report.State != tc.state {
				t.Fatalf("state = %s, want %s", report.State, tc.state)
			}
		})
	}
}

func TestNativeMergedSourcesRejectDuplicateDelivery(t *testing.T) {
	first := syntheticNativeHook()
	second := syntheticNativeHook()
	second.Source = "project"
	second.SourcePath = "/example/repo/.codex/config.toml"
	report := NativeReport{}
	err := parseNativeHooks(syntheticNativeHooks(t, []NativeHook{first, second}), "/example/repo", map[string]string{"sessionStart": "example-hook"}, &report)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || len(report.Hooks) != 2 {
		t.Fatalf("report = %+v", report)
	}
}

func TestNativeMalformedDiscoveryCannotSucceed(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"data":[]}`,
		`{"data":[{"cwd":"/different/repo","hooks":[],"errors":[],"warnings":[]}]}`,
		`{"data":[{"cwd":"/example/repo","hooks":[]}]}`,
	} {
		report := NativeReport{}
		if err := parseNativeHooks([]byte(raw), "/example/repo", map[string]string{"sessionStart": "example-hook"}, &report); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	hook := syntheticNativeHook()
	hook.Enabled = nil
	if err := parseNativeHooks(syntheticNativeHooks(t, []NativeHook{hook}), "/example/repo", map[string]string{"sessionStart": "example-hook"}, &NativeReport{}); err == nil {
		t.Fatal("accepted missing enabled")
	}
	hook = syntheticNativeHook()
	hook.TrustStatus = "future-status"
	if err := parseNativeHooks(syntheticNativeHooks(t, []NativeHook{hook}), "/example/repo", map[string]string{"sessionStart": "example-hook"}, &NativeReport{}); err == nil {
		t.Fatal("accepted unknown trust status")
	}
}

func TestNativeConfigRequiresEffectiveBudgetAndLayers(t *testing.T) {
	for _, raw := range []string{`{}`, `{"config":{},"layers":[]}`, `{"config":{"project_doc_max_bytes":null},"layers":[]}`, `{"config":{"project_doc_max_bytes":-1},"layers":[]}`, `{"config":{"project_doc_max_bytes":42},"layers":null}`} {
		if err := parseNativeConfig([]byte(raw), &NativeReport{}); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	report := NativeReport{}
	err := parseNativeConfig([]byte(`{"config":{"project_root_markers":[".git"],"project_doc_max_bytes":42,"project_doc_fallback_filenames":["EXAMPLE.md"],"features":{"hooks":false}},"layers":[{"name":{"type":"project","dotCodexFolder":"/example/repo/.codex"},"disabledReason":"project is untrusted","config":{}}]}`), &report)
	if err != nil {
		t.Fatal(err)
	}
	if *report.ProjectDocMaxBytes != 42 || report.HooksEnabled == nil || *report.HooksEnabled || len(report.ConfigLayers) != 1 || len(report.Issues) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestNativeCodexTrustIssueUsesEffectiveProjects(t *testing.T) {
	report := NativeReport{}
	raw := []byte(`{"config":{"project_root_markers":[],"project_doc_max_bytes":4096,"project_doc_fallback_filenames":[],"projects":{"/example/repo":{"trust_level":"trusted"},"/example/repo/sub":{"trust_level":"untrusted"}}},"layers":[]}`)
	if err := parseNativeConfig(raw, &report); err != nil {
		t.Fatal(err)
	}
	if issue := nativeCodexTrustIssue(report, "/example/repo/sub"); !strings.Contains(issue, "explicitly untrusted") {
		t.Fatalf("issue = %q", issue)
	}
	if issue := nativeCodexTrustIssue(report, "/example/repo"); issue != "" {
		t.Fatalf("trusted parent blocked: %q", issue)
	}
	report = NativeReport{}
	raw = []byte(`{"config":{"project_root_markers":[],"project_doc_max_bytes":4096,"project_doc_fallback_filenames":[],"projects":{"/example/repo":{"trust_level":"untrusted"}}},"layers":[]}`)
	if err := parseNativeConfig(raw, &report); err != nil {
		t.Fatal(err)
	}
	if issue := nativeCodexTrustIssue(report, "/example/repo/sub"); !strings.Contains(issue, "/example/repo is explicitly untrusted") {
		t.Fatalf("parent issue = %q", issue)
	}
	report = NativeReport{}
	raw = []byte(`{"config":{"project_root_markers":[],"project_doc_max_bytes":4096,"project_doc_fallback_filenames":[],"projects":{"/example/repo":{"trust_level":"untrusted"},"/example/repo/sub":{"trust_level":"trusted"}}},"layers":[]}`)
	if err := parseNativeConfig(raw, &report); err != nil {
		t.Fatal(err)
	}
	if issue := nativeCodexTrustIssue(report, "/example/repo/sub/nested"); issue != "" {
		t.Fatalf("trusted child blocked: %q", issue)
	}
}

func TestNativeConfigMissingDiscoveryFields(t *testing.T) {
	for _, config := range []string{
		`{"project_root_markers":[".git"],"project_doc_max_bytes":42}`,
		`{"project_root_markers":[".git"],"project_doc_max_bytes":42,"project_doc_fallback_filenames":null}`,
		`{"project_root_markers":[".git"],"project_doc_max_bytes":42,"project_doc_fallback_filenames":[],"features":{"hooks":null}}`,
		`{"project_doc_max_bytes":42,"project_doc_fallback_filenames":[]}`,
	} {
		if err := parseNativeConfig([]byte(`{"config":`+config+`,"layers":[]}`), &NativeReport{}); err == nil {
			t.Fatalf("accepted %s", config)
		}
	}
}

func TestNativeReportDoesNotExposeUnrelatedCommands(t *testing.T) {
	owned, unrelated := syntheticNativeHook(), syntheticNativeHook()
	unrelated.Command = "unrelated-sensitive-command"
	report := NativeReport{}
	if err := parseNativeHooks(syntheticNativeHooks(t, []NativeHook{owned, unrelated}), "/example/repo", map[string]string{"sessionStart": "example-hook"}, &report); err != nil {
		t.Fatal(err)
	}
	if report.State != "configured" || len(report.Hooks) != 2 || report.Hooks[0].Command != "example-hook" || report.Hooks[1].Command != "" || report.Hooks[1].Source == "" {
		t.Fatalf("report = %+v", report)
	}
}

func TestNativeUnexpectedInstructionHooks(t *testing.T) {
	for _, tc := range []struct{ name, command, source, state string }{
		{"project legacy script", `sh "$HOME/.local/bin/agents-overlay-context" json SessionStart AGENTS.md - . codex-session`, "project", "blocked"},
		{"plugin alternative executable", `/alternate/quota-cli agent instructions _hook --agent=codex --event=SessionStart`, "plugin", "blocked"},
		{"echo quoted words", `echo "quota-cli agent instructions _hook"`, "project", "configured"},
		{"echo argument words", `echo agent instructions _hook`, "project", "configured"},
		{"unrelated command", `example-tool --label instructions`, "plugin", "configured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session, subagent, extra := syntheticNativeHook(), syntheticNativeHook(), syntheticNativeHook()
			subagent.Event, subagent.Command = "subagentStart", "example-subagent-hook"
			extra.Command, extra.Source = tc.command, tc.source
			extra.SourcePath = "/example/repo/.codex/hooks.json"
			report := NativeReport{}
			expected := map[string]string{"sessionStart": session.Command, "subagentStart": subagent.Command}
			if err := parseNativeHooks(syntheticNativeHooks(t, []NativeHook{session, subagent, extra}), "/example/repo", expected, &report); err != nil {
				t.Fatal(err)
			}
			if report.State != tc.state || report.Hooks[2].Command != "" {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestNativeInformationalWarningsPreserveInspection(t *testing.T) {
	raw := syntheticNativeHooks(t, []NativeHook{syntheticNativeHook()})
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["data"].([]any)[0].(map[string]any)["warnings"] = []string{"example informational notice"}
	raw, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	report := NativeReport{}
	if err := parseNativeHooks(raw, "/example/repo", map[string]string{"sessionStart": "example-hook"}, &report); err != nil {
		t.Fatal(err)
	}
	if report.State != "configured" || len(report.Issues) != 1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestNativeMixedRepresentationsWithoutDuplicateInstructions(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex CLI is not installed")
	}
	root := t.TempDir()
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	codexHome := filepath.Join(home, ".codex")
	for _, dir := range []string{repo, codexHome} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	hooks := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"example-session-hook"}]}],"SubagentStart":[{"hooks":[{"type":"command","command":"example-subagent-hook"}]}]}}`
	config := "[[hooks.PreToolUse]]\n[[hooks.PreToolUse.hooks]]\ntype = 'command'\ncommand = 'example-policy-tool'\n"
	if err := os.WriteFile(filepath.Join(codexHome, "hooks.json"), []byte(hooks), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"sessionStart": "example-session-hook", "subagentStart": "example-subagent-hook"}
	report, err := InspectNativeCodexForHome(context.Background(), repo, codexHome, expected)
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "needs-trust" || len(report.Hooks) != 3 || len(report.Issues) == 0 {
		t.Fatalf("report = %+v", report)
	}
	matched, policies := 0, 0
	for _, hook := range report.Hooks {
		if hook.Matched {
			matched++
		}
		if hook.Event == "preToolUse" && !hook.Matched && hook.Command == "" {
			policies++
		}
	}
	if matched != 2 || policies != 1 {
		t.Fatalf("matched=%d policies=%d", matched, policies)
	}
	privatePolicy := false
	for _, metadata := range report.hookMetadata {
		if metadata.Event == "preToolUse" && metadata.Command == "example-policy-tool" {
			privatePolicy = true
		}
	}
	raw, err := json.Marshal(report)
	if err != nil || !privatePolicy || strings.Contains(string(raw), "example-policy-tool") {
		t.Fatal("native metadata did not retain the unrelated command privately")
	}
	for _, setting := range []struct{ key, value string }{
		{"project_doc_max_bytes", "0"},
		{"project_doc_fallback_filenames", "['EXAMPLE.md']"},
	} {
		if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(setting.key+" = "+setting.value+"\n"+config), 0600); err != nil {
			t.Fatal(err)
		}
		report, err = InspectNativeCodexForHome(context.Background(), repo, codexHome, expected)
		if err != nil {
			t.Fatal(err)
		}
		if report.State != "blocked" || !strings.Contains(strings.Join(report.Issues, " "), "effective "+setting.key) {
			t.Fatalf("report = %+v", report)
		}
	}
}

func TestNativeRootMarkerLayers(t *testing.T) {
	for _, tc := range []struct {
		name, effective, layer, source string
		disabled, configured, invalid  bool
	}{
		{name: "runtime default", effective: `[".git"]`, layer: `{}`, source: "user"},
		{name: "null layer value", effective: `null`, layer: `{"project_root_markers":null}`, source: "user"},
		{name: "explicit empty", effective: `[]`, layer: `{"project_root_markers":[]}`, source: "user", configured: true},
		{name: "explicit default", effective: `[".git"]`, layer: `{"project_root_markers":[".git"]}`, source: "user", configured: true},
		{name: "system override", effective: `[".example-root"]`, layer: `{"project_root_markers":[".example-root"]}`, source: "system", configured: true},
		{name: "managed override", effective: `[]`, layer: `{"project_root_markers":[]}`, source: "enterpriseManaged", configured: true},
		{name: "disabled project", effective: `[".git"]`, layer: `{"project_root_markers":[]}`, source: "project", disabled: true},
		{name: "invalid marker type", effective: `[".git"]`, layer: `{"project_root_markers":false}`, source: "user", invalid: true},
		{name: "missing layer config", effective: `[".git"]`, layer: `null`, source: "user", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disabled := "null"
			if tc.disabled {
				disabled = `"project not trusted"`
			}
			raw := `{"config":{"project_doc_max_bytes":42,"project_doc_fallback_filenames":[],"project_root_markers":` + tc.effective + `},"layers":[{"name":{"type":"` + tc.source + `"},"config":` + tc.layer + `,"disabledReason":` + disabled + `}]}`
			report := NativeReport{}
			err := parseNativeConfig([]byte(raw), &report)
			if (err != nil) != tc.invalid || report.ProjectRootMarkersConfigured != tc.configured {
				t.Fatalf("configured=%t err=%v", report.ProjectRootMarkersConfigured, err)
			}
		})
	}
}

func TestNativeCodexIsolatedDiscovery(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex CLI is not installed")
	}
	root := t.TempDir()
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	codexHome := filepath.Join(home, ".codex")
	for _, dir := range []string{repo, codexHome} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	marker := filepath.Join(root, "hook-must-not-run")
	command := "touch " + marker
	hooks, err := json.Marshal(map[string]any{"hooks": map[string]any{"SessionStart": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "hooks.json"), hooks, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("project_doc_max_bytes = 12345\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := InspectNativeCodexForHome(context.Background(), repo, codexHome, map[string]string{"sessionStart": command})
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "needs-trust" || len(report.Hooks) != 1 || !report.Hooks[0].Matched || report.ProjectDocMaxBytes == nil || *report.ProjectDocMaxBytes != 12345 {
		t.Fatalf("report = %+v", report)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("discovery executed an instruction hook")
	}
	if report.ProjectRootMarkersConfigured || len(report.ProjectRootMarkers) != 1 || report.ProjectRootMarkers[0] != ".git" {
		t.Fatalf("unexpected default root markers: %+v", report)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("project_doc_max_bytes = 12345\nproject_root_markers = []\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = InspectNativeCodexForHome(context.Background(), repo, codexHome, map[string]string{"sessionStart": command})
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || !report.ProjectRootMarkersConfigured || len(report.ProjectRootMarkers) != 0 {
		t.Fatalf("explicit root markers: %+v", report)
	}
	mergedConfig := "project_doc_max_bytes = 12345\n[[hooks.SessionStart]]\n[[hooks.SessionStart.hooks]]\ntype = 'command'\ncommand = " + strconv.Quote(command) + "\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(mergedConfig), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = InspectNativeCodexForHome(context.Background(), repo, codexHome, map[string]string{"sessionStart": command})
	if err != nil {
		t.Fatalf("merged discovery: %v; report: %+v", err, report)
	}
	if report.State != "blocked" || len(report.Hooks) != 2 {
		t.Fatalf("merged report = %+v", report)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("project_doc_max_bytes = 12345\n[features]\nhooks = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = InspectNativeCodexForHome(context.Background(), repo, codexHome, map[string]string{"sessionStart": command})
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "blocked" || !strings.Contains(strings.Join(report.Issues, " "), "features.hooks") {
		t.Fatalf("report = %+v", report)
	}
}

func TestNativeCodexTrustSyncApprovesManagedHook(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex CLI is not installed")
	}
	i := testInstallation(t)
	repo := t.TempDir()
	plan, err := i.Plan([]string{"codex"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := i.Apply(plan); err != nil {
		t.Fatal(err)
	}
	before, err := InspectNativeCodexForHome(context.Background(), repo, i.targets.CodexHome, i.ExpectedCodexHooks())
	if err != nil {
		t.Fatal(err)
	}
	if before.State != "needs-trust" {
		t.Fatalf("before sync = %+v", before)
	}
	synced, err := i.SyncCodexHookTrust(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if !synced.Changed || synced.Key == "" {
		t.Fatalf("sync result = %+v", synced)
	}
	after, err := InspectNativeCodexForHome(context.Background(), repo, i.targets.CodexHome, i.ExpectedCodexHooks())
	if err != nil {
		t.Fatal(err)
	}
	if after.State != "configured" || len(after.Hooks) != 1 || after.Hooks[0].TrustStatus != "trusted" {
		t.Fatalf("after sync = %+v", after)
	}
	again, err := i.SyncCodexHookTrust(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Fatalf("second sync rewrote trust: %+v", again)
	}
}

func TestCodexTrustTableQuotesHookKey(t *testing.T) {
	after, err := syncCodexTrustTOML(nil, `/tmp/a"b\c:session_start:1:0`, `hash"with\chars`)
	if err != nil {
		t.Fatal(err)
	}
	want := "[hooks.state.\"/tmp/a\\\"b\\\\c:session_start:1:0\"]\ntrusted_hash = \"hash\\\"with\\\\chars\"\n"
	if string(after) != want {
		t.Fatalf("trust TOML = %q, want %q", after, want)
	}
	root, err := parseInstallTOML(after)
	if err != nil {
		t.Fatal(err)
	}
	if !codexTrustStateMatches(root, `/tmp/a"b\c:session_start:1:0`, `hash"with\chars`) {
		t.Fatal("quoted trust key did not round-trip")
	}
}

func TestCodexTrustTOMLUpdatesExistingTableWithoutLosingFields(t *testing.T) {
	before := "[hooks.state.example]\nenabled = false\ntrusted_hash = 'old' # old hash\n"
	after, err := syncCodexTrustTOML([]byte(before), "example", "new")
	if err != nil {
		t.Fatal(err)
	}
	got := string(after)
	if !strings.Contains(got, "enabled = false") || strings.Contains(got, "trusted_hash = 'old'") || !strings.Contains(got, `trusted_hash = "new"`) || !strings.Contains(got, "# old hash") {
		t.Fatalf("trust table update = %q", got)
	}
	again, err := syncCodexTrustTOML(after, "example", "new")
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != got {
		t.Fatal("trust sync was not idempotent")
	}
}

func TestCodexTrustTOMLPreservesLongStringFollowingFields(t *testing.T) {
	before := "[hooks.state.example]\ntrusted_hash = \"\"\"x\"\"\"\"\nenabled = false\nother = \"x\"\n"
	after, err := syncCodexTrustTOML([]byte(before), "example", "new")
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseInstallTOML(after)
	if err != nil {
		t.Fatal(err)
	}
	entry := root["hooks"].(map[string]any)["state"].(map[string]any)["example"].(map[string]any)
	if entry["trusted_hash"] != "new" || entry["enabled"] != false || entry["other"] != "x" {
		t.Fatalf("trust update lost fields: %#v\n%s", entry, after)
	}
}

func TestCodexTrustTOMLUpdatesInlineState(t *testing.T) {
	for _, tc := range []struct {
		name, before string
	}{
		{"inline hooks root", `hooks = { state = { existing = { enabled = false } } } # keep root comment` + "\n"},
		{"inline hooks root dotted state", `hooks = { state.example = { trusted_hash = "old" } } # keep root comment` + "\n"},
		{"inline hooks root dotted hash", `hooks = { state.example.trusted_hash = "old" } # keep root comment` + "\n"},
		{"inline hooks root dotted field", `hooks = { state.example.enabled = true } # keep root comment` + "\n"},
		{"inline hooks table state", `[hooks]` + "\n" + `state = { existing = { trusted_hash = "old" } } # keep state comment` + "\n"},
		{"inline state entry", `[hooks.state]` + "\n" + `example = { enabled = false, trusted_hash = "old" } # keep entry comment` + "\n"},
		{"inline state dotted hash", `[hooks.state]` + "\n" + `example.trusted_hash = "old" # keep entry comment` + "\n"},
		{"inline state dotted field", `[hooks.state]` + "\n" + `example.enabled = true # keep entry comment` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after, err := syncCodexTrustTOML([]byte(tc.before), "example", "new")
			if err != nil {
				t.Fatal(err)
			}
			root, err := parseInstallTOML(after)
			if err != nil {
				t.Fatal(err)
			}
			if !codexTrustStateMatches(root, "example", "new") {
				t.Fatalf("trust state was not updated: %s", after)
			}
			if strings.Contains(tc.before, "# keep") && !strings.Contains(string(after), "# keep") {
				t.Fatalf("comment was not preserved: %s", after)
			}
		})
	}
}

func TestNativeCodexHookSourceMatchesSymlinkedTargetWithDifferentName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "codex-hooks-target.json")
	link := filepath.Join(dir, "hooks.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if !sameNativeCodexHookSource(link, target) {
		t.Fatal("symlinked hook source did not match resolved target")
	}
}

func TestCodexTrustTOMLRejectsInvalidState(t *testing.T) {
	for _, before := range []string{
		"[hooks]\nstate = []\n",
		"[hooks.state.example]\ntrusted_hash = 42\n",
	} {
		if _, err := syncCodexTrustTOML([]byte(before), "example", "new"); err == nil {
			t.Fatalf("invalid trust state accepted: %s", before)
		}
	}
}

func TestUpdateTOMLTrustCreatesConfigThroughManagedPath(t *testing.T) {
	i := testInstallation(t)
	i, err := i.resolveSelectedTargets([]string{"codex"})
	if err != nil {
		t.Fatal(err)
	}
	changed, err := i.updateTOMLTrust(i.targets.CodexConfig, "example", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("missing config.toml did not get created")
	}
	body, err := os.ReadFile(i.targets.CodexConfig)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); !strings.Contains(got, `[hooks.state."example"]`) || !strings.Contains(got, `trusted_hash = "hash"`) {
		t.Fatalf("config.toml = %q", got)
	}
	again, err := i.updateTOMLTrust(i.targets.CodexConfig, "example", "hash")
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("second trust sync rewrote config.toml")
	}
}

func TestCodexQuotedKeySegmentEscapesTOMLString(t *testing.T) {
	got := codexQuotedKeySegment(`/tmp/a"b\c:session_start:1:0`)
	want := `"/tmp/a\"b\\c:session_start:1:0"`
	if got != want {
		t.Fatalf("quoted key = %q, want %q", got, want)
	}
}

func TestNativeConfigInspectionIndependentOfHooks(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex CLI is not installed")
	}
	root := t.TempDir()
	home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
	codexHome := filepath.Join(home, ".codex")
	for _, path := range []string{codexHome, repo} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("project_doc_max_bytes = 12345\n[features]\nhooks = false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, hooks := range []string{
		`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"quota-cli agent instructions _hook --agent=codex --event=SessionStart"}]}]}}`,
		`{invalid`,
	} {
		if err := os.WriteFile(filepath.Join(codexHome, "hooks.json"), []byte(hooks), 0600); err != nil {
			t.Fatal(err)
		}
		report, err := InspectNativeCodexConfigForHome(context.Background(), repo, codexHome)
		if err != nil || report.State != "configured" || len(report.Hooks) != 0 || report.ProjectDocMaxBytes == nil || *report.ProjectDocMaxBytes != 12345 {
			t.Fatalf("config inspection depended on hook readiness: %+v %v", report, err)
		}
		if body, err := os.ReadFile(filepath.Join(codexHome, "hooks.json")); err != nil || string(body) != hooks {
			t.Fatalf("config inspection changed hooks: %q %v", body, err)
		}
	}
}
