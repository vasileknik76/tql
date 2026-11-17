package toml

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	scalarstyle "github.com/vasileknik76/tql/internal/style"
	"github.com/vasileknik76/tql/syntax"
)

// insertionStyle describes only trivia and quoting. Source comments and values
// are never used as generated text.
type insertionStyle struct {
	before, after, separator, commaBefore, arrayPadding, objectPadding string
	indent, unit, nl                                                   string
	keyQuote, stringQuote                                              byte
	equalsColumn                                                       int
	column                                                             int
	flow                                                               bool
	preserveArrayLayout                                                bool
	policy                                                             syntax.Style
}

const inferredArrayWidth = 100

func defaultInsertionStyle(src []byte) insertionStyle {
	return insertionStyle{before: " ", after: " ", separator: " ", objectPadding: " ", unit: "  ", nl: newlineStyle(src), equalsColumn: -1}
}

func horizontal(s string) bool { return strings.Trim(s, " \t") == "" }

func sourceColumn(src []byte, pos int) int {
	start := pos
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	return utf8.RuneCount(src[start:pos])
}

func lineIndent(src []byte, pos int) string {
	start := pos
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	end := start
	for end < pos && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}

func (s insertionStyle) forAssignment(key string) insertionStyle {
	s.column = utf8.RuneCountInString(s.indent + key + s.before + "=" + s.after)
	return s
}

func sourceIndent(src []byte, pos int) string {
	start := pos
	for start > 0 && src[start-1] != '\n' {
		start--
	}
	gap := string(src[start:pos])
	if horizontal(gap) {
		return gap
	}
	return ""
}

// sectionEntries visits dotted assignments, but does not cross table headers or
// descend into concrete values belonging to another assignment.
func visitSectionEntries(n *syntax.Node, visit func(*syntax.Entry)) {
	for _, e := range n.Entries {
		if e.Start >= 0 {
			visit(e)
		} else if e.Value.Kind == syntax.Object && e.Value.Meta["explicit"] != true {
			visitSectionEntries(e.Value, visit)
		}
	}
}

func sectionEntries(n *syntax.Node) []*syntax.Entry {
	entries := make([]*syntax.Entry, 0, len(n.Entries))
	visitSectionEntries(n, func(e *syntax.Entry) { entries = append(entries, e) })
	if !sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].Start < entries[j].Start }) {
		sort.Slice(entries, func(i, j int) bool { return entries[i].Start < entries[j].Start })
	}
	return entries
}

func entryStyle(src []byte, e *syntax.Entry, s insertionStyle) insertionStyle {
	if e.Start < 0 || e.KeyEnd < 0 || e.Value.Start < 0 || e.Value.Start > len(src) {
		return s
	}
	end := e.KeyEnd
	for end > e.KeyStart && (src[end-1] == ' ' || src[end-1] == '\t') {
		end--
	}
	if e.KeyEnd < len(src) && src[e.KeyEnd] == '=' {
		s.before = string(src[end:e.KeyEnd])
		s.after = string(src[e.KeyEnd+1 : e.Value.Start])
	}
	s.indent = sourceIndent(src, e.Start)
	s.equalsColumn = -1
	s.keyQuote = 0
	if e.KeyStart < len(src) && (src[e.KeyStart] == '\'' || src[e.KeyStart] == '"') {
		s.keyQuote = src[e.KeyStart]
	}
	return s
}

func localInsertionStyle(src []byte, n *syntax.Node) insertionStyle {
	var chain []*syntax.Node
	for cur := n; cur != nil; cur = cur.Parent {
		chain = append(chain, cur)
	}
	s := defaultInsertionStyle(src)
	for _, cur := range slices.Backward(chain) {
		if cur.Style == "inline" {
			s.flow = true
		}
		s = containerWhitespace(src, cur, s)
		var previous, last, quoted *syntax.Entry
		visitSectionEntries(cur, func(e *syntax.Entry) {
			if last == nil || e.Start > last.Start {
				previous = last
				last = e
			} else if previous == nil || e.Start > previous.Start {
				previous = e
			}
			if e.Value.Kind == syntax.String && (quoted == nil || e.Start > quoted.Start) {
				quoted = e
			}
		})
		if last != nil {
			s = entryStyle(src, last, s)
			if cur.Start < 0 && previous != nil {
				s.equalsColumn = alignedEqualsColumn(src, []*syntax.Entry{previous, last})
			}
		}
		if quoted != nil {
			s.stringQuote = src[quoted.Value.Start]
		}

		if cur.Kind == syntax.Array && len(cur.Items) > 0 {
			for _, v := range slices.Backward(cur.Items) {
				if v.Kind == syntax.String && v.Start >= 0 {
					s.stringQuote = src[v.Start]
					break
				}
			}
		}
	}
	return s
}

