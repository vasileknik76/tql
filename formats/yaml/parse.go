// Package yaml implements a source-aware YAML 1.2 configuration profile.
// The package README documents supported constructs and compatibility limits.
package yaml

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/vasileknik76/tql/internal/scan"
	"github.com/vasileknik76/tql/syntax"
)

// Format parses and edits YAML documents without reformatting untouched source.
type Format struct{ style syntax.Style }

// WithStyle returns an independent backend with an explicit generation policy.
func (f Format) WithStyle(style syntax.Style) (syntax.Format, error) {
	if err := style.Validate("yaml"); err != nil {
		return nil, err
	}
	f.style = style
	return f, nil
}

var _ syntax.Format = Format{}

type line struct {
	start, end, next, indent int
	text                     string
	inlineRoot               bool
}
type parser struct {
	arena         nodeArena
	src           []byte
	lines         []line
	i, depth      int
	propertyDepth int
	anchors       map[string]*syntax.Node
	tagHandles    map[string]string
}
type segment struct{ start, end int }

func errAt(pos int, msg string) error { return fmt.Errorf("yaml: byte %d: %s", pos, msg) }

// Parse reads one document and returns a linked, source-aware syntax tree.
// It does not modify source.
func (Format) Parse(src []byte) (*syntax.Node, error) { return projectDocument(src) }

func parseDocuments(src []byte) ([]*syntax.Node, error) {
	if !utf8.Valid(src) {
		return nil, errAt(0, "invalid UTF-8")
	}
	if i := scan.YAMLControl(src); i < len(src) {
		return nil, errAt(i, "non-printable character")
	}
	if scan.NonASCII(src) < len(src) {
		for i, r := range string(src) {
			if r >= 0x7f && r <= 0x9f && r != 0x85 || r == 0xfffe || r == 0xffff {
				return nil, errAt(i, "non-printable character")
			}
		}
	}
	p := &parser{src: src}
	offset := 0
	if strings.HasPrefix(string(src), "\xef\xbb\xbf") {
		offset = 3
	}
	for off := offset; off < len(src); {
		end := off + scan.LineBreak(src[off:])
		next := end
		if next < len(src) {
			next++
			if src[end] == '\r' && next < len(src) && src[next] == '\n' {
				next++
			}
		}
		in := 0
		for off+in < end && src[off+in] == ' ' {
			in++
		}
		p.lines = append(p.lines, line{start: off, end: end, next: next, indent: in, text: string(src[off+in : end])})
		off = next
	}

	documents := []*syntax.Node{}
	documentStart := 0
	for {
		p.skip()
		for p.i < len(p.lines) && marker(p.lines[p.i], "...") {
			if err := p.endMarker(); err != nil {
				return nil, err
			}
			p.skip()
		}
		if p.i == len(p.lines) {
			break
		}
		p.anchors = nil
		p.tagHandles = map[string]string{"!": "!", "!!": "tag:yaml.org,2002:"}
		version, hadDirective, err := p.directives()
		if err != nil {
			return nil, err
		}
		explicitStart := p.i < len(p.lines) && marker(p.lines[p.i], "---")
		if explicitStart {
			l := &p.lines[p.i]
			start := l.start + 3
			for start < l.end && (src[start] == ' ' || src[start] == '\t') {
				start++
			}
			if start == l.end || src[start] == '#' {
				p.i++
				p.skip()
			} else {
				text := string(src[start:l.end])
				if seq(text) || explicitKey(text) || colon(text) >= 0 {
					return nil, errAt(start, "block collections cannot follow --- on the same line")
				}
				l.start, l.text, l.inlineRoot = start, text, true
			}
		} else if hadDirective {
			return nil, errAt(0, "a directive requires ---")
		}
		var root *syntax.Node
		if p.i == len(p.lines) || marker(p.lines[p.i], "---") || marker(p.lines[p.i], "...") {
			pos := len(src)
			if p.i < len(p.lines) {
				pos = p.lines[p.i].start
			}
			root = emptyScalar(&p.arena, pos)
		} else {
			root, err = p.block(p.lines[p.i].indent, -1)
			if err != nil {
				return nil, err
			}
		}
		p.skip()
		explicitEnd := p.i < len(p.lines) && marker(p.lines[p.i], "...")
		if explicitEnd {
			if err := p.endMarker(); err != nil {
				return nil, err
			}
			p.skip()
		}
		documentEnd := len(src)
		if p.i < len(p.lines) {
			documentEnd = p.lines[p.i].start
			if !marker(p.lines[p.i], "---") && !explicitEnd {
				return nil, errAt(documentEnd, "unexpected content after document")
			}
		}
		root.Meta = meta(root.Meta)
		root.Meta["documentStart"], root.Meta["documentEnd"] = documentStart, documentEnd
		root.Meta["explicitStart"], root.Meta["explicitEnd"], root.Meta["version"] = explicitStart, explicitEnd, version
		documents = append(documents, root)
		documentStart = documentEnd
	}
	return documents, nil
}

