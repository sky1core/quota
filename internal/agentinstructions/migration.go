package agentinstructions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/sky1core/quota/internal/agenthooks"
)

type installTOMLStatement struct {
	start, end int
	text       string
	header     []string
	array      bool
}

func parseInstallTOML(b []byte) (map[string]any, error) {
	root := map[string]any{}
	if err := toml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	return root, nil
}
func scanInstallTOML(b []byte) ([]installTOMLStatement, error) {
	out := []installTOMLStatement{}
	for pos := 0; pos < len(b); {
		if b[pos] == '#' {
			for pos < len(b) && b[pos] != '\n' {
				pos++
			}
			continue
		}
		if strings.ContainsRune(" \t\r\n", rune(b[pos])) {
			pos++
			continue
		}
		start := pos
		depth := 0
		var quote byte
		triple := false
		for pos < len(b) {
			ch := b[pos]
			if quote != 0 {
				if quote == '"' && ch == '\\' {
					pos += 2
					continue
				}
				if triple {
					if ch == quote {
						run := 0
						for pos+run < len(b) && b[pos+run] == quote {
							run++
						}
						if run < 3 {
							pos += run
							continue
						}
						quote = 0
						triple = false
						pos += run
						continue
					}
				} else if ch == quote {
					quote = 0
				}
				pos++
				continue
			}
			if ch == '"' || ch == '\'' {
				quote = ch
				triple = pos+2 < len(b) && b[pos+1] == ch && b[pos+2] == ch
				if triple {
					pos += 3
				} else {
					pos++
				}
				continue
			}
			if ch == '#' {
				if depth == 0 {
					break
				}
				for pos < len(b) && b[pos] != '\n' {
					pos++
				}
				continue
			}
			if ch == '[' || ch == '{' {
				depth++
			}
			if ch == ']' || ch == '}' {
				depth--
			}
			if ch == '\n' && depth == 0 {
				break
			}
			pos++
		}
		if pos > len(b) {
			return nil, fmt.Errorf("invalid TOML string span")
		}
		s := installTOMLStatement{start: start, end: pos, text: strings.TrimSpace(string(b[start:pos]))}
		if strings.HasPrefix(s.text, "[") {
			s.array = strings.HasPrefix(s.text, "[[")
			probe := s.text + "\n__quota_span_probe = true\n"
			var root map[string]any
			md, err := toml.Decode(probe, &root)
			if err != nil {
				return nil, fmt.Errorf("unsupported TOML table span: %w", err)
			}
			for _, k := range md.Keys() {
				if len(k) > 0 && k[len(k)-1] == "__quota_span_probe" {
					s.header = append([]string{}, k[:len(k)-1]...)
					break
				}
			}
		}
		out = append(out, s)
	}
	return out, nil
}
func (i *Installation) migrateTOML(before []byte) ([]byte, error) {
	root, err := parseInstallTOML(before)
	if err != nil {
		return nil, err
	}
	serialized, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	expected := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(serialized))
	decoder.UseNumber()
	if err := decoder.Decode(&expected); err != nil {
		return nil, err
	}
	if err := i.transformHooks(expected, "codex", true); err != nil {
		return nil, err
	}
	if installEqual(root, expected) {
		return before, nil
	}
	statements, err := scanInstallTOML(before)
	if err != nil {
		return nil, err
	}
	hooks, _ := root["hooks"].(map[string]any)
	groupIndexes := map[string]int{}
	entryIndexes := map[string]int{}
	edits := []installTOMLSpan{}
	table := []string{}
	activeRemove := false
	for _, s := range statements {
		if s.header != nil {
			activeRemove = false
			table = s.header
			p := s.header
			if len(p) >= 2 && p[0] == "hooks" {
				event := p[1]
				groups, _ := installArray(hooks[event])
				if len(p) == 2 && s.array {
					idx := groupIndexes[event]
					groupIndexes[event]++
					entryIndexes[event] = 0
					if idx >= len(groups) {
						return nil, fmt.Errorf("TOML hook group span mismatch")
					}
					g, _ := groups[idx].(map[string]any)
					entries, _ := installArray(g["hooks"])
					owned := 0
					for _, e := range entries {
						h, _ := e.(map[string]any)
						c, _ := h["command"].(string)
						if i.owns(c, "codex", event) {
							owned++
						}
					}
					activeRemove = len(entries) > 0 && owned == len(entries)
				} else if len(p) == 3 && p[2] == "hooks" && s.array {
					idx := groupIndexes[event] - 1
					if idx < 0 || idx >= len(groups) {
						return nil, fmt.Errorf("unsupported TOML hook layout; account files unchanged")
					}
					g, _ := groups[idx].(map[string]any)
					entries, _ := installArray(g["hooks"])
					entry := entryIndexes[event]
					entryIndexes[event]++
					if entry >= len(entries) {
						return nil, fmt.Errorf("TOML hook entry span mismatch")
					}
					h, _ := entries[entry].(map[string]any)
					c, _ := h["command"].(string)
					activeRemove = i.owns(c, "codex", event)
				}
			}
		}
		if activeRemove {
			edits = append(edits, installTOMLSpan{s.start, s.end})
		} else if s.header == nil {
			if err := i.inlineStatement(before, s, table, &edits); err != nil {
				return nil, err
			}
		}
	}
	after := applyInstallTOMLEdits(before, edits)
	actual, err := parseInstallTOML(after)
	if err != nil {
		return nil, fmt.Errorf("TOML migration did not preserve syntax: %w", err)
	}
	if h, ok := actual["hooks"].(map[string]any); ok && len(h) == 0 {
		delete(actual, "hooks")
	}
	if !installEqual(actual, expected) {
		return nil, fmt.Errorf("unsupported TOML hook layout: cannot migrate owned hooks without changing unrelated data")
	}
	return after, nil
}