func literalString(value string) bool {
	for _, r := range value {
		if r == '\'' || r == '\n' || r == '\r' || r < 0x20 && r != '\t' || r == 0x7f {
			return false
		}
	}
	return true
}

func styledString(value string, q byte) string {
	if q == '\'' && literalString(value) {
		return "'" + value + "'"
	}
	return quoteString(value)
}

func styledKey(key string, s insertionStyle) string {
	if s.keyQuote == '\'' {
		return styledString(key, '\'')
	}
	if s.keyQuote == '"' {
		return quoteString(key)
	}
	return quoteKey(key)
}

func styledPath(keys []string, s insertionStyle) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = styledKey(k, s)
	}
	return strings.Join(parts, ".")
}

func valueKind(value any) syntax.Kind { return scalarstyle.Kind(value) }

func insertionTemplate(src []byte, n *syntax.Node, kind syntax.Kind) *syntax.Node {
	return syntax.NearbyTemplate(n, kind, nil, func(v *syntax.Node) string { return scalarstyle.Signature(src, v) })
}

func containerWhitespace(src []byte, n *syntax.Node, s insertionStyle) insertionStyle {
	if n == nil || n.Start < 0 || n.End > len(src) {
		return s
	}
	if n.Kind != syntax.Array && n.Kind != syntax.Object {
		return s
	}
	first := n.End - 1
	if n.Kind == syntax.Array && len(n.Items) > 0 {
		first = n.Items[0].Start
	}
	if n.Kind == syntax.Object {
		entries := sectionEntries(n)
		if len(entries) > 0 {
			first = entries[0].Start
			s = entryStyle(src, entries[len(entries)-1], s)
		}
	}
	opening := string(src[n.Start+1 : first])
	if horizontal(opening) {
		if n.Kind == syntax.Array {
			s.arrayPadding = opening
		} else {
			s.objectPadding = opening
		}
	}
	if n.Kind == syntax.Array && len(n.Items) > 1 {
		a, b := n.Items[len(n.Items)-2], n.Items[len(n.Items)-1]
		c := comma(src, a.End, b.Start)
		if c >= 0 && horizontal(string(src[c+1:b.Start])) {
			s.separator = string(src[c+1 : b.Start])
			if horizontal(string(src[a.End:c])) {
				s.commaBefore = string(src[a.End:c])
			}
		}
	} else if n.Kind == syntax.Object {
		entries := sectionEntries(n)
		if len(entries) > 1 {
			a, b := entries[len(entries)-2], entries[len(entries)-1]
			c := comma(src, a.End, b.Start)
			if c >= 0 && horizontal(string(src[c+1:b.Start])) {
				s.separator = string(src[c+1 : b.Start])
				if horizontal(string(src[a.End:c])) {
					s.commaBefore = string(src[a.End:c])
				}
			}
		} else if horizontal(opening) {
			s.separator = opening
		}
	} else if horizontal(opening) {
		s.separator = opening
	}
	if n.Kind == syntax.Array && len(n.Items) > 0 {
		last := n.Items[len(n.Items)-1]
		if c := comma(src, last.End, n.End-1); c >= 0 && horizontal(string(src[last.End:c])) {
			s.commaBefore = string(src[last.End:c])
		}
	}
	return s
}

