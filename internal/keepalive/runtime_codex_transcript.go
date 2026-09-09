//go:build darwin || linux

package keepalive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
)

const codexTranscriptLimit = 64 * 1024 * 1024

type codexTranscript struct {
	pendingCommands                       map[string]bool
	session, cwd, turn, activity          string
	seenMeta, active, answered, complete  bool
	lastRecord, lastActivity              time.Time
	startedAt                             int64
	usageTotalInput, usageTotalCached     int64
	usageBaseInput, usageBaseCached       int64
	usageResultInput, usageResultCached   int64
	usageBroken, usageSeen, usageResultOK bool
	usageTotalOK, usageBaseOK             bool
}

func parseCodexTranscript(ctx context.Context, data []byte, session, cwd string) (codexTranscript, error) {
	s := codexTranscript{session: session, cwd: cwd}
	if len(data) == 0 || len(data) > codexTranscriptLimit || data[len(data)-1] != '\n' {
		return s, errors.New("codex transcript is empty, oversized, or incomplete")
	}
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return s, err
		}
		end := bytes.IndexByte(data, '\n')
		if err := s.consume(data[:end]); err != nil {
			return s, err
		}
		data = data[end+1:]
	}
	return s, nil
}

func (s codexTranscript) idle() bool {
	return len(s.pendingCommands) == 0 && s.seenMeta && !s.active && s.complete && s.answered && s.activity != ""
}

func (s codexTranscript) cacheUsage() *CacheUsage {
	if !s.usageResultOK {
		return nil
	}
	return &CacheUsage{InputTokens: s.usageResultInput, CachedTokens: s.usageResultCached}
}

func (s *codexTranscript) recordUsage(payload json.RawMessage) {
	var v struct {
		Info struct {
			Total struct {
				Input  cacheToken `json:"input_tokens"`
				Cached cacheToken `json:"cached_input_tokens"`
			} `json:"total_token_usage"`
		} `json:"info"`
	}
	if json.Unmarshal(payload, &v) != nil {
		s.usageBroken = true
		return
	}
	total := v.Info.Total
	if !total.Input.present && !total.Cached.present {
		s.usageTotalOK = false
		return
	}
	if !total.Input.valid || !total.Cached.valid || total.Cached.value > total.Input.value || total.Input.value < s.usageTotalInput || total.Cached.value < s.usageTotalCached {
		s.usageBroken = true
		return
	}
	s.usageTotalInput, s.usageTotalCached = total.Input.value, total.Cached.value
	s.usageTotalOK = true
	if s.active {
		s.usageSeen = true
	}
}

