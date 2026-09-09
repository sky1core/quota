package keepalive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
)

type claudeTranscript struct {
	metaInput                 bool
	session, activity, parent string
	lastActivity              time.Time
	turnStarted, completedAt  time.Time
	assistant, complete       bool
	queued                    int
	usage                     claudeUsage
}

type cacheToken struct {
	value          int64
	present, valid bool
}

func (c *cacheToken) UnmarshalJSON(data []byte) error {
	wasInvalid := c.present && !c.valid
	*c = cacheToken{present: true}
	if wasInvalid {
		return nil
	}
	var v int64
	if bytes.Equal(data, []byte("null")) || json.Unmarshal(data, &v) != nil || v < 0 {
		return nil
	}
	c.value, c.valid = v, true
	return nil
}

type claudeUsage struct {
	byRequest map[string][2]int64
	bad       bool
}

func (u *claudeUsage) reset() { *u = claudeUsage{} }

func (u *claudeUsage) record(m claudeLogMessage) {
	if u.bad {
		return
	}
	if m.Usage == nil || m.ID == "" {
		u.bad = true
		return
	}
	in, read, create := m.Usage.InputTokens, m.Usage.CacheReadInputTokens, m.Usage.CacheCreationInputTokens
	if !in.present || !in.valid || !read.present || !read.valid || !create.present || !create.valid {
		u.bad = true
		return
	}
	total, ok := runtimeAddTokens(in.value, read.value)
	if ok {
		total, ok = runtimeAddTokens(total, create.value)
	}
	if !ok {
		u.bad = true
		return
	}
	if u.byRequest == nil {
		u.byRequest = map[string][2]int64{}
	}
	u.byRequest[m.ID] = [2]int64{total, read.value}
}

func (u claudeUsage) result() *CacheUsage {
	if u.bad || len(u.byRequest) == 0 {
		return nil
	}
	var input, cached int64
	for _, r := range u.byRequest {
		var ok bool
		if input, ok = runtimeAddTokens(input, r[0]); !ok {
			return nil
		}
		if cached, ok = runtimeAddTokens(cached, r[1]); !ok {
			return nil
		}
	}
	return &CacheUsage{InputTokens: input, CachedTokens: cached}
}

type claudeLogRecord struct {
	Attachment struct {
		Type string `json:"type"`
	} `json:"attachment"`
	Type                        string          `json:"type"`
	Subtype                     string          `json:"subtype"`
	SessionID                   string          `json:"sessionId"`
	UUID                        string          `json:"uuid"`
	ParentUUID                  string          `json:"parentUuid"`
	Timestamp                   string          `json:"timestamp"`
	IsSidechain                 bool            `json:"isSidechain"`
	IsMeta                      bool            `json:"isMeta"`
	IsCompactSummary            bool            `json:"isCompactSummary"`
	Operation                   string          `json:"operation"`
	PendingBackgroundAgentCount claudeWorkCount `json:"pendingBackgroundAgentCount"`
	PendingWorkflowCount        claudeWorkCount `json:"pendingWorkflowCount"`
	DurationMS                  *int64          `json:"durationMs"`
	Message                     json.RawMessage `json:"message"`
}

type claudeWorkCount int

func (n *claudeWorkCount) UnmarshalJSON(data []byte) error {
	var value int
	if bytes.Equal(data, []byte("null")) || json.Unmarshal(data, &value) != nil || value < 0 {
		return errors.New("claude background work count is unknown")
	}
	*n = claudeWorkCount(value)
	return nil
}

