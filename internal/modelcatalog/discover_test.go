package modelcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestCodexDiscoveryPaging(t *testing.T) {
	input := `
{"method":"notice","params":{"account":"private-placeholder"}}
{"id":99,"result":{}}
{"id":1,"result":{}}
{"id":2,"result":{"data":[{"id":"opaque-placeholder","model":"model-alpha","displayName":"Model Alpha","supportedReasoningEfforts":[{"reasoningEffort":"moderate","description":"placeholder"}],"defaultReasoningEffort":"moderate","hidden":true}],"nextCursor":"cursor-placeholder"}}
{"id":3,"result":{"data":[{"model":"model-beta"}],"nextCursor":null}}
`
	var output bytes.Buffer
	models, err := discoverCodex(newProtocolStream(context.Background(), iotest.OneByteReader(strings.NewReader(input)), &output))
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	want := []Model{
		{ID: "model-alpha", DisplayName: "Model Alpha", SupportsEffort: &yes, SupportedEfforts: []string{"moderate"}, DefaultEffort: "moderate", Hidden: true},
		{ID: "model-beta"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v; want %#v", models, want)
	}
	assertMessages(t, output.Bytes(), []string{
		`{"id":1,"method":"initialize","params":{"clientInfo":{"name":"quota-cli","version":"0.1.0"},"capabilities":{}}}`,
		`{"method":"initialized"}`,
		`{"id":2,"method":"model/list","params":{"limit":100,"includeHidden":true,"cursor":null}}`,
		`{"id":3,"method":"model/list","params":{"limit":100,"includeHidden":true,"cursor":"cursor-placeholder"}}`,
	})
}

func assertMessages(t *testing.T, data []byte, expected []string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	for _, raw := range expected {
		var got, want any
		if err := decoder.Decode(&got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("request = %#v; want %#v", got, want)
		}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected extra request: %#v, %v", extra, err)
	}
}

func TestCodexCapabilities(t *testing.T) {
	models, _, err := decodeCodexPage(json.RawMessage(`{"data":[
{"model":"model-unknown"},
{"model":"model-null","supportedReasoningEfforts":null},
{"model":"model-empty","supportedReasoningEfforts":[]}
]}`))
	if err != nil {
		t.Fatal(err)
	}
	if models[0].SupportsEffort != nil || models[1].SupportsEffort != nil || models[0].SupportedEfforts != nil {
		t.Fatal("missing or null capabilities must stay unknown")
	}
	if models[2].SupportsEffort == nil || *models[2].SupportsEffort || models[2].SupportedEfforts == nil {
		t.Fatal("explicit empty supported efforts must remain known empty")
	}
}

func TestClaudeDiscoveryCapabilities(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"system","commands":["private-placeholder"],"agents":["private-placeholder"]}`,
		`{"type":"control_response","response":{"request_id":"other-placeholder","subtype":"error"}}`,
		strings.ReplaceAll(`{"type":"control_response","response":{"request_id":"quota-model-discovery","subtype":"success","response":{"models":[
{"value":"alias-alpha","resolvedModel":"model-alpha[1m]","displayName":"Model Alpha","supportsEffort":true,"supportedEffortLevels":["moderate"]},
{"value":"alias-unknown"},
{"value":"alias-false","supportsEffort":false},
{"value":"alias-levels","supportedEffortLevels":["moderate"]},
{"value":"alias-true","supportsEffort":true},
{"value":"alias-empty","supportedEffortLevels":[]}
],"account":"private-placeholder","commands":["private-placeholder"]}}}`, "\n", ""),
	}, "\n")
	var output bytes.Buffer
	models, err := discoverClaude(newProtocolStream(context.Background(), strings.NewReader(input), &output))
	if err != nil {
		t.Fatal(err)
	}
	yes, no := true, false
	want := []Model{
		{ID: "alias-alpha", ResolvedModel: "model-alpha[1m]", DisplayName: "Model Alpha", SupportsEffort: &yes, SupportedEfforts: []string{"moderate"}},
		{ID: "alias-unknown"},
		{ID: "alias-false", SupportsEffort: &no},
		{ID: "alias-levels", SupportedEfforts: []string{"moderate"}},
		{ID: "alias-true", SupportsEffort: &yes},
		{ID: "alias-empty", SupportedEfforts: []string{}},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("models = %#v; want %#v", models, want)
	}
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-placeholder") || !strings.Contains(string(raw), `"supportsEffort":false`) {
		t.Fatalf("unexpected serialized models: %s", raw)
	}
	assertMessages(t, output.Bytes(), []string{`{"type":"control_request","request_id":"quota-model-discovery","request":{"subtype":"initialize"}}`})
}

func TestCodexProtocolErrors(t *testing.T) {
	tests := []struct{ name, input, want string }{
		{"initialize error", `{"id":1,"error":{"code":-1,"message":"private-placeholder"}}`, "code -1"},
		{"invalid error", `{"id":1,"error":"private-placeholder"}`, "invalid RPC error"},
		{"null result", `{"id":1,"result":null}`, "invalid RPC result"},
		{"missing result", `{"id":1}`, "invalid RPC result"},
		{"invalid JSON", `{"id":1,`, "invalid protocol JSON"},
		{"unexpected EOF", `{"id":99,"result":{}}`, "unexpected EOF"},
		{"list error", "{\"id\":1,\"result\":{}}\n" + `{"id":2,"error":{"code":-2,"message":"private-placeholder"}}`, "code -2"},
		{"repeated cursor", "{\"id\":1,\"result\":{}}\n" + `{"id":2,"result":{"data":[],"nextCursor":"private-placeholder"}}` + "\n" + `{"id":3,"result":{"data":[],"nextCursor":"private-placeholder"}}`, "repeated pagination cursor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models, err := discoverCodex(newProtocolStream(context.Background(), strings.NewReader(tt.input), io.Discard))
			if models != nil || err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), "private-placeholder") {
				t.Fatalf("models = %#v, err = %v", models, err)
			}
		})
	}
}

func TestClaudeProtocolErrors(t *testing.T) {
	for _, input := range []string{
		`{"type":"control_response","response":null}`,
		`{"type":"control_response","response":"private-placeholder"}`,
		`{"type":"control_response","response":{"request_id":"quota-model-discovery","subtype":"error","error":"private-placeholder"}}`,
		`{"type":"control_response","response":{"request_id":"quota-model-discovery","subtype":"success","response":{}}}`,
		`{"type":"control_response","response":{"request_id":"quota-model-discovery","subtype":"success","response":{"models":"private-placeholder"}}}`,
		`{"type":"control_response",`,
		`{"type":"system"}`,
	} {
		models, err := discoverClaude(newProtocolStream(context.Background(), strings.NewReader(input), io.Discard))
		if models != nil || err == nil || strings.Contains(err.Error(), "private-placeholder") {
			t.Fatalf("input = %s; models = %#v, err = %v", input, models, err)
		}
	}
}

func TestInvalidModelData(t *testing.T) {
	for _, input := range []string{`{}`, `{"data":null}`, `{"data":[{}]}`, `{"data":[null]}`, `{"data":[{"model":"placeholder","supportedReasoningEfforts":[{}]}]}`, `{"data":[],"nextCursor":3}`} {
		if _, _, err := decodeCodexPage(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted invalid codex data %s", input)
		}
	}
	for _, input := range []string{`{}`, `{"models":null}`, `{"models":[{}]}`, `{"models":[null]}`, `{"models":[{"value":"placeholder","supportsEffort":"false"}]}`, `{"models":[{"value":"placeholder","supportedEffortLevels":[null]}]}`} {
		if _, err := decodeClaudeModels(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted invalid claude data %s", input)
		}
	}
	models, _, err := decodeCodexPage(json.RawMessage(`{"data":[]}`))
	if err != nil || models == nil || len(models) != 0 {
		t.Fatalf("empty codex catalog = %#v, %v", models, err)
	}
	models, err = decodeClaudeModels(json.RawMessage(`{"models":[]}`))
	if err != nil || models == nil || len(models) != 0 {
		t.Fatalf("empty claude catalog = %#v, %v", models, err)
	}
}

func TestStreamBoundsAndCancellation(t *testing.T) {
	s := newProtocolStream(context.Background(), strings.NewReader(strings.Repeat("x", maxMessageBytes+1)), io.Discard)
	if _, err := s.read(); err == nil {
		t.Fatal("accepted oversized message")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s = newProtocolStream(ctx, strings.NewReader(`{"id":1,"result":{}}`), io.Discard)
	if _, err := s.read(); !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancellation = %v", err)
	}
	if err := s.send(map[string]any{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("send cancellation = %v", err)
	}
	_, err := newProtocolStream(context.Background(), strings.NewReader(""), io.Discard).rpcResult(1)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("EOF = %v", err)
	}
}

func TestCommandEnvironment(t *testing.T) {
	env := []string{"ACCOUNT_PLACEHOLDER=old", "PATH=/placeholder/bin"}
	cmd, err := command(context.Background(), Target{Binary: "/placeholder/cli", ConfigDir: "/unused-placeholder", Env: env}, "--version")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cmd.Env, env) || !reflect.DeepEqual(cmd.Args, []string{"/placeholder/cli", "--version"}) {
		t.Fatalf("unexpected command: args %v env %v", cmd.Args, cmd.Env)
	}
	cmd.Env[0] = "changed"
	if env[0] != "ACCOUNT_PLACEHOLDER=old" {
		t.Fatal("mutated caller environment")
	}
	for _, target := range []Target{{Binary: "cli", Env: []string{}}, {Binary: "/placeholder/cli"}} {
		if _, err := command(context.Background(), target); err == nil {
			t.Fatal("accepted implicit binary or environment")
		}
	}
	cmd, err = command(context.Background(), Target{Binary: "/placeholder/cli", Env: []string{}})
	if err != nil || cmd.Env == nil || len(cmd.Env) != 0 {
		t.Fatalf("explicit empty environment not preserved: %v", err)
	}
}

func TestBoundedDiagnostics(t *testing.T) {
	var output boundedOutput
	data := bytes.Repeat([]byte("private-placeholder"), maxDiagnosticBytes)
	for range 2 {
		if n, err := output.Write(data); err != nil || n != len(data) {
			t.Fatalf("write = %d, %v", n, err)
		}
	}
	if len(output.data) != maxDiagnosticBytes || !output.truncated {
		t.Fatalf("unbounded diagnostics: %d bytes", len(output.data))
	}
	err := processError(context.Background(), "initialize", errors.New("failed"), &output)
	if strings.Contains(err.Error(), "private-placeholder") || !strings.Contains(err.Error(), "4096 bytes") {
		t.Fatalf("unexpected diagnostics: %v", err)
	}
}