func (i *Installation) trustCodexTOML(before []byte, key, hash string) ([]byte, error) {
	migrated, err := i.migrateTOML(before)
	if err != nil {
		return nil, err
	}
	return syncCodexTrustTOML(migrated, key, hash)
}

func syncCodexTrustTOML(before []byte, key, hash string) ([]byte, error) {
	if key == "" || hash == "" {
		return nil, fmt.Errorf("Codex hook trust key and hash are required")
	}
	root, err := parseInstallTOML(before)
	if err != nil {
		return nil, err
	}
	if err := validateCodexTrustRoot(root); err != nil {
		return nil, err
	}
	if codexTrustStateMatches(root, key, hash) {
		return before, nil
	}
	expected, err := expectedCodexTrustRoot(root, key, hash)
	if err != nil {
		return nil, err
	}
	statements, err := scanInstallTOML(before)
	if err != nil {
		return nil, err
	}
	after, err := upsertCodexTrustState(before, statements, key, hash)
	if err != nil {
		return nil, err
	}
	actual, err := parseInstallTOML(after)
	if err != nil {
		return nil, fmt.Errorf("Codex trust sync did not preserve config.toml syntax: %w", err)
	}
	if err := validateCodexTrustRoot(actual); err != nil {
		return nil, err
	}
	if !codexTrustStateMatches(actual, key, hash) {
		return nil, fmt.Errorf("Codex trust sync did not save the expected hook hash")
	}
	if !installEqual(actual, expected) {
		return nil, fmt.Errorf("Codex trust sync changed unrelated config.toml data")
	}
	return after, nil
}

func validateCodexTrustRoot(root map[string]any) error {
	rawHooks, ok := root["hooks"]
	if !ok {
		return nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return fmt.Errorf("hooks must be an object")
	}
	if rawState, ok := hooks["state"]; ok {
		return agenthooks.ValidateCodexHookState(rawState)
	}
	return nil
}

func codexTrustStateMatches(root map[string]any, key, hash string) bool {
	hooks, _ := root["hooks"].(map[string]any)
	state, _ := hooks["state"].(map[string]any)
	entry, _ := state[key].(map[string]any)
	value, _ := entry["trusted_hash"].(string)
	return value == hash
}

