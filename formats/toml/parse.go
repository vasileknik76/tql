// Package toml implements source-aware TOML 1.1 parsing and edit planning.
package toml

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vasileknik76/tql/internal/scan"
	"github.com/vasileknik76/tql/syntax"
)

// Format parses and edits TOML documents without reformatting untouched source.
type Format struct{ style syntax.Style }

// WithStyle returns an independent backend with an explicit generation policy.
func (f Format) WithStyle(style syntax.Style) (syntax.Format, error) {
	if err := style.Validate("toml"); err != nil {
		return nil, err
	}
	f.style = style
	return f, nil
}

var _ syntax.Format = Format{}

type parser struct {
	arena         nodeArena
	src           []byte
	pos           int
	root, current *syntax.Node
	depth         int
	entries       map[*syntax.Node]map[string]*syntax.Node
}

func (p *parser) object(style string) *syntax.Node {
	return p.arena.node(syntax.Node{Kind: syntax.Object, Start: -1, End: -1, Style: style, Meta: map[string]any{}})
}
func (p *parser) err(s string) error {
	line, col := 1, 1
	for _, c := range p.src[:p.pos] {
		if c == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return fmt.Errorf("toml:%d:%d: %s", line, col, s)
}
func (p *parser) peek() byte {
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}
func (p *parser) spaces() {
	for p.peek() == ' ' || p.peek() == '\t' {
		p.pos++
	}
}
func (p *parser) newline() bool {
	if p.peek() == '\n' {
		p.pos++
		return true
	}
	if p.peek() == '\r' && p.pos+1 < len(p.src) && p.src[p.pos+1] == '\n' {
		p.pos += 2
		return true
	}
	return false
}
func (p *parser) comment() error {
	if p.peek() != '#' {
		return nil
	}
	for p.pos < len(p.src) && p.peek() != '\n' && p.peek() != '\r' {
		start := p.pos
		p.pos += scan.QuotedScalar(p.src[p.pos:min(p.pos+16, len(p.src))], 0x7f, 0x7f)
		if p.pos-start == 16 {
			p.pos += scan.Quoted(p.src[p.pos:], 0x7f, 0x7f)
		}
		if p.pos == len(p.src) || p.peek() == '\n' || p.peek() == '\r' {
			break
		}
		c := p.peek()
		if c < 0x20 && c != '\t' || c == 0x7f {
			return p.err("control character in comment")
		}
		p.pos++
	}
	return nil
}
func (p *parser) trivia() error {
	for {
		p.spaces()
		if err := p.comment(); err != nil {
			return err
		}
		if !p.newline() {
			return nil
		}
	}
}

// Parse reads one document and returns a linked, source-aware syntax tree.
// It does not modify source.
func (Format) Parse(src []byte) (*syntax.Node, error) {
	if !utf8.Valid(src) {
		return nil, fmt.Errorf("toml: invalid UTF-8")
	}
	p := &parser{src: src, entries: make(map[*syntax.Node]map[string]*syntax.Node)}
	p.root = p.object("table")
	p.current = p.root
	for {
		if err := p.trivia(); err != nil {
			return nil, err
		}
		if p.pos == len(src) {
			break
		}
		start := p.pos
		if p.peek() == '[' {
			if err := p.header(); err != nil {
				return nil, err
			}
		} else {
			keys, ks, ke, err := p.keypath()
			if err != nil {
				return nil, err
			}
			p.spaces()
			if p.peek() != '=' {
				return nil, p.err("expected =")
			}
			p.pos++
			p.spaces()
			v, err := p.value()
			if err != nil {
				return nil, err
			}
			end := p.pos
			if err = p.assign(p.current, keys, v, ks, ke, start, end); err != nil {
				return nil, err
			}
		}
		p.spaces()
		if err := p.comment(); err != nil {
			return nil, err
		}
		if p.pos < len(src) && !p.newline() {
			return nil, p.err("expected end of line")
		}
	}
	syntax.Link(p.root)
	return p.root, nil
}
func (p *parser) keypath() ([]string, int, int, error) {
	start := p.pos
	var keys []string
	for {
		p.spaces()
		var key string
		if p.peek() == '"' || p.peek() == '\'' {
			if p.pos+2 < len(p.src) && p.src[p.pos+1] == p.peek() && p.src[p.pos+2] == p.peek() {
				return nil, 0, 0, p.err("multiline key")
			}
			n, e := p.stringValue()
			if e != nil {
				return nil, 0, 0, e
			}
			key = n.Value.(string)
		} else {
			s := p.pos
			for {
				c := p.peek()
				if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
					p.pos++
				} else {
					break
				}
			}
			if s == p.pos {
				return nil, 0, 0, p.err("expected key")
			}
			key = string(p.src[s:p.pos])
		}
		if len(keys) >= 256 {
			return nil, 0, 0, p.err("key nesting exceeds 256 levels")
		}
		keys = append(keys, key)
		p.spaces()
		if p.peek() != '.' {
			break
		}
		p.pos++
	}
	return keys, start, p.pos, nil
}
func (p *parser) addEntry(n *syntax.Node, key string, v *syntax.Node, ks, ke, start, end int) {
	e := p.arena.entry(syntax.Entry{Key: key, Value: v, KeyStart: ks, KeyEnd: ke, Start: start, End: end})
	n.Entries = append(n.Entries, e)
	if p.entries[n] == nil {
		p.entries[n] = make(map[string]*syntax.Node)
	}
	p.entries[n][key] = v
	v.Parent = n
}
func (p *parser) assign(n *syntax.Node, keys []string, v *syntax.Node, ks, ke, start, end int) error {
	for _, k := range keys[:len(keys)-1] {
		next := p.entries[n][k]
		if next == nil {
			next = p.object("table")
			next.Meta["dotted"] = true
			p.addEntry(n, k, next, ks, ke, -1, -1)
		} else if next.Kind != syntax.Object || next.Style == "inline" || next.Meta["explicit"] == true {
			return p.err("key traverses a value")
		}
		// Traversing an implicit header-created supertable with a dotted key
		// defines that table too, so a later explicit header cannot redefine it.
		next.Meta["dotted"] = true
		n = next
	}
	k := keys[len(keys)-1]
	if p.entries[n][k] != nil {
		return p.err("duplicate key " + k)
	}
	p.addEntry(n, k, v, ks, ke, start, end)
	return nil
}
func (p *parser) header() error {
	start := p.pos
	p.pos++
	array := p.peek() == '['
	if array {
		p.pos++
	}
	p.spaces()
	keys, _, _, err := p.keypath()
	if err != nil {
		return err
	}
	p.spaces()
	if p.peek() != ']' {
		return p.err("expected ]")
	}
	p.pos++
	if array {
		if p.peek() != ']' {
			return p.err("expected ]]")
		}
		p.pos++
	}
	end := p.pos
	n := p.root
	for i, k := range keys {
		last := i == len(keys)-1
		child := p.entries[n][k]
		if last && array {
			if child == nil {
				child = p.arena.node(syntax.Node{Kind: syntax.Array, Start: -1, End: -1, Style: "tables", Meta: map[string]any{}})
				p.addEntry(n, k, child, -1, -1, -1, -1)
			}
			if child.Kind != syntax.Array || child.Style != "tables" {
				return p.err("array table conflicts with existing value")
			}
			item := p.object("table")
			item.Meta["explicit"] = true
			item.Meta["headerStart"] = start
			item.Meta["headerEnd"] = end
			child.Items = append(child.Items, item)
			item.Parent = child
			n = item
			continue
		}
		if child == nil {
			child = p.object("table")
			p.addEntry(n, k, child, -1, -1, -1, -1)
		}
		if child.Kind == syntax.Array && child.Style == "tables" {
			if len(child.Items) == 0 {
				return p.err("empty array table")
			}
			child = child.Items[len(child.Items)-1]
		} else if child.Kind != syntax.Object || child.Style == "inline" {
			return p.err("table conflicts with existing value")
		}
		if last {
			if child.Meta["explicit"] == true || child.Meta["dotted"] == true {
				return p.err("duplicate table")
			}
			child.Meta["explicit"] = true
			child.Meta["headerStart"] = start
			child.Meta["headerEnd"] = end
		}
		n = child
	}
	p.current = n
	return nil
}
func (p *parser) value() (*syntax.Node, error) {
	p.depth++
	defer func() { p.depth-- }()
	if p.depth > 256 {
		return nil, p.err("value nesting exceeds 256 levels")
	}
	switch p.peek() {
	case '"', '\'':
		return p.stringValue()
	case '[':
		return p.array()
	case '{':
		return p.inline()
	default:
		return p.atom()
	}
}
func (p *parser) stringValue() (*syntax.Node, error) {
	start := p.pos
	q := p.peek()
	p.pos++
	multi := p.pos+1 < len(p.src) && p.src[p.pos] == q && p.src[p.pos+1] == q
	if multi {
		p.pos += 2
		p.newline()
	}
	content := p.pos
	escape := q
	if q == '"' {
		escape = '\\'
	}
	p.pos += scan.QuotedScalar(p.src[p.pos:min(p.pos+16, len(p.src))], q, escape)
	if p.pos-content == 16 {
		p.pos += scan.Quoted(p.src[p.pos:], q, escape)
	}
	if !multi && p.pos < len(p.src) && p.src[p.pos] == q {
		value := string(p.src[content:p.pos])
		p.pos++
		return p.arena.node(syntax.Node{Kind: syntax.String, Value: value, Start: start, End: p.pos}), nil
	}
	var b strings.Builder
	b.Write(p.src[content:p.pos])
	for p.pos < len(p.src) {
		c := p.peek()
		if c == q {
			run := 0
			for p.pos+run < len(p.src) && p.src[p.pos+run] == q {
				run++
			}
			if !multi {
				p.pos++
				return p.arena.node(syntax.Node{Kind: syntax.String, Value: b.String(), Start: start, End: p.pos}), nil
			}
			if run >= 3 {
				if run > 5 {
					return nil, p.err("too many closing quotes")
				}
				for j := 0; j < run-3; j++ {
					b.WriteByte(q)
				}
				p.pos += run
				return p.arena.node(syntax.Node{Kind: syntax.String, Value: b.String(), Start: start, End: p.pos}), nil
			}
		}
		if c == '\n' || c == '\r' {
			if !multi {
				return nil, p.err("newline in single-line string")
			}
			if !p.newline() {
				return nil, p.err("bare carriage return")
			}
			b.WriteByte('\n')
			// Copy ordinary text after a line break as one run. Dense escapes
			// in single-line strings keep the original byte decoder.
			end := p.pos + scan.QuotedScalar(p.src[p.pos:min(p.pos+16, len(p.src))], q, escape)
			if end-p.pos == 16 && end < len(p.src) && p.src[end] >= 0x20 {
				end += scan.Quoted(p.src[end:], q, escape)
			}
			b.Write(p.src[p.pos:end])
			p.pos = end
			continue
		}
		if c < 0x20 && c != '\t' || c == 0x7f {
			return nil, p.err("control character in string")
		}
		if c == '\\' && q == '"' {
			p.pos++
			if p.pos == len(p.src) {
				return nil, p.err("incomplete escape")
			}
			if multi && (p.peek() == ' ' || p.peek() == '\t' || p.peek() == '\n' || p.peek() == '\r') {
				p.spaces()
				if !p.newline() {
					return nil, p.err("escaped whitespace must end with newline")
				}
				for {
					p.spaces()
					if !p.newline() {
						break
					}
				}
				continue
			}
			e := p.peek()
			p.pos++
			switch e {
			case 'b':
				b.WriteByte('\b')
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'f':
				b.WriteByte('\f')
			case 'r':
				b.WriteByte('\r')
			case 'e':
				b.WriteByte(0x1b)
			case '"', '\\':
				b.WriteByte(e)
			case 'x', 'u', 'U':
				count := 4
				switch e {
				case 'x':
					count = 2
				case 'U':
					count = 8
				}
				if p.pos+count > len(p.src) {
					return nil, p.err("incomplete Unicode escape")
				}
				hex := string(p.src[p.pos : p.pos+count])
				for _, h := range hex {
					if (h < '0' || h > '9') && (h < 'a' || h > 'f') && (h < 'A' || h > 'F') {
						return nil, p.err("invalid Unicode escape")
					}
				}
				v, er := strconv.ParseUint(hex, 16, 32)
				if er != nil || v > utf8.MaxRune || v >= 0xd800 && v <= 0xdfff {
					return nil, p.err("invalid Unicode scalar")
				}
				b.WriteRune(rune(v))
				p.pos += count
			default:
				return nil, p.err("unknown escape")
			}
			continue
		}
		b.WriteByte(c)
		p.pos++
	}
	return nil, p.err("unterminated string")
}
func (p *parser) array() (*syntax.Node, error) {
	n := p.arena.node(syntax.Node{Kind: syntax.Array, Start: p.pos, Style: "array"})
	p.pos++
	if err := p.trivia(); err != nil {
		return nil, err
	}
	if p.peek() == ']' {
		p.pos++
		n.End = p.pos
		return n, nil
	}
	for {
		v, e := p.value()
		if e != nil {
			return nil, e
		}
		v.Parent = n
		n.Items = append(n.Items, v)
		if e = p.trivia(); e != nil {
			return nil, e
		}
		if p.peek() == ']' {
			p.pos++
			n.End = p.pos
			return n, nil
		}
		if p.peek() != ',' {
			return nil, p.err("expected comma or ]")
		}
		p.pos++
		if e = p.trivia(); e != nil {
			return nil, e
		}
		if p.peek() == ']' {
			p.pos++
			n.End = p.pos
			return n, nil
		}
	}
}
func (p *parser) inline() (*syntax.Node, error) {
	n := p.object("inline")
	n.Start = p.pos
	p.pos++
	if err := p.trivia(); err != nil {
		return nil, err
	}
	if p.peek() == '}' {
		p.pos++
		n.End = p.pos
		return n, nil
	}
	for {
		start := p.pos
		keys, ks, ke, e := p.keypath()
		if e != nil {
			return nil, e
		}
		if p.peek() != '=' {
			return nil, p.err("expected =")
		}
		p.pos++
		p.spaces()
		v, e := p.value()
		if e != nil {
			return nil, e
		}
		if e = p.assign(n, keys, v, ks, ke, start, p.pos); e != nil {
			return nil, e
		}
		if err := p.trivia(); err != nil {
			return nil, err
		}
		if p.peek() == '}' {
			p.pos++
			n.End = p.pos
			return n, nil
		}
		if p.peek() != ',' {
			return nil, p.err("expected comma or }")
		}
		p.pos++
		if err := p.trivia(); err != nil {
			return nil, err
		}
		if p.peek() == '}' {
			p.pos++
			n.End = p.pos
			return n, nil
		}
	}
}