func (s *codexTranscript) consume(line []byte) error {
	var r struct {
		Type      string          `json:"type"`
		Timestamp time.Time       `json:"timestamp"`
		Payload   json.RawMessage `json:"payload"`
	}
	if len(line) > 4*1024*1024 || json.Unmarshal(line, &r) != nil || r.Timestamp.IsZero() || r.Timestamp.Before(s.lastRecord) {
		return errors.New("codex transcript record or timestamp is unsupported")
	}
	s.lastRecord = r.Timestamp
	if r.Type == "session_meta" {
		var m struct {
			ID, CWD, Source string
			Version         string `json:"cli_version"`
		}
		if s.seenMeta || json.Unmarshal(r.Payload, &m) != nil || m.ID != s.session || m.CWD != s.cwd || m.Source != "cli" || !versionAtLeast(m.Version, codexMinVersion) {
			return errors.New("codex transcript ownership, source, or version is unsupported")
		}
		s.seenMeta = true
		s.usageTotalOK = true
		return nil
	}
	if !s.seenMeta {
		return errors.New("codex transcript session metadata is missing")
	}
	if r.Type != "event_msg" {
		switch r.Type {
		case "turn_context":
			var v struct {
				Turn string `json:"turn_id"`
				CWD  string `json:"cwd"`
			}
			if json.Unmarshal(r.Payload, &v) != nil || !s.active || v.Turn != s.turn || v.CWD != s.cwd {
				return errors.New("codex turn context changed")
			}
		case "response_item":
			if !s.active {
				return errors.New("codex work outside a tracked turn")
			}
			var item struct {
				Type, Name string
				CallID     string `json:"call_id"`
			}
			if json.Unmarshal(r.Payload, &item) != nil {
				return errors.New("codex response item unavailable")
			}
			if item.Type == "function_call" && (item.Name == "exec_command" || item.Name == "shell_command" || item.Name == "shell") {
				if err := s.commandState(item.CallID, "in_progress", nil); err != nil {
					return err
				}
			}
		case "world_state", "token_usage_record":
			if !s.active {
				return errors.New("codex work outside a tracked turn")
			}
		default:
			return errors.New("codex transcript record type is unsupported")
		}
		return nil
	}
	var e struct {
		Type        string `json:"type"`
		Turn        string `json:"turn_id"`
		Thread      string `json:"thread_id"`
		StartedAt   int64  `json:"started_at"`
		CompletedAt int64  `json:"completed_at"`
		Item        struct {
			Type, ID, Phase string
			Content         json.RawMessage `json:"content"`
			Status          string          `json:"status"`
			ExitCode        *int            `json:"exit_code"`
			ClientID        *string         `json:"client_id"`
		} `json:"item"`
	}
	if json.Unmarshal(r.Payload, &e) != nil {
		return errors.New("codex event shape is unsupported")
	}
	switch e.Type {
	case "task_started":
		if s.active || !runtimeUUID.MatchString(e.Turn) || e.Turn == s.turn || e.StartedAt <= 0 || e.StartedAt > r.Timestamp.Unix() {
			return errors.New("codex turn start is ambiguous")
		}
		s.turn, s.startedAt = e.Turn, e.StartedAt
		s.activity, s.active, s.answered, s.complete = "", true, false, false
		s.usageBaseInput, s.usageBaseCached = s.usageTotalInput, s.usageTotalCached
		s.usageBaseOK = s.usageTotalOK
		s.usageSeen, s.usageResultOK = false, false
	case "item_completed":
		if e.Item.Type == "CommandExecution" && s.pendingCommands[e.Item.ID] && e.Thread == s.session {
			return s.commandState(e.Item.ID, e.Item.Status, e.Item.ExitCode)
		}
		if !s.active || e.Turn != s.turn || e.Thread != s.session || e.Item.ID == "" {
			return errors.New("codex item ownership is ambiguous")
		}
		switch e.Item.Type {
		case "UserMessage":
			if s.activity != "" || !runtimeUUID.MatchString(e.Item.ID) || !codexMessageText(e.Item.Content, "text") {
				return errors.New("codex user input identity is ambiguous")
			}
			s.activity = e.Item.ID
			if e.Item.ClientID != nil {
				if !runtimeUUID.MatchString(*e.Item.ClientID) {
					return errors.New("codex client input identity is unsupported")
				}
				s.activity = *e.Item.ClientID
			}
		case "AgentMessage":
			if s.activity == "" || !codexMessageText(e.Item.Content, "Text") {
				return errors.New("codex answer has no tracked input")
			}
			s.answered = e.Item.Phase == "final_answer"
		case "CommandExecution":
			if err := s.commandState(e.Item.ID, e.Item.Status, e.Item.ExitCode); err != nil {
				return err
			}
			s.answered = false
		case "Reasoning", "Plan", "FileChange", "McpToolCall", "DynamicToolCall", "WebSearch", "ImageView":
			s.answered = false
		default:
			return errors.New("codex completed item type is unsupported")
		}
	case "task_complete":
		if !s.active || e.Turn != s.turn || e.StartedAt != s.startedAt || e.CompletedAt < e.StartedAt || e.CompletedAt > r.Timestamp.Unix() || s.activity == "" || !s.answered {
			return errors.New("codex completion is not correlated to an answered input")
		}
		s.active, s.complete, s.lastActivity = false, true, r.Timestamp
		if !s.usageSeen {
			s.usageTotalOK = false
		}
		if !s.usageBroken && s.usageSeen && s.usageBaseOK && s.usageTotalOK {
			di, dc := s.usageTotalInput-s.usageBaseInput, s.usageTotalCached-s.usageBaseCached
			if di >= 0 && dc >= 0 && dc <= di {
				s.usageResultInput, s.usageResultCached, s.usageResultOK = di, dc, true
			}
		}
	case "exec_approval_request", "apply_patch_approval_request", "request_user_input":
		if !s.active {
			return errors.New("codex waiting work outside a tracked turn")
		}
		s.answered = false
	case "turn_aborted":
		if !s.active || e.Turn != s.turn {
			return errors.New("codex aborted turn is uncorrelated")
		}
		s.active, s.complete, s.answered = false, false, false
		s.usageTotalOK = false
	case "item_started":
		if !s.active || e.Turn != s.turn || e.Thread != s.session {
			return errors.New("codex started item ownership is ambiguous")
		}
		if e.Item.Type == "CommandExecution" {
			if err := s.commandState(e.Item.ID, "in_progress", nil); err != nil {
				return err
			}
		}
	case "exec_command_output_delta":
		var output struct {
			ID string `json:"call_id"`
		}
		if json.Unmarshal(r.Payload, &output) != nil || !s.pendingCommands[output.ID] {
			return errors.New("codex command output is uncorrelated")
		}
	case "exec_command_begin", "exec_command_end":
		var command struct {
			ID       string `json:"call_id"`
			Status   string `json:"status"`
			ExitCode *int   `json:"exit_code"`
		}
		if json.Unmarshal(r.Payload, &command) != nil || command.ID == "" {
			return errors.New("codex command identity unavailable")
		}
		if e.Type == "exec_command_begin" {
			if !s.active {
				return errors.New("codex command outside tracked turn")
			}
			command.Status = "in_progress"
		}
		if err := s.commandState(command.ID, command.Status, command.ExitCode); err != nil {
			return err
		}
	case "thread_settings_applied":
		var settings struct {
			Thread   string `json:"thread_id"`
			Settings struct {
				CWD string `json:"cwd"`
			} `json:"thread_settings"`
		}
		if json.Unmarshal(r.Payload, &settings) != nil || settings.Thread != s.session || settings.Settings.CWD != s.cwd {
			return errors.New("codex applied settings ownership changed")
		}
	case "token_count":
		s.recordUsage(r.Payload)
	case "agent_message", "agent_reasoning", "agent_reasoning_raw_content", "agent_reasoning_section_break", "patch_apply_begin", "patch_apply_end", "mcp_tool_call_begin", "mcp_tool_call_end", "web_search_begin", "web_search_end":
		if !s.active {
			return errors.New("codex work outside a tracked turn")
		}
	default:
		return errors.New("codex event indicates unknown, interrupted, or waiting work")
	}
	return nil
}

func codexMessageText(raw json.RawMessage, kind string) bool {
	var content []struct{ Type, Text string }
	if json.Unmarshal(raw, &content) != nil || len(content) == 0 {
		return false
	}
	hasText := false
	for _, block := range content {
		if block.Type != kind {
			return false
		}
		hasText = hasText || block.Text != ""
	}
	return hasText
}

func (s *codexTranscript) commandState(id, status string, exitCode *int) error {
	if id == "" {
		return errors.New("codex command identity unavailable")
	}
	if s.pendingCommands == nil {
		s.pendingCommands = map[string]bool{}
	}
	switch status {
	case "in_progress":
		s.answered = false
		s.pendingCommands[id] = true
	case "completed", "failed":
		if exitCode == nil {
			s.pendingCommands[id] = true
		} else {
			delete(s.pendingCommands, id)
		}
	case "declined":
		delete(s.pendingCommands, id)
	default:
		return errors.New("codex command execution status unsupported")
	}
	return nil
}