func renderInsertion(src []byte, value any, template *syntax.Node, s insertionStyle, depth int) (string, error) {
	if depth > 256 {
		return "", fmt.Errorf("TOML value nesting exceeds 256 levels")
	}
	baseIndent := s.indent
	if template != nil {
		s = containerWhitespace(src, template, s)
	}
	s = s.overrides()
	switch v := value.(type) {
	case string:
		q := s.stringQuote
		sample := ""
		if template != nil && template.Kind == syntax.String && template.Start >= 0 {
			sample = string(src[template.Start:template.End])
			if s.policy.Strings == "" || s.policy.Strings == "auto" {
				q = src[template.Start]
			}
		}
		return strings.ReplaceAll(renderString(v, s.policy.Strings, sample, q), "\n", s.nl), nil

	case []any:
		parts := make([]string, len(v))
		var exemplar *syntax.Node
		if template != nil && template.Kind == syntax.Array && len(template.Items) > 0 {
			exemplar = template.Items[len(template.Items)-1]
		}
		multi := s.policy.Arrays == "block" || s.policy.Arrays != "flow" && template != nil && template.Start >= 0 && strings.Contains(string(src[template.Start:template.End]), "\n")
		itemIndent := s.indent + s.unit
		closingIndent := s.indent
		if multi && template != nil && template.Start >= 0 && len(template.Items) > 0 && s.policy.Indent == "" {
			base := lineIndent(src, template.Start)
			if gap := sourceIndent(src, template.Items[len(template.Items)-1].Start); strings.HasPrefix(gap, base) {
				itemIndent = s.indent + strings.TrimPrefix(gap, base)
			}
			if gap := sourceIndent(src, template.End-1); strings.HasPrefix(gap, base) {
				closingIndent = s.indent + strings.TrimPrefix(gap, base)
			}
		}
		for i, x := range v {
			child := exemplar
			if child != nil && child.Kind != valueKind(x) {
				child = nil
			}
			var err error
			childStyle := s
			childStyle.indent = itemIndent
			parts[i], err = renderInsertion(src, x, child, childStyle, depth+1)
			if err != nil {
				return "", err
			}
		}
		if len(parts) == 0 {
			if multi && template != nil && template.Start >= 0 {
				return "[" + s.nl + closingIndent + "]", nil
			}
			return "[" + s.arrayPadding + "]", nil
		}
		trailing := template != nil && template.Start >= 0 && len(template.Items) > 0 && comma(src, template.Items[len(template.Items)-1].End, template.End-1) >= 0
		if multi {
			body := strings.Join(parts, s.commaBefore+","+s.nl+itemIndent)
			if trailing {
				body += s.commaBefore + ","
			}
			return "[" + s.nl + itemIndent + body + s.nl + closingIndent + "]", nil
		}
		tail := ""
		if trailing {
			tail = s.commaBefore + ","
		}
		text := "[" + s.arrayPadding + strings.Join(parts, s.commaBefore+","+s.separator) + tail + s.arrayPadding + "]"
		// A short inline example supplies spacing, not an unbounded line width.
		// Only new scalar arrays use this soft limit; replacements, explicit
		// flow, and inline-table contents retain their selected layout.
		if !s.preserveArrayLayout && !s.flow && (s.policy.Arrays == "" || s.policy.Arrays == "auto") && len(parts) > 1 && scalarArray(v) && s.column+utf8.RuneCountInString(text) > inferredArrayWidth {
			return "[" + s.nl + itemIndent + strings.Join(parts, s.commaBefore+","+s.nl+itemIndent) + tail + s.nl + s.indent + "]", nil
		}
		return text, nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		multiline := template != nil && template.Style == "inline" && template.Meta["multiline"] == true
		itemIndent := baseIndent + s.unit
		if multiline && s.policy.Indent == "" {
			entries := sectionEntries(template)
			if len(entries) > 0 {
				oldBase := sourceIndent(src, template.Start)
				oldItem := sourceIndent(src, entries[len(entries)-1].Start)
				if after, ok := strings.CutPrefix(oldItem, oldBase); ok {
					itemIndent = baseIndent + after
				}
			}
		}
		templates := make(map[syntax.Kind]*syntax.Node, 3)
		for _, k := range keys {
			var child *syntax.Node
			if template != nil && template.Kind == syntax.Object {
				child = template.Get(k)
				if child == nil {
					kind := valueKind(v[k])
					var found bool
					child, found = templates[kind]
					if !found {
						child = insertionTemplate(src, template, kind)
						templates[kind] = child
					}
				}
			}
			childStyle := s
			childStyle.flow = true
			if multiline {
				childStyle.indent = itemIndent
			}
			text, err := renderInsertion(src, v[k], child, childStyle, depth+1)
			if err != nil {
				return "", err
			}
			parts = append(parts, styledKey(k, s)+s.before+"="+s.after+text)
		}
		if len(parts) == 0 {
			return "{" + s.objectPadding + "}", nil
		}
		if multiline {
			tail := ""
			entries := sectionEntries(template)
			if len(entries) > 0 && comma(src, entries[len(entries)-1].End, template.End-1) >= 0 {
				tail = s.commaBefore + ","
			}
			return "{" + s.nl + itemIndent + strings.Join(parts, s.commaBefore+","+s.nl+itemIndent) + tail + s.nl + baseIndent + "}", nil
		}
		return "{" + s.objectPadding + strings.Join(parts, s.commaBefore+","+s.separator) + s.objectPadding + "}", nil
	default:
		sample := ""
		if template != nil && template.Start >= 0 {
			sample = string(src[template.Start:template.End])
		}
		if text, ok := scalarstyle.Integer(value, s.policy.Numbers, sample); ok {
			return text, nil
		}
		return render(value)
	}
}

