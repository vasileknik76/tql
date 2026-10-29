package yaml

import (
	"math/big"
	"strconv"
	"strings"

	"github.com/vasileknik76/tql/syntax"
)

// intLiteral and floatLiteral match the YAML plain-scalar number forms without
// running a regular expression or allocating on every scalar.
func intLiteral(s string) bool {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func floatLiteral(s string) bool {
	if s != "" && (s[0] == '+' || s[0] == '-') {
		s = s[1:]
	}
	digits := func(text string) int {
		i := 0
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		return i
	}
	intPart := digits(s)
	rest := s[intPart:]
	if rest != "" && rest[0] == '.' {
		frac := digits(rest[1:])
		if intPart == 0 && frac == 0 {
			return false
		}
		rest = rest[1+frac:]
	} else if intPart == 0 {
		return false
	}
	if rest == "" {
		return true
	}
	if rest[0] != 'e' && rest[0] != 'E' {
		return false
	}
	rest = rest[1:]
	if rest != "" && (rest[0] == '+' || rest[0] == '-') {
		rest = rest[1:]
	}
	return rest != "" && digits(rest) == len(rest)
}

func scalar(a *nodeArena, s string, start, end int) (*syntax.Node, error) {
	kind, value := scalarKind(s)
	return a.node(syntax.Node{Kind: kind, Value: value, Start: start, End: end, Style: "plain", Meta: map[string]any{"scalarValue": s}}), nil
}

// scalarKind classifies a plain scalar without building a node, so tag handling
// can validate a typed scalar without allocating a tree element for it.
func scalarKind(s string) (syntax.Kind, any) {
	kind, value := syntax.String, any(s)
	switch s {
	case "~", "null", "Null", "NULL":
		kind, value = syntax.Null, nil
	case "true", "True", "TRUE":
		kind, value = syntax.Bool, true
	case "false", "False", "FALSE":
		kind, value = syntax.Bool, false
	default:
		if s == ".nan" || s == ".NaN" || s == ".NAN" || strings.EqualFold(strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-"), ".inf") {
			return syntax.NumberKind, syntax.Number(s)
		}
		if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0o") {
			base := 16
			if s[1] == 'o' {
				base = 8
			}
			digits := s[2:]
			allowed := "01234567"
			if base == 16 {
				allowed = "0123456789abcdefABCDEF"
			}
			if digits != "" && strings.Trim(digits, allowed) == "" {
				z, _ := new(big.Int).SetString(digits, base)
				kind, value = syntax.NumberKind, syntax.Number(z.String())
			}
		} else if intLiteral(s) {
			if v, err := strconv.ParseInt(s, 10, 64); err == nil {
				kind, value = syntax.NumberKind, syntax.Number(strconv.FormatInt(v, 10))
			} else {
				z, _ := new(big.Int).SetString(s, 10)
				kind, value = syntax.NumberKind, syntax.Number(z.String())
			}
		} else if floatLiteral(s) {
			x := strings.TrimPrefix(s, "+")
			if strings.HasPrefix(x, ".") {
				x = "0" + x
			}
			if strings.HasPrefix(x, "-.") {
				x = "-0" + x[1:]
			}
			idx := strings.IndexAny(x, "eE")
			mant, exp := x, ""
			if idx >= 0 {
				mant, exp = x[:idx], x[idx:]
			}
			if strings.HasSuffix(mant, ".") {
				mant += "0"
			}
			negative := strings.HasPrefix(mant, "-")
			unsignedMant := strings.TrimPrefix(mant, "-")
			parts := strings.SplitN(unsignedMant, ".", 2)
			parts[0] = strings.TrimLeft(parts[0], "0")
			if parts[0] == "" {
				parts[0] = "0"
			}
			mant = strings.Join(parts, ".")
			if negative {
				mant = "-" + mant
			}
			kind, value = syntax.NumberKind, syntax.Number(mant+exp)
		}
	}
	return kind, value
}
func (p *parser) blockScalar(start, end, indent int) (*syntax.Node, error) {
	head := string(p.src[start:end])
	if i := strings.IndexByte(head, '#'); i >= 0 {
		if i > 0 && head[i-1] != ' ' && head[i-1] != '\t' {
			return nil, errAt(start+i, "comment must be separated")
		}
		head = head[:i]
	}
	head = strings.Trim(head, " \t")
	style := head[0]
	chomp := byte(0)
	explicit := 0
	for i := 1; i < len(head); i++ {
		c := head[i]
		if c == '+' || c == '-' {
			if chomp != 0 {
				return nil, errAt(start, "duplicate chomping indicator")
			}
			chomp = c
		} else if c >= '1' && c <= '9' {
			if explicit != 0 {
				return nil, errAt(start, "duplicate indentation indicator")
			}
			explicit = int(c - '0')
		} else {
			return nil, errAt(start+i, "invalid block scalar header")
		}
	}
	contentIndent := max(indent, 0) + explicit
	j := p.i
	if explicit == 0 {
		for j < len(p.lines) && strings.Trim(p.lines[j].text, " ") == "" {
			j++
		}
		if j < len(p.lines) && p.lines[j].indent > indent {
			contentIndent = p.lines[j].indent
		} else {
			contentIndent = indent + 1
			// With no nonempty content line, whitespace-only lines establish
			// indentation themselves instead of exceeding an inferred width.
			for k := p.i; k < j; k++ {
				if p.lines[k].indent > contentIndent {
					contentIndent = p.lines[k].indent
				}
			}
		}
	}
	var rows []string
	var terminated []bool
	contentTerminated := false
	last := end
	leading := true
	for p.i < len(p.lines) {
		l := p.lines[p.i]
		if marker(l, "---") || marker(l, "...") {
			break
		}
		blank := strings.Trim(l.text, " ") == ""
		if explicit == 0 && leading && blank && l.indent > contentIndent {
			return nil, errAt(l.start, "leading empty block scalar line is over-indented")
		}
		if !blank {
			leading = false
		}
		if !blank && l.indent < contentIndent {
			if strings.HasPrefix(l.text, "\t") {
				return nil, errAt(l.start+l.indent, "tab indentation is not allowed")
			}
			if l.indent > indent && !strings.HasPrefix(l.text, "#") {
				return nil, errAt(l.start, "invalid block scalar indentation")
			}
			break
		}
		text := ""
		if l.end-l.start >= contentIndent {
			text = string(p.src[l.start+contentIndent : l.end])
		}
		rows = append(rows, text)
		// A final all-space line still contributes an empty line at EOF.
		lineTerminated := l.next > l.end || blank
		terminated = append(terminated, lineTerminated)
		if text != "" {
			contentTerminated = lineTerminated
		}
		last = l.next
		p.i++
	}
	var b strings.Builder
	for i, s := range rows {
		b.WriteString(s)
		if i+1 < len(rows) {
			if style == '>' && s != "" && rows[i+1] != "" && !moreIndented(s) && !moreIndented(rows[i+1]) {
				b.WriteByte(' ')
			} else if style == '>' && s != "" && rows[i+1] == "" && !moreIndented(s) {
				j := i + 1
				for j < len(rows) && rows[j] == "" {
					j++
				}
				if j == len(rows) || moreIndented(rows[j]) {
					b.WriteByte('\n')
				}
			} else {
				b.WriteByte('\n')
			}
		} else if terminated[i] {
			b.WriteByte('\n')
		}
	}
	value := b.String()
	if chomp == '-' {
		value = strings.TrimRight(value, "\n")
	} else if chomp != '+' {
		value = strings.TrimRight(value, "\n")
		if value != "" && contentTerminated {
			value += "\n"
		}
	}
	return p.arena.node(syntax.Node{Kind: syntax.String, Value: value, Start: start, End: last, Style: "block-scalar", Meta: map[string]any{"indent": indent, "headerEnd": end, "scalarValue": value}}), nil
}

func moreIndented(s string) bool {
	return strings.HasPrefix(s, " ") || strings.HasPrefix(s, "\t")
}