var decimalInt = regexp.MustCompile(`^[+-]?(0|[1-9](_?[0-9])*)$`)
var baseInt = regexp.MustCompile(`^0(x[0-9a-fA-F](_?[0-9a-fA-F])*|o[0-7](_?[0-7])*|b[01](_?[01])*)$`)
var floatRE = regexp.MustCompile(`^[+-]?(0|[1-9](_?[0-9])*)(\.[0-9](_?[0-9])*)?([eE][+-]?[0-9](_?[0-9])*)?$`)
var temporalRE = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}([Tt ]\d{2}:\d{2}(:\d{2}(\.[0-9]+)?)?([Zz]|[+-]\d{2}:\d{2})?)?|\d{2}:\d{2}(:\d{2}(\.[0-9]+)?)?)$`)

func validTime(s string) bool {
	if len(s) >= 6 && (s[len(s)-6] == '-' || s[len(s)-6] == '+') && s[len(s)-3] == ':' {
		zone := s[len(s)-5:]
		if zone[:2] > "23" || zone[3:] > "59" {
			return false
		}
	}
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(s, "t", "T"), "z", "Z"), " ", "T")
	clock := 0
	if len(s) > 10 && s[10] == 'T' {
		clock = 11
	}
	if len(s) >= clock+5 && s[clock+2] == ':' && (len(s) == clock+5 || s[clock+5] != ':') {
		s = s[:clock+5] + ":00" + s[clock+5:]
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02", "15:04:05.999999999"} {
		if _, e := time.Parse(layout, s); e == nil {
			return true
		}
	}
	return false
}
func (p *parser) atom() (*syntax.Node, error) {
	start := p.pos
	for p.pos < len(p.src) {
		c := p.peek()
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '#' || c == ',' || c == ']' || c == '}' {
			break
		}
		p.pos++
	}
	if p.pos == start {
		return nil, p.err("expected value")
	}
	s := string(p.src[start:p.pos])
	if len(s) == 10 && s[4] == '-' && p.peek() == ' ' && p.pos+1 < len(p.src) && p.src[p.pos+1] >= '0' && p.src[p.pos+1] <= '9' {
		p.pos++
		for p.pos < len(p.src) {
			c := p.peek()
			if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '#' || c == ',' || c == ']' || c == '}' {
				break
			}
			p.pos++
		}
		s = string(p.src[start:p.pos])
	}
	n := p.arena.node(syntax.Node{Start: start, End: p.pos})
	switch {
	case s == "true" || s == "false":
		n.Kind = syntax.Bool
		n.Value = s == "true"
	case temporalRE.MatchString(s):
		if !validTime(s) {
			return nil, p.err("invalid date/time")
		}
		n.Kind = syntax.Datetime
		n.Value = syntax.Temporal(s)
	case decimalInt.MatchString(s):
		clean := strings.ReplaceAll(s, "_", "")
		v, e := strconv.ParseInt(clean, 10, 64)
		if e != nil {
			return nil, p.err("integer out of range")
		}
		n.Kind = syntax.NumberKind
		n.Value = syntax.Number(strconv.FormatInt(v, 10))
	case baseInt.MatchString(s):
		v, e := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 0, 64)
		if e != nil {
			return nil, p.err("integer out of range")
		}
		n.Kind = syntax.NumberKind
		n.Value = syntax.Number(strconv.FormatInt(v, 10))
	case s == "inf" || s == "+inf" || s == "-inf" || s == "nan" || s == "+nan" || s == "-nan":
		n.Kind = syntax.NumberKind
		n.Value = syntax.Number(s)
	case floatRE.MatchString(s) && strings.ContainsAny(s, ".eE"):
		clean := strings.ReplaceAll(s, "_", "")
		if _, e := strconv.ParseFloat(clean, 64); e != nil {
			return nil, p.err("float out of range")
		}
		n.Kind = syntax.NumberKind
		n.Value = syntax.Number(strings.TrimPrefix(clean, "+"))
	default:
		return nil, p.err("invalid value " + s)
	}
	return n, nil
}
