package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sky1core/quota/internal/config"
)

const (
	defaultSessionLogLimit       = 20
	defaultSessionLogTail        = 40
	defaultSessionLogSnippetSize = 220
)

type sessionLogAccount struct {
	Provider string
	Key      string
	Root     string
}

type sessionLogRecord struct {
	Provider  string    `json:"provider"`
	Account   string    `json:"account"`
	Path      string    `json:"path"`
	UpdatedAt time.Time `json:"updatedAt"`
	Size      int64     `json:"size"`
}

type sessionLogHit struct {
	Provider  string    `json:"provider"`
	Account   string    `json:"account"`
	Path      string    `json:"path"`
	UpdatedAt time.Time `json:"updatedAt"`
	Line      int       `json:"line"`
	Role      string    `json:"role"`
	Snippet   string    `json:"snippet"`
}

type sessionLogMessage struct {
	Line int    `json:"line"`
	Role string `json:"role"`
	Text string `json:"text"`
}

type sessionLogShowOutput struct {
	Session  sessionLogRecord    `json:"session"`
	Messages []sessionLogMessage `json:"messages"`
}

type sessionLogCommonOptions struct {
	agent   string
	account string
}

func printSessionLogUsage() {
	fmt.Fprint(os.Stderr, `usage:
  quota-cli session-log list   [-agent all|claude|codex] [-account key] [-limit N] [-json]
  quota-cli session-log search [-agent all|claude|codex] [-account key] [-limit N] [-max-chars N] [-include-tools] [-json] <query>
  quota-cli session-log show   [-agent all|claude|codex] [-account key] [-tail N] [-max-chars N] [-include-tools] [-json] <session-ref>

기본은 configured Claude/Codex 계정 전체를 읽기 전용으로 검색하며, tool 결과 원문은 제외한다.
`)
}

func runSessionLog(args []string) int {
	if len(args) == 0 {
		printSessionLogUsage()
		return 2
	}
	switch args[0] {
	case "list", "ls":
		return sessionLogList(args[1:], os.Stdout, os.Stderr)
	case "search", "grep":
		return sessionLogSearch(args[1:], os.Stdout, os.Stderr)
	case "show":
		return sessionLogShow(args[1:], os.Stdout, os.Stderr)
	default:
		fmt.Fprintf(os.Stderr, "unknown session-log command: %q\n\n", args[0])
		printSessionLogUsage()
		return 2
	}
}

func newSessionLogFlagSet(name string, output io.Writer) (*flag.FlagSet, *string, *string, *bool) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(output)
	agent := fs.String("agent", "all", "Filter provider: all, claude, codex")
	account := fs.String("account", "", "Filter account key")
	jsonOut := fs.Bool("json", false, "Output JSON")
	return fs, agent, account, jsonOut
}

func sessionLogList(args []string, stdout, stderr io.Writer) int {
	fs, agent, account, jsonOut := newSessionLogFlagSet("quota-cli session-log list", io.Discard)
	limit := fs.Int("limit", defaultSessionLogLimit, "Maximum sessions to print")
	if err := fs.Parse(sessionLogInterspersedFlags(args, map[string]bool{"agent": true, "account": true, "limit": true})); err != nil {
		return sessionLogFlagError(err, fs, stderr)
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "unexpected argument: %q\n", fs.Arg(0))
		return 2
	}
	if !validateSessionLogPositiveFlag("limit", *limit, stderr) {
		return 2
	}

	records, code := loadSessionLogRecords(sessionLogCommonOptions{agent: *agent, account: *account}, stderr)
	if code != 0 {
		return code
	}
	records = limitSessionLogRecords(records, *limit)
	if *jsonOut {
		return encodeSessionLogJSON(stdout, map[string]any{"sessions": records})
	}
	for _, record := range records {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", record.UpdatedAt.Format(time.RFC3339), record.Provider, record.Account, record.Path)
	}
	return 0
}

