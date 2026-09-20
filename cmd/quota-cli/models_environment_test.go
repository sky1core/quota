package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sky1core/quota/internal/config"
	"github.com/sky1core/quota/internal/modelcatalog"
)

func TestModelDiscoveryEnvironmentIndependence(t *testing.T) {
	wantIDs := []string{"model-alpha", "model-beta"}
	for _, p := range modelProviders() {
		t.Run(p.name, func(t *testing.T) {
			for _, kind := range []string{"default", "extra"} {
				t.Run(kind, func(t *testing.T) {
					home := t.TempDir()
					binDir := filepath.Join(home, "bin")
					if err := os.MkdirAll(binDir, 0o755); err != nil {
						t.Fatal(err)
					}
					writeModelDiscoveryCLIs(t, binDir)
					runsPath := filepath.Join(home, "discovery-runs")
					recordConfigured := filepath.Join(home, "child-env-configured")
					recordClean := filepath.Join(home, "child-env-clean")

					t.Setenv("HOME", home)
					t.Setenv("PATH", binDir)
					t.Setenv("QUOTA_MODEL_RUNS", runsPath)
					t.Setenv("QUOTA_TEST_GENERIC", "keepme")
					t.Setenv("NO_PROXY", "localhost")

					var cfg config.Config
					account := ""
					if kind == "extra" {
						cfg = p.extraField(t.TempDir())
						account = p.extraKey
					}
					_, dir := targetForAccount(t, cfg, p.name, account)
					modelCacheDir := filepath.Join(filepath.Dir(config.Path()), "model-cache")

					wantAccountPresent := !(p.name == "claude" && kind == "default")

					load := func(force bool) modelcatalog.Snapshot {
						ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						snap, err := loadAccountModels(ctx, p.name, dir, force)
						if err != nil {
							t.Fatalf("loadAccountModels(force=%v): %v", force, err)
						}
						return snap
					}

					setConfiguredCallerEnv(t, p, home)
					t.Setenv("QUOTA_MODEL_RECORD", recordConfigured)
					t.Setenv("HTTPS_PROXY", "http://proxy-a.invalid:8080")
					r1 := load(false)
					if r1.SchemaVersion != 2 {
						t.Fatalf("cold schemaVersion=%d, want 2", r1.SchemaVersion)
					}
					if got := snapshotModelIDs(r1); !reflect.DeepEqual(got, wantIDs) {
						t.Fatalf("cold models=%v, want %v", got, wantIDs)
					}
					if n := countLines(t, runsPath); n != 1 {
						t.Fatalf("cold discovery runs=%d, want 1", n)
					}
					configuredEnv := readEnvRecord(t, recordConfigured)
					assertGenericEnv(t, configuredEnv, home, binDir, "http://proxy-a.invalid:8080")
					assertAccountEnv(t, configuredEnv, p, dir, wantAccountPresent)
					assertStrippedEnv(t, configuredEnv, p)

					for _, key := range []string{p.envKey, p.secretKey, "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
						"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_FOUNDRY", "MAX_THINKING_TOKENS",
						"OPENAI_BASE_URL", "CODEX_SQLITE_HOME"} {
						t.Setenv(key, "")
						if err := os.Unsetenv(key); err != nil {
							t.Fatal(err)
						}
					}
					t.Setenv("HTTPS_PROXY", "http://proxy-b.invalid:8080")
					r2 := load(false)
					if n := countLines(t, runsPath); n != 1 {
						t.Fatalf("reuse triggered discovery: runs=%d, want 1", n)
					}
					if !r2.FetchedAt.Equal(r1.FetchedAt) {
						t.Fatal("reuse refetched instead of serving the on-disk cache")
					}
					if got := snapshotModelIDs(r2); !reflect.DeepEqual(got, wantIDs) {
						t.Fatalf("reuse models=%v, want %v", got, wantIDs)
					}
					if n := countCacheFiles(t, modelCacheDir); n != 1 {
						t.Fatalf("cache files=%d, want 1 (caller env must not fork the cache)", n)
					}

					t.Setenv("QUOTA_MODEL_RECORD", recordClean)
					r3 := load(true)
					if n := countLines(t, runsPath); n != 2 {
						t.Fatalf("force discovery runs=%d, want 2", n)
					}
					if got := snapshotModelIDs(r3); !reflect.DeepEqual(got, wantIDs) {
						t.Fatalf("force models=%v, want %v", got, wantIDs)
					}
					if n := countCacheFiles(t, modelCacheDir); n != 1 {
						t.Fatalf("force cache files=%d, want 1", n)
					}
					cleanEnv := readEnvRecord(t, recordClean)
					assertGenericEnv(t, cleanEnv, home, binDir, "http://proxy-b.invalid:8080")
					assertAccountEnv(t, cleanEnv, p, dir, wantAccountPresent)
					assertStrippedEnv(t, cleanEnv, p)

					writeLegacyCache(t, modelCacheDir)
					r4 := load(false)
					if n := countLines(t, runsPath); n != 3 {
						t.Fatalf("legacy refresh runs=%d, want 3", n)
					}
					if r4.SchemaVersion != 2 {
						t.Fatalf("legacy refresh schemaVersion=%d, want 2", r4.SchemaVersion)
					}
					if got := snapshotModelIDs(r4); !reflect.DeepEqual(got, wantIDs) {
						t.Fatalf("legacy refresh models=%v, want %v (stale cache reused)", got, wantIDs)
					}
					if n := countCacheFiles(t, modelCacheDir); n != 1 {
						t.Fatalf("legacy refresh cache files=%d, want 1", n)
					}
				})
			}
		})
	}
}