func scalarArray(items []any) bool {
	for _, item := range items {
		if kind := valueKind(item); kind == syntax.Array || kind == syntax.Object {
			return false
		}
	}
	return true
}

func insertArrayStyled(src []byte, parent *syntax.Node, value any, policy syntax.Style) ([]syntax.Edit, error) {
	return appendArrayStyled(src, parent, []any{value}, policy)
}

func appendArrayStyled(src []byte, parent *syntax.Node, values []any, policy syntax.Style) ([]syntax.Edit, error) {
	s := localInsertionStyle(src, parent)
	s.policy = policy
	s = s.overrides()
	renderValues := func(style insertionStyle, separator string) (string, error) {
		parts := make([]string, len(values))
		for i, value := range values {
			var err error
			parts[i], err = renderInsertion(src, value, collectionTemplate(src, parent, value), style, 0)
			if err != nil {
				return "", err
			}
		}
		return strings.Join(parts, separator), nil
	}
	open, closing := parent.Start+1, parent.End-1
	multiline := strings.Contains(string(src[open:closing]), "\n")
	if !multiline {
		text, err := renderValues(s, s.commaBefore+","+s.separator)
		if err != nil {
			return nil, err
		}
		if len(parent.Items) == 0 {
			gap := string(src[open:closing])
			return []syntax.Edit{edit(open, open, gap+text)}, nil
		}
		last := parent.Items[len(parent.Items)-1]
		if c := comma(src, last.End, closing); c >= 0 {
			return []syntax.Edit{edit(c+1, c+1, s.separator+text+s.commaBefore+",")}, nil
		}
		return []syntax.Edit{edit(last.End, last.End, s.commaBefore+","+s.separator+text)}, nil
	}
	lineStart := closing
	for lineStart > open && src[lineStart-1] != '\n' {
		lineStart--
	}
	closingLine := horizontal(string(src[lineStart:closing]))
	indent := string(src[lineStart:closing]) + s.unit
	if !closingLine {
		indent = s.indent + s.unit
	}
	if len(parent.Items) > 0 {
		last := parent.Items[len(parent.Items)-1]
		start := last.Start
		for start > 0 && src[start-1] != '\n' {
			start--
		}
		if horizontal(string(src[start:last.Start])) {
			indent = string(src[start:last.Start])
		}
	}
	s.indent = indent
	text, err := renderValues(s, s.commaBefore+","+s.nl+indent)
	if err != nil {
		return nil, err
	}
	if len(parent.Items) == 0 {
		if closingLine {
			return []syntax.Edit{edit(lineStart, lineStart, indent+text+s.nl)}, nil
		}
		return []syntax.Edit{edit(closing, closing, s.nl+indent+text+s.nl+s.indent)}, nil
	}
	last := parent.Items[len(parent.Items)-1]
	c := comma(src, last.End, closing)
	trailing := c >= 0
	suffix := ""
	if trailing {
		suffix = s.commaBefore + ","
	}
	if closingLine && lineStart > last.End {
		edits := []syntax.Edit{edit(lineStart, lineStart, indent+text+suffix+s.nl)}
		if !trailing {
			edits = append(edits, edit(last.End, last.End, s.commaBefore+","))
		}
		return edits, nil
	}
	if trailing {
		return []syntax.Edit{edit(c+1, c+1, s.nl+indent+text+s.commaBefore+",")}, nil
	}
	return []syntax.Edit{edit(last.End, last.End, s.commaBefore+","+s.nl+indent+text)}, nil
}

