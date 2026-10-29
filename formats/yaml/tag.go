package yaml

import (
	"bytes"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/vasileknik76/tql/syntax"
)

type tagInfo struct {
	name       string
	uri        string
	start, end int
}

func (p *parser) tagProperty(src []byte, start, end int) (tagInfo, int, error) {
	pos := start
	if strings.HasPrefix(string(src[start:end]), "!<") {
		pos += 2
		valueStart := pos
		for pos < end && src[pos] != '>' {
			pos++
		}
		if pos == end {
			return tagInfo{}, pos, errAt(start, "unterminated tag")
		}
		if pos+1 < end && !strings.ContainsRune(" \t\r\n,]}", rune(src[pos+1])) {
			return tagInfo{}, pos, errAt(pos+1, "tag property must be separated from its node")
		}
		uri, err := decodeTagURI(string(src[valueStart:pos]), valueStart)
		if err != nil {
			return tagInfo{}, pos, err
		}
		return resolvedTag(uri, start, pos+1), pos + 1, nil
	}
	for pos < end && !strings.ContainsRune(" \t\r\n[]{},", rune(src[pos])) {
		pos++
	}
	raw := string(src[start:pos])
	if pos < end && (src[pos] == '[' || src[pos] == '{') {
		return tagInfo{}, pos, errAt(pos, "tag property must be separated from its node")
	}
	if raw == "!" {
		return resolvedTag("!", start, pos), pos, nil
	}
	handle, suffix := "!", raw[1:]
	if i := strings.IndexByte(raw[1:], '!'); i >= 0 {
		handle, suffix = raw[:i+2], raw[i+2:]
	}
	if !validTagHandle(handle) {
		return tagInfo{}, pos, errAt(start, "invalid tag handle")
	}
	if suffix == "" {
		return tagInfo{}, pos, errAt(start, "empty tag suffix")
	}
	if strings.ContainsAny(suffix, "![]{},") {
		return tagInfo{}, pos, errAt(start, "invalid tag suffix")
	}
	prefix, ok := p.tagHandles[handle]
	if !ok {
		switch handle {
		case "!":
			prefix, ok = "!", true
		case "!!":
			prefix, ok = "tag:yaml.org,2002:", true
		}
	}
	if !ok {
		return tagInfo{}, pos, errAt(start, "undefined tag handle "+handle)
	}
	uri, err := decodeTagURI(prefix+suffix, start)
	if err != nil {
		return tagInfo{}, pos, err
	}
	return resolvedTag(uri, start, pos), pos, nil
}
func validTagHandle(handle string) bool {
	if handle == "!" || handle == "!!" {
		return true
	}
	if len(handle) < 3 || handle[0] != '!' || handle[len(handle)-1] != '!' {
		return false
	}
	for _, c := range handle[1 : len(handle)-1] {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' {
			continue
		}
		return false
	}
	return true
}
func decodeTagURI(raw string, start int) (string, error) {
	if raw == "" {
		return "", errAt(start, "empty tag URI")
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '%' {
			if i+2 >= len(raw) {
				return "", errAt(start+i, "incomplete tag URI escape")
			}
			i += 2
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-#;/?:@&=+$,_.!~*'()[]", rune(c)) {
			continue
		}
		return "", errAt(start+i, "invalid tag URI character")
	}
	uri, err := url.PathUnescape(raw)
	if err != nil || !utf8.ValidString(uri) {
		return "", errAt(start, "invalid tag URI escape")
	}
	return uri, nil
}
func resolvedTag(uri string, start, end int) tagInfo {
	name := uri
	if after, ok := strings.CutPrefix(uri, "tag:yaml.org,2002:"); ok {
		name = after
	}
	return tagInfo{name: name, uri: uri, start: start, end: end}
}
func pendingTag(n *syntax.Node) (tagInfo, bool) {
	t, ok := n.Meta["pendingTag"].(tagInfo)
	return t, ok
}
func (p *parser) applyTag(n *syntax.Node, t tagInfo) error {
	if _, alias := n.Meta["aliasName"]; alias {
		return errAt(t.start, "alias nodes cannot have tag properties")
	}
	if _, exists := n.Meta["tag"]; exists {
		return errAt(t.start, "duplicate tag properties")
	}
	n.Meta = meta(n.Meta)
	n.Meta["tag"] = t
	n.Meta["tagURI"] = t.uri
	return nil
}
func (p *parser) constructTag(n *syntax.Node, t tagInfo) error {
	value := n.Value
	text, hasText := n.Meta["scalarValue"].(string)
	if !hasText && n.Kind == syntax.String {
		text = value.(string)
	} else if !hasText && n.Style == "plain" {
		text = string(p.src[n.Start:n.End])
	}
	switch t.name {
	case "!":
		if n.Kind != syntax.Object && n.Kind != syntax.Array {
			n.Kind, n.Value = syntax.String, text
		}
	case "str":
		if n.Kind == syntax.Object || n.Kind == syntax.Array {
			return errAt(t.start, "!!str requires a scalar")
		}
		if n.Kind == syntax.Null && n.Start == n.End {
			text = ""
		}
		n.Kind = syntax.String
		n.Value = text
	case "int", "float":
		kind, value := scalarKind(text)
		if kind != syntax.NumberKind {
			return errAt(t.start, "invalid !!"+t.name+" scalar")
		}
		if t.name == "int" && !intLiteral(string(value.(syntax.Number))) {
			return errAt(t.start, "invalid !!int scalar")
		}
		n.Kind, n.Value = syntax.NumberKind, value
	case "bool":
		kind, value := scalarKind(text)
		if kind != syntax.Bool {
			return errAt(t.start, "invalid !!bool scalar")
		}
		n.Kind, n.Value = syntax.Bool, value
	case "null":
		if n.Kind == syntax.Object || n.Kind == syntax.Array {
			return errAt(t.start, "!!null requires a scalar")
		}
		kind, _ := scalarKind(text)
		if text == "" {
			kind = syntax.Null
		}
		if kind != syntax.Null {
			return errAt(t.start, "invalid !!null scalar")
		}
		n.Kind, n.Value = syntax.Null, nil
	case "map":
		if n.Kind != syntax.Object {
			return errAt(t.start, "!!map requires a mapping")
		}
	case "seq":
		if n.Kind != syntax.Array {
			return errAt(t.start, "!!seq requires a sequence")
		}
	default:
		return errAt(t.start, "tag cannot be represented by TQL: "+t.uri)
	}
	return nil
}
func tagFits(t tagInfo, value any) bool {
	switch t.name {
	case "!":
		switch value.(type) {
		case string, syntax.Temporal, map[string]any, []any:
			return true
		}
	case "str":
		switch value.(type) {
		case string, syntax.Temporal:
			return true
		}
	case "int":
		switch x := value.(type) {
		case syntax.Number:
			return intLiteral(string(x))
		case int, int64:
			return true
		}
	case "float":
		switch value.(type) {
		case syntax.Number, int, int64, float64:
			return true
		}
	case "bool":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "map":
		_, ok := value.(map[string]any)
		return ok
	case "seq":
		_, ok := value.([]any)
		return ok
	}
	return false
}
func tagEdits(src []byte, target *syntax.Node, value any, edits []syntax.Edit) []syntax.Edit {
	if tag, ok := target.Meta["tag"].(tagInfo); ok && !tagFits(tag, value) {
		end := tag.end
		if target.Parent == nil {
			lineStart := bytes.LastIndexAny(src[:tag.start], "\r\n") + 1
			if strings.Trim(string(src[lineStart:tag.start]), " \t") == "" {
				// Property separation must not become indentation after removing
				// a leading root tag, especially before an anchor or a tab.
				for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
					end++
				}
			}
		}
		edits = append(edits, syntax.Edit{Start: tag.start, End: end})
	}
	return edits
}