func expectedCodexTrustRoot(root map[string]any, key, hash string) (map[string]any, error) {
	serialized, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	expected := map[string]any{}
	decoder := json.NewDecoder(bytes.NewReader(serialized))
	decoder.UseNumber()
	if err := decoder.Decode(&expected); err != nil {
		return nil, err
	}
	rawHooks, ok := expected["hooks"]
	if !ok {
		rawHooks = map[string]any{}
		expected["hooks"] = rawHooks
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hooks must be an object")
	}
	rawState, ok := hooks["state"]
	if !ok {
		rawState = map[string]any{}
		hooks["state"] = rawState
	}
	state, ok := rawState.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hooks.state must be an object")
	}
	rawEntry, ok := state[key]
	if !ok {
		rawEntry = map[string]any{}
		state[key] = rawEntry
	}
	entry, ok := rawEntry.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("hooks.state.%s must be an object", key)
	}
	entry["trusted_hash"] = hash
	return expected, nil
}

func upsertCodexTrustState(before []byte, statements []installTOMLStatement, key, hash string) ([]byte, error) {
	line := "trusted_hash = " + codexQuotedKeySegment(hash)
	tableStart, tableEnd := -1, len(before)
	inTable := false
	for _, s := range statements {
		if s.header == nil {
			continue
		}
		if inTable {
			tableEnd = s.start
			break
		}
		if len(s.header) == 3 && !s.array && s.header[0] == "hooks" && s.header[1] == "state" && s.header[2] == key {
			tableStart = s.end
			inTable = true
		}
	}
	if tableStart >= 0 {
		active := false
		for _, s := range statements {
			if s.header != nil {
				active = len(s.header) == 3 && !s.array && s.header[0] == "hooks" && s.header[1] == "state" && s.header[2] == key
				continue
			}
			if !active {
				continue
			}
			k, err := installAssignmentKey(before, s)
			if err != nil {
				return nil, err
			}
			if len(k) == 1 && k[0] == "trusted_hash" {
				replacement := []byte(line)
				if s.end < len(before) && before[s.end] == '#' {
					replacement = append(replacement, ' ')
				}
				return replaceInstallTOMLSpan(before, installTOMLSpan{s.start, s.end}, replacement), nil
			}
		}
		insert := []byte(line + "\n")
		if tableEnd > 0 && before[tableEnd-1] != '\n' {
			insert = append([]byte("\n"), insert...)
		}
		return insertInstallTOMLBytes(before, tableEnd, insert), nil
	}
	if after, ok, err := upsertInlineCodexTrustState(before, statements, key, hash); err != nil || ok {
		return after, err
	}
	addition := "\n[hooks.state." + codexQuotedKeySegment(key) + "]\n" + line + "\n"
	if len(bytes.TrimSpace(before)) == 0 {
		addition = addition[1:]
	} else if len(before) > 0 && before[len(before)-1] != '\n' {
		addition = "\n" + addition[1:]
	}
	return append(append([]byte{}, before...), []byte(addition)...), nil
}

func upsertInlineCodexTrustState(before []byte, statements []installTOMLStatement, key, hash string) ([]byte, bool, error) {
	table := []string{}
	var candidate *installTOMLSpan
	var candidateText []byte
	for _, s := range statements {
		if s.header != nil {
			table = s.header
			continue
		}
		assignmentKey, value, err := installAssignmentParts(before, s)
		if err != nil {
			return nil, false, err
		}
		path := append(append([]string{}, table...), assignmentKey...)
		var span installTOMLSpan
		var replacement []byte
		switch {
		case len(path) == 1 && path[0] == "hooks" && value.kind == '{':
			span, replacement, err = upsertTrustInHooksObject(before, value, key, hash)
		case len(path) == 2 && path[0] == "hooks" && path[1] == "state" && value.kind == '{':
			span, replacement, err = upsertTrustInStateObject(before, value, key, hash)
		case len(path) == 3 && path[0] == "hooks" && path[1] == "state" && path[2] == key && value.kind == '{':
			span, replacement, err = upsertTrustInStateEntryObject(before, value, hash)
		case len(path) == 4 && path[0] == "hooks" && path[1] == "state" && path[2] == key && path[3] == "trusted_hash":
			span, replacement, err = installTOMLSpan{value.start, value.end}, []byte(codexQuotedKeySegment(hash)), nil
		case len(path) == 4 && path[0] == "hooks" && path[1] == "state" && path[2] == key:
			if candidate == nil {
				pos := statementLineEnd(before, s)
				candidate = &installTOMLSpan{pos, pos}
				candidateText = []byte(codexTrustHashLine(table, key, hash, before, pos))
			}
			continue
		default:
			continue
		}
		if err != nil {
			return nil, false, err
		}
		return replaceInstallTOMLSpan(before, span, replacement), true, nil
	}
	if candidate != nil {
		return replaceInstallTOMLSpan(before, *candidate, candidateText), true, nil
	}
	return nil, false, nil
}