func (p *parser) directives() (string, bool, error) {
	hadDirective, hadYAML := false, false
	version := ""
	handles := map[string]bool{}
	for p.i < len(p.lines) && strings.HasPrefix(p.lines[p.i].text, "%") {
		l := p.lines[p.i]
		if l.indent != 0 {
			return "", false, errAt(l.start, "directives must start at column zero")
		}
		text := l.text
		if i := strings.IndexByte(text, '#'); i > 0 && (text[i-1] == ' ' || text[i-1] == '\t') {
			text = text[:i]
		}
		fields := strings.FieldsFunc(text, func(r rune) bool { return r == ' ' || r == '\t' })
		switch fields[0] {
		case "%YAML":
			if hadYAML {
				return "", false, errAt(l.start, "duplicate YAML directive")
			}
			if len(fields) != 2 {
				return "", false, errAt(l.start, "invalid YAML directive")
			}
			major, minor, ok := strings.Cut(fields[1], ".")
			if !ok || major != "1" || minor == "" || strings.Trim(minor, "0123456789") != "" {
				return "", false, errAt(l.start, "unsupported YAML version")
			}
			version, hadYAML = fields[1], true
		case "%TAG":
			if len(fields) != 3 {
				return "", false, errAt(l.start, "invalid TAG directive")
			}
			handle := fields[1]
			if !validTagHandle(handle) {
				return "", false, errAt(l.start, "invalid tag handle")
			}
			prefixStart := l.start + strings.LastIndex(text, fields[2])
			if _, err := decodeTagURI(fields[2], prefixStart); err != nil {
				return "", false, err
			}
			if handles[handle] {
				return "", false, errAt(l.start, "duplicate tag directive")
			}
			handles[handle] = true
			p.tagHandles[handle] = fields[2]
		default:
			if len(fields[0]) == 1 {
				return "", false, errAt(l.start, "empty directive name")
			}
		}
		hadDirective = true
		p.i++
		p.skip()
	}
	return version, hadDirective, nil
}

func (p *parser) endMarker() error {
	l := p.lines[p.i]
	rest := strings.TrimLeft(l.text[3:], " \t")
	if rest != "" && rest[0] != '#' {
		return errAt(l.start+3, "unexpected content after document end marker")
	}
	p.i++
	return nil
}

func emptyScalar(a *nodeArena, pos int) *syntax.Node {
	return a.node(syntax.Node{Kind: syntax.Null, Start: pos, End: pos, Style: "plain", Meta: map[string]any{"scalarValue": ""}})
}