func insertInlineAssignment(src []byte, owner *syntax.Node, assignment string, s insertionStyle) []syntax.Edit {
	open, closing := owner.Start+1, owner.End-1
	entries := inlineEntries(owner)
	if owner.Meta["multiline"] != true {
		if len(entries) == 0 {
			gap := string(src[open:closing])
			return []syntax.Edit{edit(open, open, gap+assignment)}
		}
		last := entries[len(entries)-1]
		if c := comma(src, last.End, closing); c >= 0 {
			return []syntax.Edit{edit(c+1, c+1, s.separator+assignment+s.commaBefore+",")}
		}
		return []syntax.Edit{edit(last.End, last.End, s.commaBefore+","+s.separator+assignment)}
	}
	lineStart := closing
	for lineStart > open && src[lineStart-1] != '\n' {
		lineStart--
	}
	closingLine := horizontal(string(src[lineStart:closing]))
	indent := s.indent + s.unit
	if closingLine {
		indent = string(src[lineStart:closing]) + s.unit
	}
	if len(entries) > 0 {
		last := entries[len(entries)-1]
		start := last.Start
		for start > 0 && src[start-1] != '\n' {
			start--
		}
		if horizontal(string(src[start:last.Start])) {
			indent = string(src[start:last.Start])
		}
	}
	if len(entries) == 0 {
		if closingLine {
			return []syntax.Edit{edit(lineStart, lineStart, indent+assignment+s.nl)}
		}
		return []syntax.Edit{edit(closing, closing, s.nl+indent+assignment+s.nl+s.indent)}
	}
	last := entries[len(entries)-1]
	c := comma(src, last.End, closing)
	suffix := ""
	if c >= 0 {
		suffix = s.commaBefore + ","
	}
	if closingLine && lineStart > last.End {
		edits := []syntax.Edit{edit(lineStart, lineStart, indent+assignment+suffix+s.nl)}
		if c < 0 {
			edits = append(edits, edit(last.End, last.End, s.commaBefore+","))
		}
		return edits
	}
	if c >= 0 {
		return []syntax.Edit{edit(c+1, c+1, s.nl+indent+assignment+s.commaBefore+",")}
	}
	return []syntax.Edit{edit(last.End, last.End, s.commaBefore+","+s.nl+indent+assignment)}
}

// Treat equals signs as aligned only when at least two differently sized keys
// demonstrate the same column. Fixed-width spacing alone is not evidence.
func alignedEqualsColumn(src []byte, entries []*syntax.Entry) int {
	if len(entries) < 2 {
		return -1
	}
	a, b := entries[len(entries)-2], entries[len(entries)-1]
	columns := [2]int{}
	lengths := [2]int{}
	for i, e := range []*syntax.Entry{a, b} {
		start := e.Start
		for start > 0 && src[start-1] != '\n' {
			start--
		}
		if !horizontal(string(src[start:e.Start])) || strings.Contains(string(src[start:e.KeyEnd]), "\t") {
			return -1
		}
		columns[i] = e.KeyEnd - start
		end := e.KeyEnd
		for end > e.KeyStart && src[end-1] == ' ' {
			end--
		}
		lengths[i] = end - e.KeyStart
	}
	if columns[0] == columns[1] && lengths[0] != lengths[1] {
		return columns[0]
	}
	return -1
}

func styledAssignment(key, value string, s insertionStyle) string {
	before := s.before
	if s.equalsColumn >= 0 {
		width := s.equalsColumn - len(s.indent) - len(key)
		if width > 0 {
			before = strings.Repeat(" ", width)
		} else {
			before = " "
		}
	}
	return key + before + "=" + s.after + value
}
