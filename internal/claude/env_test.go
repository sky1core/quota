package claude

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnvForConfigDirSeparatesRuntimeSettingsFromGeneralEnvironment(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	general := []string{"HOME=" + home, "PATH=/bin", "TMPDIR=/tmp", "LANG=C.UTF-8",
		"HTTPS_PROXY=http://proxy.invalid:8080", "NO_PROXY=localhost", "SSL_CERT_FILE=/cert.pem",
		"NODE_EXTRA_CA_CERTS=/extra.pem", "DO_NOT_TRACK=1", "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318",
		"DEBUG=example:*", "MCP_EXAMPLE_ENDPOINT=http://example.invalid", "CLAUDE_CODE_CLIENT_CERT=/client.pem",
		"CLAUDE_CODE_CLIENT_KEY=/client-key.pem", "CLAUDE_CODE_CLIENT_KEY_PASSPHRASE=example-passphrase",
		"UNRELATED=value"}
	runtime := []string{"CLAUDE_CONFIG_DIR=/caller", "CLAUDECODE=1", "CLAUDE_PROJECTS_DIR=/caller-projects",
		"CLAUDE_CODE_USE_BEDROCK=1", "CLAUDE_CODE_USE_FOUNDRY=1", "ANTHROPIC_MODEL=caller-model",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=caller-alias", "CLAUDE_FUTURE_SETTING=caller",
		"ANTHROPIC_FUTURE_SETTING=caller", "MAX_THINKING_TOKENS=0", "DISABLE_AUTO_COMPACT=1",
		"DISABLE_PROMPT_CACHING_FUTURE=1", "ENABLE_PROMPT_CACHING_FUTURE=1",
		"MCP_TOOL_TIMEOUT=1", "MCP_CONNECTION_NONBLOCKING=0", "MCP_DISCOVERY_CACHE=0", "MCP_CLIENT_SECRET=caller-secret",
		"BASH_MAX_TIMEOUT_MS=1", "FALLBACK_FOR_ALL_PRIMARY_MODELS=1",
		"VERTEX_REGION_CLAUDE_FUTURE=region", "ENABLE_CLAUDEAI_MCP_SERVERS=0", "FORCE_PROMPT_CACHING_5M=1",
		"SLASH_COMMAND_TOOL_CHAR_BUDGET=1", "OTEL_LOG_USER_PROMPTS=1"}
	base := append(append([]string{}, general...), runtime...)
	before := append([]string{}, base...)
	for _, dir := range []string{"", filepath.Join(home, ".claude"), filepath.Join(home, "extra")} {
		want := append([]string{}, general...)
		if dir == filepath.Join(home, "extra") {
			want = append(want, "CLAUDE_CONFIG_DIR="+dir)
		}
		if got := EnvForConfigDir(base, dir); !reflect.DeepEqual(got, want) {
			t.Fatalf("environment for %q = %v, want %v", dir, got, want)
		}
	}
	if !reflect.DeepEqual(base, before) {
		t.Fatal("caller environment was modified")
	}
}