type claudeLogMessage struct {
	ID         string          `json:"id"`
	Role       string          `json:"role"`
	Model      string          `json:"model"`
	StopReason *string         `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	Usage      *struct {
		OutputTokens             int        `json:"output_tokens"`
		InputTokens              cacheToken `json:"input_tokens"`
		CacheReadInputTokens     cacheToken `json:"cache_read_input_tokens"`
		CacheCreationInputTokens cacheToken `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

func parseClaudeTranscript(ctx context.Context, data []byte, session string) (claudeTranscript, error) {
	s := claudeTranscript{session: session}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return s, errors.New("claude transcript is empty or has an incomplete record")
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

func (s *claudeTranscript) idle() bool {
	return !s.metaInput && s.responseComplete()
}

func (s *claudeTranscript) cacheUsage() *CacheUsage { return s.usage.result() }

func (s *claudeTranscript) responseComplete() bool {
	return s.activity != "" && s.assistant && s.complete && s.queued == 0
}

func (s *claudeTranscript) consume(line []byte) error {
	var record claudeLogRecord
	if len(line) == 0 || len(line) > 4*1024*1024 || json.Unmarshal(line, &record) != nil || record.Type == "" {
		return errors.New("claude transcript record is malformed or unsupported")
	}
	if record.SessionID != "" && record.SessionID != s.session || record.IsSidechain {
		return errors.New("claude transcript session ownership mismatch")
	}
	switch record.Type {
	case "queue-operation":
		if record.SessionID != s.session {
			return errors.New("claude queue record session is missing")
		}
		switch record.Operation {
		case "enqueue":
			s.queued++
		case "dequeue", "remove", "popAll", "popOne":
			if s.queued == 0 {
				return errors.New("claude queue history is incomplete")
			}
			s.queued--
		default:
			return errors.New("claude queue operation is unsupported")
		}
	case "user", "assistant":
		if record.SessionID != s.session || !runtimeUUID.MatchString(record.UUID) {
			return errors.New("claude turn identity is missing")
		}
		at, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil || at.Before(s.lastActivity) {
			return errors.New("claude turn timestamp is invalid")
		}
		var message claudeLogMessage
		if json.Unmarshal(record.Message, &message) != nil || message.Role != record.Type {
			return errors.New("claude message shape is unsupported")
		}
		hasText, containsTools, err := claudeContent(message.Content)
		if err != nil {
			return err
		}
		if record.Type == "user" && !containsTools {
			s.activity, s.parent = record.UUID, record.UUID
			s.metaInput = record.IsMeta
			s.turnStarted = at
			s.complete, s.assistant = false, false
			s.usage.reset()
			if record.IsCompactSummary || !hasText {
				s.activity = ""
			}
			return nil
		}
		if s.activity == "" {
			s.complete = false
			return nil
		}
		if at.Before(s.turnStarted) {
			return errors.New("claude response precedes its input")
		}
		if record.ParentUUID != s.parent {
			return errors.New("claude turn ancestry is ambiguous")
		}
		s.parent, s.complete = record.UUID, false
		if record.Type == "user" {
			s.assistant = false
			return nil
		}
		s.lastActivity = at
		s.usage.record(message)
		s.assistant = hasText && !containsTools && message.Model != "" && message.Model != "<synthetic>" && message.Usage != nil && message.Usage.OutputTokens > 0 && (message.StopReason == nil || *message.StopReason == "end_turn")
	case "attachment":
		switch record.Attachment.Type {
		case "environment", "model", "instructions", "session_context", "date", "remote_session_change", "prompt_snapshot":
		default:
			return errors.New("claude attachment type is unsupported")
		}
		if record.SessionID != s.session || !runtimeUUID.MatchString(record.UUID) || record.ParentUUID != s.parent {
			return errors.New("claude attachment ancestry is ambiguous")
		}
		s.parent = record.UUID
	case "system":
		if record.Subtype == "turn_duration" {
			at, err := time.Parse(time.RFC3339Nano, record.Timestamp)
			if err != nil || at.Before(s.lastActivity) || record.SessionID != s.session || record.DurationMS == nil || *record.DurationMS < 0 || !runtimeUUID.MatchString(record.UUID) {
				return errors.New("claude turn completion is malformed")
			}
			s.completedAt = at
			s.complete = s.assistant && record.PendingBackgroundAgentCount == 0 && record.PendingWorkflowCount == 0
		} else if record.Subtype == "stop_hook_summary" {
			s.complete = false
		} else {
			s.complete, s.assistant = false, false
		}
		if runtimeUUID.MatchString(record.UUID) {
			s.parent = record.UUID
		}
	case "file-history-snapshot", "file-history-delta", "attribution-snapshot", "last-prompt", "summary", "custom-title", "ai-title", "tag", "agent-name", "agent-color", "agent-setting", "mode", "permission-mode", "cost-state", "atis-latch", "bridge-session":
	case "progress":
		s.complete, s.assistant = false, false
	default:
		return errors.New("claude transcript record type is unsupported")
	}
	return nil
}

func claudeContent(raw json.RawMessage) (hasText, containsTools bool, err error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != "", false, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil || len(blocks) == 0 {
		return false, false, errors.New("claude message content is unsupported")
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			hasText = hasText || block.Text != ""
		case "tool_result", "tool_use":
			containsTools = true
		case "thinking", "redacted_thinking", "image", "document":
		default:
			return false, false, errors.New("claude content block type is unsupported")
		}
	}
	return hasText, containsTools, nil
}
