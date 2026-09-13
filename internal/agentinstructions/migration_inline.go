package agentinstructions

import (
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

type installTOMLSpan struct{ start, end int }
type installTOMLNode struct {
	start, end int
	kind       byte
	fields     []installTOMLField
	removed    map[int]bool
}
type installTOMLField struct {
	start, end, comma int
	key               []string
	value             *installTOMLNode
}
type installTOMLReader struct {
	b   []byte
	pos int
}

func (r *installTOMLReader) skip() {
	for r.pos < len(r.b) {
		if strings.ContainsRune(" \t\r\n", rune(r.b[r.pos])) {
			r.pos++
			continue
		}
		if r.b[r.pos] == '#' {
			for r.pos < len(r.b) && r.b[r.pos] != '\n' {
				r.pos++
			}
			continue
		}
		break
	}
}
func (r *installTOMLReader) stringEnd() error {
	quote := r.b[r.pos]
	triple := r.pos+2 < len(r.b) && r.b[r.pos+1] == quote && r.b[r.pos+2] == quote
	if triple {
		r.pos += 3
	} else {
		r.pos++
	}
	for r.pos < len(r.b) {
		if quote == '"' && r.b[r.pos] == '\\' {
			r.pos += 2
			continue
		}
		if r.b[r.pos] == quote {
			if !triple {
				r.pos++
				return nil
			}
			if r.pos+2 < len(r.b) && r.b[r.pos+1] == quote && r.b[r.pos+2] == quote {
				r.pos += 3
				return nil
			}
		}
		r.pos++
	}
	return fmt.Errorf("unterminated TOML string")
}
func installTOMLKey(text string) ([]string, error) {
	var root map[string]any
	md, err := toml.Decode(strings.TrimSpace(text)+" = true", &root)
	if err != nil {
		return nil, err
	}
	keys := md.Keys()
	if len(keys) == 0 {
		return nil, fmt.Errorf("missing TOML key")
	}
	return []string(keys[len(keys)-1]), nil
}
func (r *installTOMLReader) value() (*installTOMLNode, error) {
	r.skip()
	if r.pos >= len(r.b) {
		return nil, fmt.Errorf("missing TOML value")
	}
	n := &installTOMLNode{start: r.pos, kind: r.b[r.pos]}
	if n.kind == '"' || n.kind == '\'' {
		if err := r.stringEnd(); err != nil {
			return nil, err
		}
		n.end = r.pos
		return n, nil
	}
	if n.kind != '[' && n.kind != '{' {
		for r.pos < len(r.b) && !strings.ContainsRune(",]}#\r\n", rune(r.b[r.pos])) {
			r.pos++
		}
		n.end = r.pos
		return n, nil
	}
	r.pos++
	close := byte(']')
	if n.kind == '{' {
		close = '}'
	}
	for {
		r.skip()
		if r.pos >= len(r.b) {
			return nil, fmt.Errorf("unterminated TOML container")
		}
		if r.b[r.pos] == close {
			r.pos++
			n.end = r.pos
			return n, nil
		}
		f := installTOMLField{start: r.pos, comma: -1}
		if n.kind == '{' {
			keyStart := r.pos
			for r.pos < len(r.b) && r.b[r.pos] != '=' {
				if r.b[r.pos] == '"' || r.b[r.pos] == '\'' {
					if err := r.stringEnd(); err != nil {
						return nil, err
					}
				} else {
					r.pos++
				}
			}
			if r.pos >= len(r.b) {
				return nil, fmt.Errorf("missing TOML equals")
			}
			key, err := installTOMLKey(string(r.b[keyStart:r.pos]))
			if err != nil {
				return nil, err
			}
			f.key = key
			r.pos++
		}
		v, err := r.value()
		if err != nil {
			return nil, err
		}
		f.value = v
		f.end = v.end
		r.skip()
		if r.pos < len(r.b) && r.b[r.pos] == ',' {
			f.comma = r.pos
			r.pos++
		} else if r.pos >= len(r.b) || r.b[r.pos] != close {
			return nil, fmt.Errorf("missing TOML separator")
		}
		n.fields = append(n.fields, f)
	}
}
func installRemoveField(n *installTOMLNode, index int, edits *[]installTOMLSpan) {
	f := n.fields[index]
	*edits = append(*edits, installTOMLSpan{f.start, f.end})
	if n.removed == nil {
		n.removed = map[int]bool{}
	}
	n.removed[index] = true
}
func finishInstallInline(n *installTOMLNode, edits *[]installTOMLSpan) {
	if len(n.removed) == 0 {
		return
	}
	lastKept := -1
	for index := range n.fields {
		if !n.removed[index] {
			lastKept = index
		}
	}
	for index, f := range n.fields {
		if f.comma >= 0 && (n.removed[index] || index == lastKept) {
			*edits = append(*edits, installTOMLSpan{f.comma, f.comma + 1})
		}
	}
}
func (i *Installation) inlineOwned(raw []byte, n *installTOMLNode, event string) bool {
	parsed, err := parseInstallTOML(append([]byte("value = "), raw[n.start:n.end]...))
	if err != nil {
		return false
	}
	h, _ := parsed["value"].(map[string]any)
	command, _ := h["command"].(string)
	return i.owns(command, "codex", event)
}
func (i *Installation) inlineEntries(raw []byte, n *installTOMLNode, event string, edits *[]installTOMLSpan) bool {
	if n.kind != '[' {
		return false
	}
	owned := 0
	for index, f := range n.fields {
		if i.inlineOwned(raw, f.value, event) {
			owned++
			installRemoveField(n, index, edits)
		}
	}
	finishInstallInline(n, edits)
	return owned > 0 && owned == len(n.fields)
}
func (i *Installation) inlineGroup(raw []byte, n *installTOMLNode, event string, edits *[]installTOMLSpan) bool {
	if n.kind != '{' {
		return false
	}
	for _, f := range n.fields {
		if len(f.key) == 1 && f.key[0] == "hooks" {
			return i.inlineEntries(raw, f.value, event, edits)
		}
	}
	return false
}
func (i *Installation) inlineEvent(raw []byte, n *installTOMLNode, event string, edits *[]installTOMLSpan) bool {
	if n.kind != '[' {
		return false
	}
	removed := 0
	for index, f := range n.fields {
		childEdits := []installTOMLSpan{}
		if i.inlineGroup(raw, f.value, event, &childEdits) {
			removed++
			installRemoveField(n, index, edits)
		} else {
			*edits = append(*edits, childEdits...)
		}
	}
	finishInstallInline(n, edits)
	return removed > 0 && removed == len(n.fields)
}
func (i *Installation) inlineRoot(raw []byte, n *installTOMLNode, edits *[]installTOMLSpan) {
	if n.kind != '{' {
		return
	}
	for index, f := range n.fields {
		if len(f.key) != 1 {
			continue
		}
		childEdits := []installTOMLSpan{}
		if i.inlineEvent(raw, f.value, f.key[0], &childEdits) {
			installRemoveField(n, index, edits)
		} else {
			*edits = append(*edits, childEdits...)
		}
	}
	finishInstallInline(n, edits)
}
func (i *Installation) inlineStatement(raw []byte, s installTOMLStatement, table []string, edits *[]installTOMLSpan) error {
	r := installTOMLReader{b: raw, pos: s.start}
	keyStart := r.pos
	for r.pos < s.end && r.b[r.pos] != '=' {
		if r.b[r.pos] == '"' || r.b[r.pos] == '\'' {
			if err := r.stringEnd(); err != nil {
				return err
			}
		} else {
			r.pos++
		}
	}
	if r.pos >= s.end {
		return fmt.Errorf("unsupported TOML assignment")
	}
	key, err := installTOMLKey(string(raw[keyStart:r.pos]))
	if err != nil {
		return err
	}
	path := append(append([]string{}, table...), key...)
	if len(path) == 0 || path[0] != "hooks" {
		return nil
	}
	r.pos++
	n, err := r.value()
	if err != nil {
		return err
	}
	switch len(path) {
	case 1:
		i.inlineRoot(raw, n, edits)
	case 2:
		if i.inlineEvent(raw, n, path[1], edits) {
			*edits = append(*edits, installTOMLSpan{s.start, s.end})
		}
	case 3:
		if path[2] == "hooks" {
			i.inlineEntries(raw, n, path[1], edits)
		}
	}
	return nil
}
func applyInstallTOMLEdits(raw []byte, edits []installTOMLSpan) []byte {
	sort.Slice(edits, func(a, b int) bool { return edits[a].start < edits[b].start })
	merged := []installTOMLSpan{}
	for _, e := range edits {
		if len(merged) > 0 && e.start <= merged[len(merged)-1].end {
			if e.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = e.end
			}
		} else {
			merged = append(merged, e)
		}
	}
	comments := []installTOMLSpan{}
	r := installTOMLReader{b: raw}
	for r.pos < len(raw) {
		if raw[r.pos] == '"' || raw[r.pos] == '\'' {
			if err := r.stringEnd(); err != nil {
				break
			}
			continue
		}
		if raw[r.pos] == '#' {
			start := r.pos
			for r.pos < len(raw) && raw[r.pos] != '\n' {
				r.pos++
			}
			if r.pos < len(raw) {
				r.pos++
			}
			comments = append(comments, installTOMLSpan{start, r.pos})
			continue
		}
		r.pos++
	}
	out := []byte{}
	pos := 0
	for _, e := range merged {
		out = append(out, raw[pos:e.start]...)
		for _, c := range comments {
			if c.start >= e.start && c.end <= e.end {
				out = append(out, raw[c.start:c.end]...)
			}
		}
		pos = e.end
	}
	return append(out, raw[pos:]...)
}
