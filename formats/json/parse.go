// Package json implements RFC 8259 JSON with byte-accurate source locations.
// Duplicate object keys and unpaired UTF-16 surrogates are rejected for
// unambiguous paths and valid Unicode strings.
package json

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/vasileknik76/tql/internal/scan"
	"github.com/vasileknik76/tql/syntax"
)

// Format parses and edits JSON documents without reformatting untouched source.
type Format struct{}

var _ syntax.Format = Format{}

type parser struct {
	source  []byte
	pos     int
	nodes   []syntax.Node
	entries []syntax.Entry
	owned   bool
}

// arenaSize bounds one block of nodes or entries. Trees keep individual elements
// alive, so blocks never need to be tracked separately.
const arenaSize = 1024

// nextBlock doubles a block up to arenaSize, so short parses stay small while
// large documents reach full blocks after a few allocations.
func nextBlock(current int) int {
	if current == 0 {
		return 16
	}
	return min(current*2, arenaSize)
}

// duplicateKeyMapThreshold is the entry count above which duplicate key checks
// switch from a linear scan to a map.
const duplicateKeyMapThreshold = 16

func (p *parser) newNode(start int) *syntax.Node {
	if len(p.nodes) == cap(p.nodes) {
		p.nodes = make([]syntax.Node, 0, nextBlock(cap(p.nodes)))
	}
	p.nodes = append(p.nodes, syntax.Node{Start: start})
	return &p.nodes[len(p.nodes)-1]
}

func (p *parser) newEntry() *syntax.Entry {
	if len(p.entries) == cap(p.entries) {
		p.entries = make([]syntax.Entry, 0, nextBlock(cap(p.entries)))
	}
	p.entries = append(p.entries, syntax.Entry{})
	return &p.entries[len(p.entries)-1]
}

// addKey rejects duplicate object keys. Small objects are scanned linearly;
// only wide objects pay for a lookup map, which is built once and reused.
func (p *parser) addKey(n *syntax.Node, key string, seen map[string]bool) (map[string]bool, error) {
	if len(n.Entries) < duplicateKeyMapThreshold {
		for _, entry := range n.Entries {
			if entry.Key == key {
				return seen, p.fail("duplicate object key " + strconv.Quote(key))
			}
		}
		return seen, nil
	}
	if seen == nil {
		seen = make(map[string]bool, len(n.Entries)+1)
		for _, entry := range n.Entries {
			seen[entry.Key] = true
		}
	}
	if seen[key] {
		return seen, p.fail("duplicate object key " + strconv.Quote(key))
	}
	seen[key] = true
	return seen, nil
}

// Parse reads one JSON value, surrounded only by JSON whitespace. Numbers retain
// their exact decimal spelling, including exponent and negative-zero notation.
func (Format) Parse(source []byte) (*syntax.Node, error) {
	return parseSource(source, false)
}

// ParseOwned parses a buffer that the caller keeps immutable for the lifetime of
// the tree. Decoded strings then reference the buffer instead of copying it.
func ParseOwned(source []byte) (*syntax.Node, error) {
	return parseSource(source, true)
}

func parseSource(source []byte, owned bool) (*syntax.Node, error) {
	p := &parser{source: source, owned: owned}
	if !utf8.Valid(source) {
		return nil, p.fail("invalid UTF-8")
	}
	p.space()
	n, err := p.value(0)
	if err != nil {
		return nil, err
	}
	p.space()
	if p.pos != len(source) {
		return nil, p.fail("unexpected content after JSON value")
	}
	syntax.Link(n)
	return n, nil
}

func (p *parser) fail(message string) error {
	line, column := 1, 1
	for _, b := range p.source[:p.pos] {
		if b == '\n' {
			line++
			column = 1
		} else {
			column++
		}
	}
	return fmt.Errorf("json: %s at line %d, column %d (byte %d)", message, line, column, p.pos)
}

func (p *parser) space() {
	for p.pos < len(p.source) {
		switch p.source[p.pos] {
		case ' ', '\t', '\r', '\n':
			p.pos++
		default:
			return
		}
	}
}

func (p *parser) value(depth int) (*syntax.Node, error) {
	if depth > 1024 {
		return nil, p.fail("nesting exceeds 1024 levels")
	}
	if p.pos == len(p.source) {
		return nil, p.fail("expected a value")
	}
	start := p.pos
	n := p.newNode(start)
	switch p.source[p.pos] {
	case '{':
		n.Kind = syntax.Object
		p.pos++
		p.space()
		var seen map[string]bool
		if p.take('}') {
			break
		}
		for {
			if p.pos == len(p.source) || p.source[p.pos] != '"' {
				return nil, p.fail("expected a quoted object key")
			}
			keyStart := p.pos
			key, err := p.string()
			if err != nil {
				return nil, err
			}
			keyEnd := p.pos
			seen, err = p.addKey(n, key, seen)
			if err != nil {
				return nil, err
			}
			p.space()
			if !p.take(':') {
				return nil, p.fail("expected ':' after object key")
			}
			p.space()
			child, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			entry := p.newEntry()
			entry.Key, entry.KeyStart, entry.KeyEnd = key, keyStart, keyEnd
			entry.Start, entry.End, entry.Value = keyStart, p.pos, child
			n.Entries = append(n.Entries, entry)
			p.space()
			if p.take('}') {
				break
			}
			if !p.take(',') {
				return nil, p.fail("expected ',' or '}'")
			}
			p.space()
		}
	case '[':
		n.Kind = syntax.Array
		p.pos++
		p.space()
		if p.take(']') {
			break
		}
		for {
			child, err := p.value(depth + 1)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, child)
			p.space()
			if p.take(']') {
				break
			}
			if !p.take(',') {
				return nil, p.fail("expected ',' or ']'")
			}
			p.space()
		}
	case '"':
		n.Kind = syntax.String
		s, err := p.string()
		if err != nil {
			return nil, err
		}
		n.Value = s
	case 't':
		if !p.literal("true") {
			return nil, p.fail("expected 'true'")
		}
		n.Kind = syntax.Bool
		n.Value = true
	case 'f':
		if !p.literal("false") {
			return nil, p.fail("expected 'false'")
		}
		n.Kind = syntax.Bool
		n.Value = false
	case 'n':
		if !p.literal("null") {
			return nil, p.fail("expected 'null'")
		}
		n.Kind = syntax.Null
	default:
		if p.source[p.pos] != '-' && !digit(p.source[p.pos]) {
			return nil, p.fail("expected a JSON value")
		}
		if err := p.number(); err != nil {
			return nil, err
		}
		n.Kind = syntax.NumberKind
		n.Value = syntax.Number(p.text(start, p.pos))
	}
	n.End = p.pos
	return n, nil
}