func setConfiguredCallerEnv(t *testing.T, p modelProvider, home string) {
	t.Helper()
	t.Setenv(p.envKey, filepath.Join(home, "caller-"+p.name))
	t.Setenv(p.secretKey, "caller-not-a-real-key")
	if p.name == "claude" {
		t.Setenv("ANTHROPIC_MODEL", "caller-model")
		t.Setenv("ANTHROPIC_DEFAULT_OPUS_MODEL", "caller-opus")
		t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
		t.Setenv("CLAUDE_CODE_USE_FOUNDRY", "1")
		t.Setenv("MAX_THINKING_TOKENS", "99999")
		return
	}
	t.Setenv("OPENAI_BASE_URL", "http://caller.invalid")
	t.Setenv("CODEX_SQLITE_HOME", filepath.Join(home, "caller-sqlite"))
	t.Setenv("CODEX_CA_CERTIFICATE", "caller-ca-cert-value")
}

func assertAccountEnv(t *testing.T, rec map[string]string, p modelProvider, dir string, wantPresent bool) {
	t.Helper()
	v, ok := rec[p.envKey]
	if !wantPresent {
		if ok {
			t.Fatalf("%s=%q leaked; the default Claude account must use the CLI builtin default", p.envKey, v)
		}
		return
	}
	if !ok || v != dir {
		t.Fatalf("%s=%q present=%v, want resolved account %q", p.envKey, v, ok, dir)
	}
}

func assertStrippedEnv(t *testing.T, rec map[string]string, p modelProvider) {
	t.Helper()
	var stripped []string
	if p.name == "claude" {
		stripped = []string{
			"ANTHROPIC_API_KEY", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL",
			"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_FOUNDRY", "MAX_THINKING_TOKENS",
		}
	} else {
		stripped = []string{"OPENAI_API_KEY", "OPENAI_BASE_URL", "CODEX_SQLITE_HOME"}
	}
	for _, key := range stripped {
		if v, ok := rec[key]; ok {
			t.Fatalf("caller runtime env %s=%q leaked into the child", key, v)
		}
	}
	if p.name == "codex" {
		if v, ok := rec["CODEX_CA_CERTIFICATE"]; !ok || v != "caller-ca-cert-value" {
			t.Fatalf("CODEX_CA_CERTIFICATE=%q present=%v, want preserved", v, ok)
		}
	}
}