func upsertTrustInHooksObject(raw []byte, node *installTOMLNode, key, hash string) (installTOMLSpan, []byte, error) {
	var candidate *installTOMLSpan
	var candidateText []byte
	for _, field := range node.fields {
		if len(field.key) == 0 || field.key[0] != "state" {
			continue
		}
		switch {
		case len(field.key) == 1:
			if field.value.kind != '{' {
				return installTOMLSpan{}, nil, fmt.Errorf("hooks.state must be an object")
			}
			return upsertTrustInStateObject(raw, field.value, key, hash)
		case len(field.key) == 2 && field.key[1] == key:
			if field.value.kind != '{' {
				return installTOMLSpan{}, nil, fmt.Errorf("hooks.state.%s must be an object", key)
			}
			return upsertTrustInStateEntryObject(raw, field.value, hash)
		case len(field.key) == 3 && field.key[1] == key && field.key[2] == "trusted_hash":
			return installTOMLSpan{field.value.start, field.value.end}, []byte(codexQuotedKeySegment(hash)), nil
		case len(field.key) == 3 && field.key[1] == key:
			if candidate == nil {
				span, replacement := insertInlineField(node, "state."+codexQuotedKeySegment(key)+".trusted_hash = "+codexQuotedKeySegment(hash))
				candidate, candidateText = &span, replacement
			}
		}
	}
	if candidate != nil {
		return *candidate, candidateText, nil
	}
	value := "state = { " + codexQuotedKeySegment(key) + " = { trusted_hash = " + codexQuotedKeySegment(hash) + " } }"
	span, replacement := insertInlineField(node, value)
	return span, replacement, nil
}

func upsertTrustInStateObject(raw []byte, node *installTOMLNode, key, hash string) (installTOMLSpan, []byte, error) {
	var candidate *installTOMLSpan
	var candidateText []byte
	for _, field := range node.fields {
		switch {
		case len(field.key) == 1 && field.key[0] == key:
			if field.value.kind != '{' {
				return installTOMLSpan{}, nil, fmt.Errorf("hooks.state.%s must be an object", key)
			}
			return upsertTrustInStateEntryObject(raw, field.value, hash)
		case len(field.key) == 2 && field.key[0] == key && field.key[1] == "trusted_hash":
			return installTOMLSpan{field.value.start, field.value.end}, []byte(codexQuotedKeySegment(hash)), nil
		case len(field.key) == 2 && field.key[0] == key:
			if candidate == nil {
				span, replacement := insertInlineField(node, codexQuotedKeySegment(key)+".trusted_hash = "+codexQuotedKeySegment(hash))
				candidate, candidateText = &span, replacement
			}
		}
	}
	if candidate != nil {
		return *candidate, candidateText, nil
	}
	value := codexQuotedKeySegment(key) + " = { trusted_hash = " + codexQuotedKeySegment(hash) + " }"
	span, replacement := insertInlineField(node, value)
	return span, replacement, nil
}

func upsertTrustInStateEntryObject(raw []byte, node *installTOMLNode, hash string) (installTOMLSpan, []byte, error) {
	for _, field := range node.fields {
		if len(field.key) == 1 && field.key[0] == "trusted_hash" {
			return installTOMLSpan{field.value.start, field.value.end}, []byte(codexQuotedKeySegment(hash)), nil
		}
	}
	span, replacement := insertInlineField(node, "trusted_hash = "+codexQuotedKeySegment(hash))
	return span, replacement, nil
}

func insertInlineField(node *installTOMLNode, field string) (installTOMLSpan, []byte) {
	if len(node.fields) == 0 {
		return installTOMLSpan{node.start + 1, node.start + 1}, []byte(" " + field + " ")
	}
	return installTOMLSpan{node.end - 1, node.end - 1}, []byte(", " + field)
}

func statementLineEnd(raw []byte, s installTOMLStatement) int {
	pos := s.end
	for pos < len(raw) && raw[pos] != '\n' {
		pos++
	}
	if pos < len(raw) {
		pos++
	}
	return pos
}