// text returns the source slice as a string. A caller that owns the buffer keeps
// it immutable, so the string can share its bytes; otherwise they are copied.
func (p *parser) text(start, end int) string {
	slice := p.source[start:end]
	if !p.owned {
		return string(slice)
	}
	return unsafe.String(unsafe.SliceData(slice), len(slice))
}

func (p *parser) take(b byte) bool {
	if p.pos < len(p.source) && p.source[p.pos] == b {
		p.pos++
		return true
	}
	return false
}
func (p *parser) literal(s string) bool {
	if bytes.HasPrefix(p.source[p.pos:], []byte(s)) {
		p.pos += len(s)
		return true
	}
	return false
}
func digit(b byte) bool { return b >= '0' && b <= '9' }
func (p *parser) number() error {
	p.take('-')
	if !p.take('0') {
		if p.pos == len(p.source) || p.source[p.pos] < '1' || p.source[p.pos] > '9' {
			return p.fail("expected integer part")
		}
		for p.pos < len(p.source) && digit(p.source[p.pos]) {
			p.pos++
		}
	}
	if p.take('.') {
		start := p.pos
		for p.pos < len(p.source) && digit(p.source[p.pos]) {
			p.pos++
		}
		if p.pos == start {
			return p.fail("expected fractional digits")
		}
	}
	if p.take('e') || p.take('E') {
		if !p.take('+') {
			p.take('-')
		}
		start := p.pos
		for p.pos < len(p.source) && digit(p.source[p.pos]) {
			p.pos++
		}
		if p.pos == start {
			return p.fail("expected exponent digits")
		}
	}
	return nil
}

func (p *parser) string() (string, error) {
	p.pos++ // opening quote was checked by the caller
	start := p.pos
	// Keep short keys in this function; vector setup costs more than their scan.
	p.pos += scan.JSONStringScalar(p.source[p.pos:min(p.pos+16, len(p.source))])
	if p.pos-start == 16 {
		p.pos += scan.JSONString(p.source[p.pos:])
	}
	if p.pos == len(p.source) {
		return "", p.fail("unterminated string")
	}
	if p.source[p.pos] == '"' {
		s := p.text(start, p.pos)
		p.pos++
		return s, nil
	}
	return p.escapedString(start)
}

func (p *parser) escapedString(start int) (string, error) {
	var b strings.Builder
	b.Write(p.source[start:p.pos])
	run := 0
	for p.pos < len(p.source) {
		c := p.source[p.pos]
		p.pos++
		switch {
		case c == '"':
			return b.String(), nil
		case c < 0x20:
			return "", p.fail("unescaped control character in string")
		case c != '\\':
			b.WriteByte(c)
			run++
			if run == 16 {
				end := p.pos + scan.JSONString(p.source[p.pos:])
				b.Write(p.source[p.pos:end])
				p.pos = end
				run = 0
			}
		default:
			run = 0
			if p.pos == len(p.source) {
				return "", p.fail("incomplete string escape")
			}
			e := p.source[p.pos]
			p.pos++
			switch e {
			case '"', '\\', '/':
				b.WriteByte(e)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				r, err := p.hex4()
				if err != nil {
					return "", err
				}
				if r >= 0xd800 && r <= 0xdbff {
					if !p.take('\\') || !p.take('u') {
						return "", p.fail("high surrogate requires a low surrogate escape")
					}
					low, err := p.hex4()
					if err != nil {
						return "", err
					}
					if low < 0xdc00 || low > 0xdfff {
						return "", p.fail("invalid low surrogate")
					}
					r = utf16.DecodeRune(r, low)
				} else if r >= 0xdc00 && r <= 0xdfff {
					return "", p.fail("unpaired low surrogate")
				}
				b.WriteRune(r)
			default:
				return "", p.fail("invalid string escape")
			}
		}
	}
	return "", p.fail("unterminated string")
}

func (p *parser) hex4() (rune, error) {
	var n rune
	for range 4 {
		if p.pos == len(p.source) {
			return 0, p.fail("incomplete Unicode escape")
		}
		b := p.source[p.pos]
		p.pos++
		n <<= 4
		switch {
		case b >= '0' && b <= '9':
			n += rune(b - '0')
		case b >= 'a' && b <= 'f':
			n += rune(b - 'a' + 10)
		case b >= 'A' && b <= 'F':
			n += rune(b - 'A' + 10)
		default:
			return 0, p.fail("invalid Unicode escape")
		}
	}
	return n, nil
}