func sessionLogSearch(args []string, stdout, stderr io.Writer) int {
	fs, agent, account, jsonOut := newSessionLogFlagSet("quota-cli session-log search", io.Discard)
	limit := fs.Int("limit", defaultSessionLogLimit, "Maximum matches to print")
	maxChars := fs.Int("max-chars", defaultSessionLogSnippetSize, "Maximum snippet characters")
	includeTools := fs.Bool("include-tools", false, "Include tool calls and tool results")
	if err := fs.Parse(sessionLogInterspersedFlags(args, map[string]bool{"agent": true, "account": true, "limit": true, "max-chars": true})); err != nil {
		return sessionLogFlagError(err, fs, stderr)
	}
	query := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if query == "" {
		fmt.Fprintln(stderr, "session-log search requires a query")
		return 2
	}
	if !validateSessionLogPositiveFlag("limit", *limit, stderr) || !validateSessionLogMaxCharsFlag(*maxChars, stderr) {
		return 2
	}

	records, code := loadSessionLogRecords(sessionLogCommonOptions{agent: *agent, account: *account}, stderr)
	if code != 0 {
		return code
	}
	hits, err := searchSessionLogRecords(records, query, *limit, *maxChars, *includeTools)
	if err != nil {
		fmt.Fprintln(stderr, "session-log search error:", err)
		return 1
	}
	if *jsonOut {
		return encodeSessionLogJSON(stdout, map[string]any{"matches": hits})
	}
	for _, hit := range hits {
		fmt.Fprintf(stdout, "%s\t%s\t%s:%d\t%s\t%s\n", hit.Provider, hit.Account, hit.Path, hit.Line, hit.Role, hit.Snippet)
	}
	return 0
}

func sessionLogShow(args []string, stdout, stderr io.Writer) int {
	fs, agent, account, jsonOut := newSessionLogFlagSet("quota-cli session-log show", io.Discard)
	tail := fs.Int("tail", defaultSessionLogTail, "Maximum text messages to print from the end")
	maxChars := fs.Int("max-chars", defaultSessionLogSnippetSize*4, "Maximum characters per message")
	includeTools := fs.Bool("include-tools", false, "Include tool calls and tool results")
	if err := fs.Parse(sessionLogInterspersedFlags(args, map[string]bool{"agent": true, "account": true, "tail": true, "max-chars": true})); err != nil {
		return sessionLogFlagError(err, fs, stderr)
	}
	ref := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if ref == "" {
		fmt.Fprintln(stderr, "session-log show requires a session ref")
		return 2
	}
	if !validateSessionLogPositiveFlag("tail", *tail, stderr) || !validateSessionLogMaxCharsFlag(*maxChars, stderr) {
		return 2
	}

	records, code := loadSessionLogRecords(sessionLogCommonOptions{agent: *agent, account: *account}, stderr)
	if code != 0 {
		return code
	}
	record, ok := resolveSessionLogRef(records, ref, stderr)
	if !ok {
		return 1
	}
	messages, err := readSessionLogMessages(record.Path, *tail, *includeTools)
	if err != nil {
		fmt.Fprintln(stderr, "session-log show error:", err)
		return 1
	}
	messages = limitSessionLogMessageChars(messages, *maxChars)
	if *jsonOut {
		return encodeSessionLogJSON(stdout, sessionLogShowOutput{Session: record, Messages: messages})
	}
	fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", record.UpdatedAt.Format(time.RFC3339), record.Provider, record.Account, record.Path)
	for _, msg := range messages {
		fmt.Fprintf(stdout, "%d\t%s\t%s\n", msg.Line, msg.Role, collapseWhitespace(msg.Text))
	}
	return 0
}

func sessionLogFlagError(err error, fs *flag.FlagSet, stderr io.Writer) int {
	if err == flag.ErrHelp {
		fs.SetOutput(stderr)
		fs.Usage()
		return 0
	}
	fmt.Fprintln(stderr, err)
	return 2
}

func validateSessionLogPositiveFlag(name string, value int, stderr io.Writer) bool {
	if value >= 1 {
		return true
	}
	fmt.Fprintf(stderr, "session-log --%s must be >= 1\n", name)
	return false
}

func validateSessionLogMaxCharsFlag(value int, stderr io.Writer) bool {
	if value >= 0 {
		return true
	}
	fmt.Fprintln(stderr, "session-log --max-chars must be >= 0")
	return false
}