func codexTrustHashLine(table []string, key, hash string, raw []byte, pos int) string {
	prefix := ""
	if pos > 0 && raw[pos-1] != '\n' {
		prefix = "\n"
	}
	switch {
	case len(table) == 2 && table[0] == "hooks" && table[1] == "state":
		return prefix + codexQuotedKeySegment(key) + ".trusted_hash = " + codexQuotedKeySegment(hash) + "\n"
	case len(table) == 1 && table[0] == "hooks":
		return prefix + "state." + codexQuotedKeySegment(key) + ".trusted_hash = " + codexQuotedKeySegment(hash) + "\n"
	default:
		return prefix + "hooks.state." + codexQuotedKeySegment(key) + ".trusted_hash = " + codexQuotedKeySegment(hash) + "\n"
	}
}

func installAssignmentKey(raw []byte, s installTOMLStatement) ([]string, error) {
	key, _, err := installAssignmentParts(raw, s)
	return key, err
}

func installAssignmentParts(raw []byte, s installTOMLStatement) ([]string, *installTOMLNode, error) {
	r := installTOMLReader{b: raw, pos: s.start}
	keyStart := r.pos
	for r.pos < s.end && r.b[r.pos] != '=' {
		if r.b[r.pos] == '"' || r.b[r.pos] == '\'' {
			if err := r.stringEnd(); err != nil {
				return nil, nil, err
			}
		} else {
			r.pos++
		}
	}
	if r.pos >= s.end {
		return nil, nil, fmt.Errorf("unsupported TOML assignment")
	}
	key, err := installTOMLKey(string(raw[keyStart:r.pos]))
	if err != nil {
		return nil, nil, err
	}
	r.pos++
	value, err := r.value()
	if err != nil {
		return nil, nil, err
	}
	return key, value, nil
}

func replaceInstallTOMLSpan(raw []byte, span installTOMLSpan, value []byte) []byte {
	out := append([]byte{}, raw[:span.start]...)
	out = append(out, value...)
	return append(out, raw[span.end:]...)
}

func insertInstallTOMLBytes(raw []byte, pos int, value []byte) []byte {
	out := append([]byte{}, raw[:pos]...)
	out = append(out, value...)
	return append(out, raw[pos:]...)
}

func (i *Installation) updateTOML(path string) (bool, error) {
	return updateTOMLFile(path, false, i.migrateTOML)
}

func (i *Installation) updateTOMLTrust(path, key, hash string) (bool, error) {
	return updateTOMLFile(path, true, func(before []byte) ([]byte, error) {
		return i.trustCodexTOML(before, key, hash)
	})
}

func updateTOMLFile(path string, create bool, transform func([]byte) ([]byte, error)) (bool, error) {
	if err := checkInstallPath(path); err != nil {
		return false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if !create {
			return false, nil
		}
	} else if err != nil {
		return false, err
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return false, err
		}
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return false, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := checkInstallPath(path); err != nil {
		return false, err
	}
	before, err := readInstallFile(path)
	if err != nil {
		return false, err
	}
	existed := true
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		existed = false
		info = nil
	} else if err != nil {
		return false, err
	}
	if !existed && !create {
		return false, nil
	}
	after, err := transform(before)
	if err != nil {
		return false, err
	}
	if bytes.Equal(before, after) {
		return false, nil
	}
	mode := os.FileMode(0o600)
	if existed {
		mode = info.Mode().Perm()
		backup, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".backup-*")
		if err != nil {
			return false, err
		}
		if _, err := backup.Write(before); err != nil {
			backup.Close()
			return false, err
		}
		if err := backup.Sync(); err != nil {
			backup.Close()
			return false, err
		}
		if err := backup.Close(); err != nil {
			return false, err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(after); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	latest, err := os.ReadFile(path)
	if existed {
		if err != nil {
			return false, err
		}
		if !bytes.Equal(latest, before) {
			return false, fmt.Errorf("config.toml changed during migration; preserving external edit")
		}
	} else if err == nil {
		return false, fmt.Errorf("config.toml appeared during migration; preserving external edit")
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return false, err
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		return true, err
	}
	if !bytes.Equal(saved, after) {
		return true, fmt.Errorf("saved config.toml differs from migration")
	}
	return true, nil
}