func marker(l line, s string) bool {
	if l.inlineRoot || l.indent != 0 || !strings.HasPrefix(l.text, s) {
		return false
	}
	rest := l.text[len(s):]
	if rest == "" {
		return true
	}
	if rest[0] != ' ' && rest[0] != '\t' {
		return false
	}
	return true
}
func (p *parser) skip() {
	for p.i < len(p.lines) && (strings.Trim(p.lines[p.i].text, " \t") == "" || strings.HasPrefix(strings.TrimLeft(p.lines[p.i].text, " \t"), "#")) {
		p.i++
	}
}
func explicitKey(s string) bool {
	return s == "?" || strings.HasPrefix(s, "? ") || strings.HasPrefix(s, "?\t")
}
func seq(s string) bool { return s == "-" || strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "-\t") }
func colon(s string) int {
	start := 0
	for start < len(s) && (s[start] == '&' || s[start] == '!' || s[start] == '*') {
		for start < len(s) && s[start] != ' ' && s[start] != '\t' {
			start++
		}
		for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
			start++
		}
	}
	quote := byte(0)
	depth := 0
	for i := start; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if len(s)-i >= 32 {
				escape := quote
				if quote == '"' {
					escape = '\\'
				}
				i += scan.QuotedString(s[i:], quote, escape)
				if i == len(s) {
					break
				}
				c = s[i]
			}
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"':
			if i == start || depth > 0 && (i == start || strings.ContainsRune("[{: ,\t", rune(s[i-1]))) {
				quote = c
			}
		case '[', '{':
			if i == start || depth > 0 {
				depth++
			}
		case ']', '}':
			if depth > 0 {
				depth--
			}
		case '#':
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return -1
			}
		case ':':
			if depth == 0 && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t') {
				return i
			}
		}
	}
	return -1
}
func (p *parser) block(indent, parentIndent int) (*syntax.Node, error) {
	p.propertyDepth++
	defer func() { p.propertyDepth-- }()
	if p.propertyDepth > 256 {
		return nil, errAt(p.lines[p.i].start, "property nesting limit exceeded")
	}
	l := p.lines[p.i]
	text := strings.TrimLeft(l.text, " \t")
	if strings.HasPrefix(l.text, "\t") && (seq(text) || explicitKey(text) || colon(text) >= 0) {
		return nil, errAt(l.start+l.indent, "tab indentation is not allowed")
	}
	if seq(text) {
		return p.sequence(indent)
	}
	if explicitKey(text) || colon(text) >= 0 {
		return p.mapping(indent, -1)
	}
	p.i++
	start := l.start + l.indent
	for start < l.end && (p.src[start] == ' ' || p.src[start] == '\t') {
		start++
	}
	n, e := p.value(start, l.end, parentIndent)
	if e != nil {
		return nil, e
	}
	anchor, hasAnchor := pending(n)
	tag, hasTag := pendingTag(n)
	if hasAnchor || hasTag {
		save := p.i
		p.skip()
		if p.i < len(p.lines) && (p.lines[p.i].indent > parentIndent || p.lines[p.i].indent == parentIndent && seq(p.lines[p.i].text)) && !marker(p.lines[p.i], "---") && !marker(p.lines[p.i], "...") {
			n, e = p.block(p.lines[p.i].indent, parentIndent)
			if e != nil {
				return nil, e
			}
		} else {
			p.i = save
		}
		if hasTag {
			if e = p.applyTag(n, tag); e != nil {
				return nil, e
			}
		}
		if hasAnchor {
			if n, e = p.bind(n, anchor); e != nil {
				return nil, e
			}
		}
	}
	p.skip()
	if p.i < len(p.lines) && p.lines[p.i].indent > indent {
		return nil, errAt(p.lines[p.i].start, "unexpected indented content")
	}
	return n, nil
}
func (p *parser) mapping(indent, first int) (*syntax.Node, error) {
	propertyDepth := p.propertyDepth
	p.propertyDepth = 0
	defer func() { p.propertyDepth = propertyDepth }()
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 256 {
		return nil, errAt(p.lines[p.i].start, "nesting limit exceeded")
	}
	n := p.arena.node(syntax.Node{Kind: syntax.Object, Style: "block", Meta: map[string]any{"indent": indent}})
	n.Start = -1
	for {
		p.skip()
		if p.i >= len(p.lines) {
			break
		}
		l := p.lines[p.i]
		if strings.HasPrefix(l.text, "\t") {
			return nil, errAt(l.start+l.indent, "tab indentation is not allowed")
		}
		start := l.start + l.indent
		if first >= 0 {
			start = first
			first = -1
		} else if l.indent != indent || seq(l.text) || marker(l, "---") || marker(l, "...") {
			break
		}
		entryStart := start
		keyStart, c := start, -1
		var keyEnd int
		var key string
		var keyNode *syntax.Node
		explicit := explicitKey(string(p.src[start:l.end]))
		if explicit {
			var err error
			keyNode, err = p.explicitNodeKey(start, l.end, indent)
			if err != nil {
				return nil, err
			}
			key, keyStart, keyEnd = keySpelling(keyNode), keyNode.Start, keyNode.End
		} else {
			s := string(p.src[start:l.end])
			c = colon(s)
			if c < 0 {
				return nil, errAt(start, "expected a mapping key followed by ':'")
			}
			keyEnd = start + c
			for keyEnd > start && (p.src[keyEnd-1] == ' ' || p.src[keyEnd-1] == '\t') {
				keyEnd--
			}
			var err error
			if keyEnd == start {
				keyNode = emptyScalar(&p.arena, start)
			} else if strings.ContainsRune("'\"&*![{", rune(p.src[start])) {
				keyParser := &flow{p: p, pos: start, limit: keyEnd, depth: p.depth, minIndent: indent + 1}
				keyNode, err = keyParser.node(false)
				if err != nil {
					return nil, err
				}
				if keyParser.pos != keyEnd {
					return nil, errAt(keyParser.pos, "unexpected mapping key content")
				}
			} else {
				if strings.ContainsRune("@`%|>", rune(p.src[start])) {
					return nil, errAt(start, "invalid mapping key indicator")
				}
				keyNode, err = scalar(&p.arena, string(p.src[start:keyEnd]), start, keyEnd)
				if err != nil {
					return nil, err
				}
			}
			key = keySpelling(keyNode)
		}

		var v *syntax.Node
		var e error
		if explicit {
			p.skip()
			colonPosition, inlineValue := keyNode.Meta["explicitColon"].(int)
			if inlineValue {
				p.i = sort.Search(len(p.lines), func(i int) bool { return p.lines[i].end >= colonPosition })
			}
			if inlineValue || p.i < len(p.lines) && p.lines[p.i].indent == indent && (p.lines[p.i].text == ":" || strings.HasPrefix(p.lines[p.i].text, ": ") || strings.HasPrefix(p.lines[p.i].text, ":\t")) {
				valueLine := p.lines[p.i]
				pos := valueLine.start + indent + 1
				if inlineValue {
					pos = colonPosition + 1
				}
				for pos < valueLine.end && (p.src[pos] == ' ' || p.src[pos] == '\t') {
					pos++
				}
				if pos < valueLine.end && (seq(string(p.src[pos:valueLine.end])) || explicitKey(string(p.src[pos:valueLine.end])) || colon(string(p.src[pos:valueLine.end])) >= 0) {
					v, e = p.compactBlock(pos, p.i, indent)
				} else {
					p.i++
					v, e = p.value(pos, valueLine.end, indent)
				}
			} else {
				position := p.extentEnd(keyEnd, l.next)
				v = emptyScalar(&p.arena, position)
				v.Meta["explicitMissingValue"], v.Meta["indent"] = true, indent
			}
		} else {
			pos := start + c + 1
			for pos < l.end && (p.src[pos] == ' ' || p.src[pos] == '\t') {
				pos++
			}
			p.i++
			v, e = p.value(pos, l.end, indent)
		}
		if e != nil {
			return nil, e
		}
		anchor, hasAnchor := pending(v)
		tag, hasTag := pendingTag(v)
		if v.Kind == syntax.Null && v.Start == v.End {
			save := p.i
			p.skip()
			if p.i < len(p.lines) && (p.lines[p.i].indent > indent || (p.lines[p.i].indent == indent && seq(p.lines[p.i].text))) {
				v, e = p.block(p.lines[p.i].indent, indent)
				if e != nil {
					return nil, e
				}
				v.Meta = meta(v.Meta)
				v.Meta["nested"] = true
			} else {
				p.i = save
			}
		}
		if hasTag {
			if e = p.applyTag(v, tag); e != nil {
				return nil, e
			}
		}
		if hasAnchor {
			if v, e = p.bind(v, anchor); e != nil {
				return nil, e
			}
		}
		end := v.End
		entry := p.arena.entry(syntax.Entry{Key: key, KeyStart: keyStart, KeyEnd: keyEnd, Start: entryStart, End: end, Value: v, Meta: map[string]any{"lineStart": l.start, "lineEnd": l.next, "headerEnd": l.end}})
		if keyNode != nil {
			entry.Meta["keyNode"] = keyNode
		}
		entry.Meta["deleteEnd"] = p.extentEnd(end, l.next)
		n.Entries = append(n.Entries, entry)
		if n.Start < 0 {
			n.Start = start
		}
		n.End = end
	}
	if len(n.Entries) == 0 {
		return nil, errAt(0, "empty block mapping")
	}
	return n, nil
}
func (p *parser) explicitNodeKey(start, end, indent int) (*syntax.Node, error) {
	pos := start + 1
	for pos < end && (p.src[pos] == ' ' || p.src[pos] == '\t') {
		pos++
	}
	var n *syntax.Node
	var err error

	if pos < end && (seq(string(p.src[pos:end])) || explicitKey(string(p.src[pos:end])) || colon(string(p.src[pos:end])) >= 0) {
		return p.compactBlock(pos, p.i, indent)
	}
	if pos < end && strings.ContainsRune("[{\"'", rune(p.src[pos])) {
		f := &flow{p: p, pos: pos, limit: len(p.src), depth: p.depth, minIndent: indent + 1}
		n, err = f.node(false)
		if err != nil {
			return nil, err
		}
		rest := f.pos
		for rest < len(p.src) && (p.src[rest] == ' ' || p.src[rest] == '\t') {
			rest++
		}
		if rest < len(p.src) && p.src[rest] == ':' && (rest+1 == len(p.src) || strings.ContainsRune(" \t\r\n", rune(p.src[rest+1]))) {
			n.Meta = meta(n.Meta)
			n.Meta["explicitColon"] = rest
		}
		for p.i < len(p.lines) && p.lines[p.i].start < n.End {
			p.i++
		}
	} else {
		p.i++
		n, err = p.value(pos, end, indent)
		if err != nil {
			return nil, err
		}
	}
	anchor, hasAnchor := pending(n)
	tag, hasTag := pendingTag(n)
	if n.Kind == syntax.Null && n.Start == n.End {
		p.skip()
		if p.i < len(p.lines) && (p.lines[p.i].indent > indent || p.lines[p.i].indent == indent && seq(p.lines[p.i].text)) {
			n, err = p.block(p.lines[p.i].indent, indent)
			if err != nil {
				return nil, err
			}
		}
	}
	if hasTag {
		if err := p.applyTag(n, tag); err != nil {
			return nil, err
		}
	}
	if hasAnchor {
		n, err = p.bind(n, anchor)
		if err != nil {
			return nil, err
		}
	}
	return n, nil
}