func assertGenericEnv(t *testing.T, rec map[string]string, home, binDir, wantProxy string) {
	t.Helper()
	for key, want := range map[string]string{
		"HOME":               home,
		"PATH":               binDir,
		"HTTPS_PROXY":        wantProxy,
		"NO_PROXY":           "localhost",
		"QUOTA_TEST_GENERIC": "keepme",
	} {
		if v, ok := rec[key]; !ok || v != want {
			t.Fatalf("generic env %s=%q present=%v, want %q preserved", key, v, ok, want)
		}
	}
}

func snapshotModelIDs(s modelcatalog.Snapshot) []string {
	ids := make([]string, len(s.Models))
	for i, m := range s.Models {
		ids[i] = m.ID
	}
	return ids
}

func readEnvRecord(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("malformed env record line %q", line)
		}
		out[key] = val
	}
	return out
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

func countCacheFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	return n
}

func writeLegacyCache(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		if path != "" {
			t.Fatal("multiple catalog files before legacy downgrade")
		}
		path = filepath.Join(dir, e.Name())
	}
	if path == "" {
		t.Fatal("no catalog file to downgrade")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var legacy modelcatalog.Snapshot
	if err := json.Unmarshal(current, &legacy); err != nil {
		t.Fatal(err)
	}
	legacy.SchemaVersion = 1
	legacy.Models = []modelcatalog.Model{{ID: "legacy-model"}}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeModelDiscoveryCLIs(t *testing.T, binDir string) {
	t.Helper()
	claudeScript := `#!/bin/sh
if [ "$1" = --version ]; then
	printf 'model-cli-v1\n'
	exit 0
fi
printf 'RUN\n' >> "$QUOTA_MODEL_RUNS"
[ -n "${CLAUDE_CONFIG_DIR+x}" ] && printf 'CLAUDE_CONFIG_DIR=%s\n' "$CLAUDE_CONFIG_DIR" >> "$QUOTA_MODEL_RECORD"
[ -n "${ANTHROPIC_API_KEY+x}" ] && printf 'ANTHROPIC_API_KEY=%s\n' "$ANTHROPIC_API_KEY" >> "$QUOTA_MODEL_RECORD"
[ -n "${ANTHROPIC_MODEL+x}" ] && printf 'ANTHROPIC_MODEL=%s\n' "$ANTHROPIC_MODEL" >> "$QUOTA_MODEL_RECORD"
[ -n "${ANTHROPIC_DEFAULT_OPUS_MODEL+x}" ] && printf 'ANTHROPIC_DEFAULT_OPUS_MODEL=%s\n' "$ANTHROPIC_DEFAULT_OPUS_MODEL" >> "$QUOTA_MODEL_RECORD"
[ -n "${CLAUDE_CODE_USE_BEDROCK+x}" ] && printf 'CLAUDE_CODE_USE_BEDROCK=%s\n' "$CLAUDE_CODE_USE_BEDROCK" >> "$QUOTA_MODEL_RECORD"
[ -n "${CLAUDE_CODE_USE_FOUNDRY+x}" ] && printf 'CLAUDE_CODE_USE_FOUNDRY=%s\n' "$CLAUDE_CODE_USE_FOUNDRY" >> "$QUOTA_MODEL_RECORD"
[ -n "${MAX_THINKING_TOKENS+x}" ] && printf 'MAX_THINKING_TOKENS=%s\n' "$MAX_THINKING_TOKENS" >> "$QUOTA_MODEL_RECORD"
[ -n "${PATH+x}" ] && printf 'PATH=%s\n' "$PATH" >> "$QUOTA_MODEL_RECORD"
[ -n "${HOME+x}" ] && printf 'HOME=%s\n' "$HOME" >> "$QUOTA_MODEL_RECORD"
[ -n "${HTTPS_PROXY+x}" ] && printf 'HTTPS_PROXY=%s\n' "$HTTPS_PROXY" >> "$QUOTA_MODEL_RECORD"
[ -n "${NO_PROXY+x}" ] && printf 'NO_PROXY=%s\n' "$NO_PROXY" >> "$QUOTA_MODEL_RECORD"
[ -n "${QUOTA_TEST_GENERIC+x}" ] && printf 'QUOTA_TEST_GENERIC=%s\n' "$QUOTA_TEST_GENERIC" >> "$QUOTA_MODEL_RECORD"
read _line
model_id=model-alpha
if [ -n "${ANTHROPIC_MODEL+x}${ANTHROPIC_DEFAULT_OPUS_MODEL+x}${CLAUDE_CODE_USE_BEDROCK+x}${CLAUDE_CODE_USE_FOUNDRY+x}${MAX_THINKING_TOKENS+x}" ]; then model_id=caller-model; fi
printf '%s\n' '{"type":"control_response","response":{"request_id":"quota-model-discovery","subtype":"success","response":{"models":[{"value":"'"$model_id"'","supportsEffort":true,"supportedEffortLevels":["low","high"]},{"value":"model-beta"}]}}}'
`
	codexScript := `#!/bin/sh
if [ "$1" = --version ]; then
	printf 'model-cli-v1\n'
	exit 0
fi
[ "$1" = app-server ] || exit 2
printf 'RUN\n' >> "$QUOTA_MODEL_RUNS"
[ -n "${CODEX_HOME+x}" ] && printf 'CODEX_HOME=%s\n' "$CODEX_HOME" >> "$QUOTA_MODEL_RECORD"
[ -n "${CODEX_SQLITE_HOME+x}" ] && printf 'CODEX_SQLITE_HOME=%s\n' "$CODEX_SQLITE_HOME" >> "$QUOTA_MODEL_RECORD"
[ -n "${CODEX_CA_CERTIFICATE+x}" ] && printf 'CODEX_CA_CERTIFICATE=%s\n' "$CODEX_CA_CERTIFICATE" >> "$QUOTA_MODEL_RECORD"
[ -n "${OPENAI_API_KEY+x}" ] && printf 'OPENAI_API_KEY=%s\n' "$OPENAI_API_KEY" >> "$QUOTA_MODEL_RECORD"
[ -n "${OPENAI_BASE_URL+x}" ] && printf 'OPENAI_BASE_URL=%s\n' "$OPENAI_BASE_URL" >> "$QUOTA_MODEL_RECORD"
[ -n "${PATH+x}" ] && printf 'PATH=%s\n' "$PATH" >> "$QUOTA_MODEL_RECORD"
[ -n "${HOME+x}" ] && printf 'HOME=%s\n' "$HOME" >> "$QUOTA_MODEL_RECORD"
[ -n "${HTTPS_PROXY+x}" ] && printf 'HTTPS_PROXY=%s\n' "$HTTPS_PROXY" >> "$QUOTA_MODEL_RECORD"
[ -n "${NO_PROXY+x}" ] && printf 'NO_PROXY=%s\n' "$NO_PROXY" >> "$QUOTA_MODEL_RECORD"
[ -n "${QUOTA_TEST_GENERIC+x}" ] && printf 'QUOTA_TEST_GENERIC=%s\n' "$QUOTA_TEST_GENERIC" >> "$QUOTA_MODEL_RECORD"
model_id=model-alpha
if [ -n "${OPENAI_API_KEY+x}${OPENAI_BASE_URL+x}${CODEX_SQLITE_HOME+x}" ]; then model_id=caller-model; fi
while IFS= read -r line; do
	case "$line" in
		*'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}' ;;
		*'"method":"model/list"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"data":[{"model":"'"$model_id"'","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"high"}],"defaultReasoningEffort":"low"},{"model":"model-beta"}],"nextCursor":null}}'; exit 0 ;;
	esac
done
`
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(claudeScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(codexScript), 0o755); err != nil {
		t.Fatal(err)
	}
}