func sessionLogInterspersedFlags(args []string, valueFlags map[string]bool) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			flags = append(flags, arg)
			name := sessionLogFlagName(arg)
			if valueFlags[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	return append(flags, positionals...)
}

func sessionLogFlagName(arg string) string {
	name := strings.TrimLeft(arg, "-")
	if i := strings.Index(name, "="); i >= 0 {
		name = name[:i]
	}
	return name
}

func loadSessionLogRecords(opts sessionLogCommonOptions, stderr io.Writer) ([]sessionLogRecord, int) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "config load error:", err)
		return nil, 1
	}
	accounts, err := sessionLogAccounts(cfg, opts.agent, opts.account)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, 2
	}
	records, err := discoverSessionLogRecords(accounts)
	if err != nil {
		fmt.Fprintln(stderr, "session-log list error:", err)
		return nil, 1
	}
	return records, 0
}

func sessionLogAccounts(cfg config.Config, agent, account string) ([]sessionLogAccount, error) {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "" {
		agent = "all"
	}
	if agent != "all" && agent != "claude" && agent != "codex" {
		return nil, fmt.Errorf("invalid session-log agent: %q", agent)
	}
	var accounts []sessionLogAccount
	if agent == "all" || agent == "claude" {
		claudeAccounts, skipped := cfg.ResolveAccounts()
		if len(skipped) > 0 {
			return nil, fmt.Errorf("invalid Claude account config: %s", strings.Join(skipped, "; "))
		}
		for _, a := range claudeAccounts {
			accounts = append(accounts, sessionLogAccount{Provider: "claude", Key: a.Key, Root: claudeSessionLogRoot(a.ConfigDir)})
		}
	}
	if agent == "all" || agent == "codex" {
		codexAccounts, skipped := cfg.ResolveCodexAccounts()
		if len(skipped) > 0 {
			return nil, fmt.Errorf("invalid Codex account config: %s", strings.Join(skipped, "; "))
		}
		for _, a := range codexAccounts {
			accounts = append(accounts, sessionLogAccount{Provider: "codex", Key: a.Key, Root: codexSessionLogRoot(a.Home)})
		}
	}
	if account == "" {
		return accounts, nil
	}
	filtered := accounts[:0]
	for _, a := range accounts {
		if a.Key == account {
			filtered = append(filtered, a)
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("session-log account %q not found for agent %q", account, agent)
	}
	return filtered, nil
}

func claudeSessionLogRoot(configDir string) string {
	if configDir != "" {
		return filepath.Join(configDir, "projects")
	}
	if override := strings.TrimSpace(os.Getenv("CLAUDE_PROJECTS_DIR")); override != "" {
		return config.ExpandTilde(override)
	}
	if envDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); envDir != "" {
		return filepath.Join(config.ExpandTilde(envDir), "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

func codexSessionLogRoot(homeDir string) string {
	if homeDir != "" {
		return filepath.Join(homeDir, "sessions")
	}
	if override := strings.TrimSpace(os.Getenv("CODEX_SESSIONS_DIR")); override != "" {
		return config.ExpandTilde(override)
	}
	if envHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); envHome != "" {
		return filepath.Join(config.ExpandTilde(envHome), "sessions")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "sessions")
}

func discoverSessionLogRecords(accounts []sessionLogAccount) ([]sessionLogRecord, error) {
	var records []sessionLogRecord
	for _, account := range accounts {
		if _, err := os.Stat(account.Root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		err := filepath.WalkDir(account.Root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			records = append(records, sessionLogRecord{
				Provider:  account.Provider,
				Account:   account.Key,
				Path:      path,
				UpdatedAt: info.ModTime(),
				Size:      info.Size(),
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].UpdatedAt.Equal(records[j].UpdatedAt) {
			return records[i].Path < records[j].Path
		}
		return records[i].UpdatedAt.After(records[j].UpdatedAt)
	})
	return records, nil
}

func limitSessionLogRecords(records []sessionLogRecord, limit int) []sessionLogRecord {
	if limit < 1 {
		return nil
	}
	if len(records) <= limit {
		return records
	}
	return records[:limit]
}

func searchSessionLogRecords(records []sessionLogRecord, query string, limit, maxChars int, includeTools bool) ([]sessionLogHit, error) {
	if limit < 1 {
		return nil, fmt.Errorf("limit must be >= 1")
	}
	if maxChars < 0 {
		return nil, fmt.Errorf("max-chars must be >= 0")
	}
	var hits []sessionLogHit
	lowerQuery := strings.ToLower(query)
	for _, record := range records {
		err := scanSessionLogMessages(record.Path, includeTools, func(line int, role, text string) bool {
			if !strings.Contains(strings.ToLower(text), lowerQuery) {
				return true
			}
			hits = append(hits, sessionLogHit{
				Provider:  record.Provider,
				Account:   record.Account,
				Path:      record.Path,
				UpdatedAt: record.UpdatedAt,
				Line:      line,
				Role:      role,
				Snippet:   snippetAround(text, query, maxChars),
			})
			return len(hits) < limit
		})
		if err != nil {
			return nil, err
		}
		if len(hits) >= limit {
			break
		}
	}
	return hits, nil
}

func readSessionLogMessages(path string, tail int, includeTools bool) ([]sessionLogMessage, error) {
	if tail < 1 {
		return nil, fmt.Errorf("tail must be >= 1")
	}
	var messages []sessionLogMessage
	err := scanSessionLogMessages(path, includeTools, func(line int, role, text string) bool {
		messages = append(messages, sessionLogMessage{Line: line, Role: role, Text: text})
		if tail > 0 && len(messages) > tail {
			copy(messages, messages[len(messages)-tail:])
			messages = messages[:tail]
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return messages, nil
}

func limitSessionLogMessageChars(messages []sessionLogMessage, maxChars int) []sessionLogMessage {
	if maxChars <= 0 {
		return messages
	}
	out := make([]sessionLogMessage, len(messages))
	for i, msg := range messages {
		msg.Text = truncateRunes(msg.Text, maxChars)
		out[i] = msg
	}
	return out
}

func scanSessionLogMessages(path string, includeTools bool, visit func(line int, role, text string) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	line := 0
	for {
		rawLine, err := reader.ReadBytes('\n')
		if len(rawLine) > 0 {
			line++
			rawLine = bytes.TrimRight(rawLine, "\r\n")
			role, text, ok := parseSessionLogLine(rawLine, includeTools)
			if ok && !visit(line, role, text) {
				break
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
	return nil
}

func parseSessionLogLine(line []byte, includeTools bool) (string, string, bool) {
	var value any
	if err := json.Unmarshal(line, &value); err != nil {
		return "", "", false
	}
	role := sessionLogRole(value)
	if !includeTools && role != "user" && role != "assistant" {
		return "", "", false
	}
	if includeTools && role == "" {
		role = sessionLogType(value)
	}
	if role == "" {
		return "", "", false
	}
	text := strings.TrimSpace(strings.Join(sessionLogTextChunks(value, includeTools), "\n"))
	if text == "" {
		return "", "", false
	}
	return role, text, true
}

func sessionLogRole(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if role := normalizedSessionLogRole(stringField(m, "role")); role != "" {
		return role
	}
	if role := normalizedSessionLogRole(stringField(m, "type")); role != "" {
		return role
	}
	for _, key := range []string{"message", "payload"} {
		if nested, ok := m[key]; ok {
			if role := sessionLogRole(nested); role != "" {
				return role
			}
		}
	}
	return ""
}

func sessionLogType(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if payload, ok := m["payload"]; ok {
		if typ := sessionLogType(payload); typ != "" && typ != "message" {
			return typ
		}
	}
	if message, ok := m["message"]; ok {
		if typ := sessionLogType(message); typ != "" && typ != "message" {
			return typ
		}
	}
	if typ := stringField(m, "type"); typ != "" {
		return typ
	}
	return ""
}

func normalizedSessionLogRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user", "assistant":
		return strings.ToLower(strings.TrimSpace(role))
	default:
		return ""
	}
}

func sessionLogTextChunks(value any, includeTools bool) []string {
	switch v := value.(type) {
	case map[string]any:
		if !includeTools && sessionLogMapIsTool(v) {
			return nil
		}
		var chunks []string
		for _, key := range []string{"message", "payload"} {
			if nested, ok := v[key]; ok {
				if text, ok := nested.(string); ok {
					chunks = append(chunks, text)
				} else {
					chunks = append(chunks, sessionLogTextChunks(nested, includeTools)...)
				}
			}
		}
		if content, ok := v["content"]; ok {
			chunks = append(chunks, sessionLogContentChunks(content, includeTools)...)
		}
		if text := stringField(v, "text"); text != "" {
			chunks = append(chunks, text)
		}
		if includeTools {
			for _, key := range []string{"name", "input", "arguments", "output", "result", "error"} {
				if text := sessionLogFieldText(v, key); text != "" {
					chunks = append(chunks, text)
				}
			}
		}
		return chunks
	case []any:
		return sessionLogContentChunks(v, includeTools)
	default:
		return nil
	}
}

func sessionLogContentChunks(value any, includeTools bool) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		var chunks []string
		for _, item := range v {
			chunks = append(chunks, sessionLogContentChunks(item, includeTools)...)
		}
		return chunks
	case map[string]any:
		if !includeTools && sessionLogMapIsTool(v) {
			return nil
		}
		typ := strings.ToLower(stringField(v, "type"))
		if typ == "" || typ == "text" || typ == "input_text" || typ == "output_text" {
			return sessionLogTextChunks(v, includeTools)
		}
		if includeTools {
			return sessionLogTextChunks(v, includeTools)
		}
		return nil
	default:
		return nil
	}
}

func sessionLogMapIsTool(m map[string]any) bool {
	for _, key := range []string{"type", "role"} {
		value := strings.ToLower(stringField(m, key))
		if strings.Contains(value, "tool") || strings.Contains(value, "function") {
			return true
		}
	}
	return false
}

func stringField(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func sessionLogFieldText(m map[string]any, key string) string {
	value, ok := m[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	if string(encoded) == "null" {
		return ""
	}
	return string(encoded)
}

func resolveSessionLogRef(records []sessionLogRecord, ref string, stderr io.Writer) (sessionLogRecord, bool) {
	ref = strings.TrimSpace(ref)
	var matches []sessionLogRecord
	cleanRef := filepath.Clean(config.ExpandTilde(ref))
	for _, record := range records {
		cleanPath := filepath.Clean(record.Path)
		switch {
		case cleanPath == cleanRef:
			return record, true
		case filepath.Base(record.Path) == ref:
			matches = append(matches, record)
		case strings.Contains(record.Path, ref):
			matches = append(matches, record)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	if len(matches) == 0 {
		fmt.Fprintf(stderr, "session log %q not found\n", ref)
		return sessionLogRecord{}, false
	}
	fmt.Fprintf(stderr, "session log ref %q is ambiguous:\n", ref)
	for _, match := range limitSessionLogRecords(matches, defaultSessionLogLimit) {
		fmt.Fprintf(stderr, "  %s\t%s\t%s\n", match.Provider, match.Account, match.Path)
	}
	return sessionLogRecord{}, false
}

func encodeSessionLogJSON(stdout io.Writer, value any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintf(os.Stderr, "json encode: %v\n", err)
		return 1
	}
	return 0
}

func snippetAround(text, query string, maxChars int) string {
	text = collapseWhitespace(text)
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	lowerText := strings.ToLower(text)
	lowerQuery := strings.ToLower(query)
	index := strings.Index(lowerText, lowerQuery)
	if index < 0 {
		return truncateRunes(text, maxChars)
	}
	queryStart := utf8.RuneCountInString(text[:index])
	queryLen := utf8.RuneCountInString(query)
	start := queryStart - (maxChars-queryLen)/2
	if start < 0 {
		start = 0
	}
	end := start + maxChars
	runes := []rune(text)
	if end > len(runes) {
		end = len(runes)
		start = end - maxChars
		if start < 0 {
			start = 0
		}
	}
	out := string(runes[start:end])
	if start > 0 {
		out = "..." + out
	}
	if end < len(runes) {
		out += "..."
	}
	return out
}

func truncateRunes(text string, maxChars int) string {
	if maxChars <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxChars {
		return text
	}
	return string(runes[:maxChars]) + "..."
}

func collapseWhitespace(text string) string {
	return strings.Join(strings.FieldsFunc(text, unicode.IsSpace), " ")
}