func (p *parser) compactBlock(start, index, parentIndent int) (*syntax.Node, error) {
	original := p.lines[index]
	separation := start
	for separation > original.start && (p.src[separation-1] == ' ' || p.src[separation-1] == '\t') {
		separation--
	}
	if strings.ContainsRune(string(p.src[separation:start]), '\t') {
		return nil, errAt(separation, "tab indentation is not allowed")
	}
	current := original
	current.indent, current.text = start-original.start, string(p.src[start:original.end])
	p.lines[index] = current
	n, err := p.block(current.indent, parentIndent)
	p.lines[index] = original
	if err == nil {
		n.Meta = meta(n.Meta)
		n.Meta["compact"] = true
	}
	return n, err
}

func keySpelling(n *syntax.Node) string {
	if value, ok := n.Meta["scalarValue"].(string); ok {
		return value
	}
	return ""
}

func meta(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
func (p *parser) extentEnd(end, fallback int) int {
	if end > 0 && end <= len(p.src) && (p.src[end-1] == '\n' || p.src[end-1] == '\r') {
		return end
	}
	i := sort.Search(len(p.lines), func(i int) bool { return p.lines[i].end >= end })
	if i < len(p.lines) {
		l := p.lines[i]
		if l.start <= end {
			return l.next
		}
	}
	if end > fallback {
		return end
	}
	return fallback
}
func (p *parser) sequence(indent int) (*syntax.Node, error) {
	propertyDepth := p.propertyDepth
	p.propertyDepth = 0
	defer func() { p.propertyDepth = propertyDepth }()
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 256 {
		return nil, errAt(p.lines[p.i].start, "nesting limit exceeded")
	}
	n := p.arena.node(syntax.Node{Kind: syntax.Array, Style: "block", Start: p.lines[p.i].start + indent, Meta: map[string]any{"indent": indent}})
	for {
		p.skip()
		if p.i >= len(p.lines) {
			break
		}
		l := p.lines[p.i]
		if l.indent != indent || !seq(l.text) {
			break
		}
		pos := l.start + indent + 1
		for pos < l.end && (p.src[pos] == ' ' || p.src[pos] == '\t') {
			pos++
		}
		var v *syntax.Node
		var e error
		if pos < l.end && (seq(string(p.src[pos:l.end])) || explicitKey(string(p.src[pos:l.end])) || colon(string(p.src[pos:l.end])) >= 0) {
			v, e = p.compactBlock(pos, p.i, indent)
		} else {
			p.i++
			v, e = p.value(pos, l.end, indent)
			var anchor anchorInfo
			var tag tagInfo
			hasTag := false
			hasAnchor := false
			if e == nil {
				anchor, hasAnchor = pending(v)
				tag, hasTag = pendingTag(v)
			}
			if e == nil && v.Kind == syntax.Null && v.Start == v.End {
				save := p.i
				p.skip()
				if p.i < len(p.lines) && p.lines[p.i].indent > indent {
					v, e = p.block(p.lines[p.i].indent, indent)
					if e == nil {
						v.Meta = meta(v.Meta)
						v.Meta["nested"] = true
					}
				} else {
					p.i = save
				}
			}
			if e == nil && hasTag {
				e = p.applyTag(v, tag)
			}
			if e == nil && hasAnchor {
				v, e = p.bind(v, anchor)
			}
		}
		if e != nil {
			return nil, e
		}
		v.Meta = meta(v.Meta)
		v.Meta["itemStart"] = l.start
		v.Meta["itemEnd"] = p.extentEnd(v.End, l.next)
		n.Items = append(n.Items, v)
		n.End = v.End
	}
	return n, nil
}
func (p *parser) value(start, end, indent int) (*syntax.Node, error) {
	p.propertyDepth++
	defer func() { p.propertyDepth-- }()
	if p.propertyDepth > 256 {
		return nil, errAt(start, "property nesting limit exceeded")
	}
	if start >= end || p.src[start] == '#' {
		return emptyScalar(&p.arena, start), nil
	}
	if p.src[start] == '&' {
		a, next, e := property(p.src, start, end)
		if e != nil {
			return nil, e
		}
		a.target = p.beginAnchor(a)
		for next < end && (p.src[next] == ' ' || p.src[next] == '\t') {
			next++
		}
		if next == end || p.src[next] == '#' {
			return p.arena.node(syntax.Node{Kind: syntax.Null, Start: next, End: next, Meta: map[string]any{"pendingAnchor": a, "scalarValue": ""}}), nil
		}
		n, e := p.value(next, end, indent)
		if e != nil {
			return nil, e
		}
		if _, ok := pendingTag(n); ok {
			if _, exists := pending(n); exists {
				return nil, errAt(a.start, "duplicate anchor properties")
			}
			n.Meta["pendingAnchor"] = a
			return n, nil
		}
		if n, e = p.bind(n, a); e != nil {
			return nil, e
		}
		return n, nil
	}
	if p.src[start] == '!' {
		tag, next, e := p.tagProperty(p.src, start, end)
		if e != nil {
			return nil, e
		}
		for next < end && (p.src[next] == ' ' || p.src[next] == '\t') {
			next++
		}
		if next == end || p.src[next] == '#' {
			return p.arena.node(syntax.Node{Kind: syntax.Null, Start: next, End: next, Meta: map[string]any{"pendingTag": tag, "scalarValue": ""}}), nil
		}
		n, e := p.value(next, end, indent)
		if e != nil {
			return nil, e
		}
		if _, ok := pending(n); ok {
			if _, exists := pendingTag(n); exists {
				return nil, errAt(tag.start, "duplicate tag properties")
			}
			n.Meta["pendingTag"] = tag
			return n, nil
		}
		if e = p.applyTag(n, tag); e != nil {
			return nil, e
		}
		return n, nil
	}
	if p.src[start] == '|' || p.src[start] == '>' {
		return p.blockScalar(start, end, indent)
	}
	n, e := p.inline(start, end, false, indent)
	if e != nil {
		return nil, e
	}
	for p.i < len(p.lines) && p.lines[p.i].start < n.End {
		p.i++
	}
	if n.Style == "plain" {
		segments := []segment{{n.Start, n.End}}
		var value strings.Builder
		value.WriteString(string(p.src[n.Start:n.End]))
		continued := false
		for {
			// An inline comment ends a plain scalar; later indented text cannot
			// resume it after that comment.
			lineEnd := n.End
			for lineEnd < len(p.src) && p.src[lineEnd] != '\r' && p.src[lineEnd] != '\n' {
				if p.src[lineEnd] == '#' {
					break
				}
				lineEnd++
			}
			if lineEnd < len(p.src) && p.src[lineEnd] == '#' {
				break
			}
			j := p.i
			blanks := 0
			for j < len(p.lines) && strings.Trim(p.lines[j].text, " \t") == "" {
				blanks++
				j++
			}
			if j < len(p.lines) && strings.HasPrefix(p.lines[j].text, "#") {
				break
			}
			if j >= len(p.lines) || p.lines[j].indent <= indent || marker(p.lines[j], "---") || marker(p.lines[j], "...") {
				break
			}
			l := p.lines[j]
			if colon(l.text) >= 0 {
				return nil, errAt(l.start, "invalid plain scalar continuation")
			}
			a, b := l.start+l.indent, l.end
			for a < b && (p.src[a] == ' ' || p.src[a] == '\t') {
				a++
			}
			for k := a; k < b; k++ {
				if p.src[k] == '#' && (k == a || p.src[k-1] == ' ' || p.src[k-1] == '\t') {
					b = k
					break
				}
			}
			for b > a && (p.src[b-1] == ' ' || p.src[b-1] == '\t') {
				b--
			}
			if a == b {
				break
			}
			if blanks == 0 {
				value.WriteString(" ")
			} else {
				value.WriteString(strings.Repeat("\n", blanks))
			}
			value.WriteString(string(p.src[a:b]))
			segments = append(segments, segment{a, b})
			n.End = b
			p.i = j + 1
			continued = true
		}
		if continued {
			text := value.String()
			n.Kind = syntax.String
			n.Value = text
			n.Meta = meta(n.Meta)
			n.Meta["scalarValue"] = text
			n.Style = "multiline-plain"
			n.Meta = meta(n.Meta)
			n.Meta["segments"] = segments
		}
	}
	return n, nil
}
