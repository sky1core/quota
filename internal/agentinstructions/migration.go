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
					if pos+2 < len(b) && ch == quote && b[pos+1] == quote && b[pos+2] == quote {
						quote = 0
						triple = false
						pos += 3
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
func (i *Installation) updateTOML(path string) (bool, error) {
	if err := checkInstallPath(path); err != nil {
		return false, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
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
	after, err := i.migrateTOML(before)
	if err != nil {
		return false, err
	}
	if bytes.Equal(before, after) {
		return false, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
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
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
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
	if err != nil {
		return false, err
	}
	if !bytes.Equal(latest, before) {
		return false, fmt.Errorf("config.toml changed during migration; preserving external edit")
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
