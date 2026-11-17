package toml

import (
	"bytes"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vasileknik76/tql/syntax"
)

func render(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", fmt.Errorf("TOML has no null value")
	case syntax.Temporal:
		if !temporalRE.MatchString(string(v)) || !validTime(string(v)) {
			return "", fmt.Errorf("invalid TOML date/time")
		}
		return string(v), nil
	case string:
		return quoteString(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		if math.IsNaN(v) {
			return "nan", nil
		}
		if math.IsInf(v, 1) {
			return "inf", nil
		}
		if math.IsInf(v, -1) {
			return "-inf", nil
		}
		s := strconv.FormatFloat(v, 'g', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s, nil
	case syntax.Number:
		p := &parser{src: []byte(v)}
		n, e := p.atom()
		if e != nil || p.pos != len(p.src) || n.Kind != syntax.NumberKind {
			return "", fmt.Errorf("invalid TOML number %q", v)
		}
		return string(v), nil
	case []any:
		parts := make([]string, len(v))
		for i, x := range v {
			s, e := render(x)
			if e != nil {
				return "", e
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			s, e := render(v[k])
			if e != nil {
				return "", e
			}
			parts = append(parts, quoteKey(k)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	default:
		return "", fmt.Errorf("unsupported TOML value %T", value)
	}
}

func quoteString(s string) string { return strings.ReplaceAll(syntax.Quote(s), "\x7f", `\u007f`) }

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func quoteKey(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return quoteString(k)
}
func edit(start, end int, s string) syntax.Edit {
	return syntax.Edit{Start: start, End: end, Text: []byte(s)}
}

func parentEntry(n *syntax.Node) (*syntax.Entry, error) {
	if n.Parent == nil {
		return nil, fmt.Errorf("root has no entry")
	}
	e := n.Parent.Entry(n)
	if e == nil {
		return nil, fmt.Errorf("value is not an object member")
	}
	return e, nil
}
func newlineStyle(src []byte) string {
	if bytes.Contains(src, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// context finds a concrete inline table or the nearest explicit table section.
func context(n *syntax.Node) (*syntax.Node, []string, error) {
	var reverse []string
	for {
		if n.Kind != syntax.Object {
			return nil, nil, fmt.Errorf("invalid TOML insertion parent")
		}
		if n.Style == "inline" || n.Parent == nil || n.Meta["explicit"] == true {
			for i, j := 0, len(reverse)-1; i < j; i, j = i+1, j-1 {
				reverse[i], reverse[j] = reverse[j], reverse[i]
			}
			return n, reverse, nil
		}
		e, err := parentEntry(n)
		if err != nil {
			return nil, nil, err
		}
		reverse = append(reverse, e.Key)
		n = n.Parent
	}
}
func pathText(keys []string) string {
	a := make([]string, len(keys))
	for i, k := range keys {
		a[i] = quoteKey(k)
	}
	return strings.Join(a, ".")
}
func allHeaders(n *syntax.Node, positions *[]int) {
	if s, ok := n.Meta["headerStart"].(int); ok {
		*positions = append(*positions, s)
	}
	for _, e := range n.Entries {
		allHeaders(e.Value, positions)
	}
	for _, v := range n.Items {
		allHeaders(v, positions)
	}
}

// assignmentPosition keeps section separators and following comment blocks
// after the inserted assignment. It never moves or rewrites that trailing text.
func assignmentPosition(src []byte, root, owner *syntax.Node) int {
	start := 0
	if p, ok := owner.Meta["headerEnd"].(int); ok {
		start = p
	}
	boundary := len(src)
	var headers []int
	allHeaders(root, &headers)
	for _, p := range headers {
		if p >= start && p < boundary {
			boundary = bytes.LastIndexByte(src[:p], '\n') + 1
		}
	}
	last := -1
	visitSectionEntries(owner, func(e *syntax.Entry) {
		if e.Start >= start && e.End <= boundary && e.End > last {
			last = e.End
		}
	})
	if last >= 0 {
		for last < len(src) && src[last] != '\n' {
			last++
		}
		if last < len(src) {
			last++
		}
		return last
	}
	// With no assignments, retain the section's preamble before the new key,
	// while leaving its final blank lines as separators.
	for boundary > start {
		line := bytes.LastIndexByte(src[:boundary-1], '\n') + 1
		if line < start || len(bytes.Trim(src[line:boundary], " \t\r\n")) != 0 {
			break
		}
		boundary = line
	}
	return boundary
}
func insertion(src []byte, root, parent *syntax.Node, key string, value any, policy syntax.Style) ([]syntax.Edit, error) {
	owner, path, err := context(parent)
	if err != nil {
		return nil, err
	}
	style := localInsertionStyle(src, parent)
	style.policy = policy
	style = style.overrides()
	template := collectionTemplate(src, parent, value)
	if owner.Style != "inline" {
		objectTemplate := template
		if template == nil || template.Kind != syntax.Object {
			objectTemplate = collectionTemplate(src, parent, map[string]any{})
		}
		mode := objectMode(policy, objectTemplate)
		if m, ok := value.(map[string]any); ok && mode != "flow" && (mode != "dotted" || len(m) != 0) {
			return insertStructure(src, parent, key, value, template, style, mode)
		}
		if xs, ok := value.([]any); ok && tableArray(xs) && arrayTables(policy, template, mode) {
			return insertStructure(src, parent, key, value, template, style, mode)
		}
	}
	if items, ok := value.([]any); template == nil && ok && tableArray(items) {
		exemplar := collectionTemplate(src, parent, map[string]any{})
		if exemplar != nil {
			template = &syntax.Node{Kind: syntax.Array, Start: -1, End: -1, Items: []*syntax.Node{exemplar}}
		}
	}
	path = append(path, key)
	style = style.forAssignment(styledPath(path, style))
	text, err := renderInsertion(src, value, template, style, 0)
	if err != nil {
		return nil, err
	}
	assignment := styledAssignment(styledPath(path, style), text, style)
	if owner.Style == "inline" {
		return insertInlineAssignment(src, owner, assignment, style), nil
	}
	pos := assignmentPosition(src, root, owner)
	prefix := ""
	if pos > 0 && src[pos-1] != '\n' {
		prefix = style.nl
	}
	suffix := style.nl
	if pos == len(src) && pos > 0 && src[pos-1] != '\n' {
		suffix = ""
	}
	return []syntax.Edit{edit(pos, pos, prefix+style.indent+assignment+suffix)}, nil
}

// Insert plans a missing object member or an array append.
func (f Format) Insert(src []byte, root, parent *syntax.Node, key string, value any) ([]syntax.Edit, error) {
	if err := validateValueVersion(value, f.Version(root), 0); err != nil {
		return nil, err
	}
	if root == nil || parent == nil {
		return nil, fmt.Errorf("toml: invalid insertion target")
	}
	if parent.Kind == syntax.Array {
		if key != "-" && key != strconv.Itoa(len(parent.Items)) {
			return nil, fmt.Errorf("toml: array insertion requires '-' or index %d", len(parent.Items))
		}
		if parent.Style == "tables" {
			m, ok := value.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("array tables require object items")
			}
			keys, err := absolutePath(parent)
			if err != nil {
				return nil, err
			}
			var template *syntax.Node
			if len(parent.Items) > 0 {
				template = parent.Items[len(parent.Items)-1]
			}
			style := localInsertionStyle(src, parent)
			if template != nil {
				style = localInsertionStyle(src, template)
			}
			style.policy = f.style
			style = style.overrides()
			body, err := renderSection(src, m, keys, template, style, 0)
			if err != nil {
				return nil, err
			}
			pos := len(src)
			for ancestor := parent.Parent; ancestor != nil; ancestor = ancestor.Parent {
				if ancestor.Parent != nil && ancestor.Parent.Style == "tables" {
					siblings := ancestor.Parent.Items
					index := ancestor.Parent.Index(ancestor)
					if index+1 < len(siblings) {
						if next, ok := siblings[index+1].Meta["headerStart"].(int); ok && next < pos {
							pos = next
						}
					}
				}
			}
			prefix := ""
			if pos > 0 && src[pos-1] != '\n' {
				prefix = style.nl
			}
			header := "[[" + pathText(keys) + "]]"
			if template != nil {
				if start, ok := template.Meta["headerStart"].(int); ok {
					end := template.Meta["headerEnd"].(int)
					header = sourceIndent(src, start) + string(src[start:end])
				}
			}
			addition := prefix + header + style.nl + body
			if pos == len(src) && pos > 0 && src[pos-1] != '\n' {
				addition = strings.TrimSuffix(addition, style.nl)
			}
			return []syntax.Edit{edit(pos, pos, addition)}, nil
		}
		return insertArrayStyled(src, parent, value, f.style)
	}
	if parent.Kind != syntax.Object {
		return nil, fmt.Errorf("TOML insertion requires object or array")
	}
	if parent.Get(key) != nil {
		return nil, fmt.Errorf("key %q already exists", key)
	}
	return insertion(src, root, parent, key, value, f.style)
}
func absolutePath(n *syntax.Node) ([]string, error) {
	var keys []string
	for n.Parent != nil {
		if n.Parent.Kind == syntax.Array {
			n = n.Parent
			continue
		}
		e, err := parentEntry(n)
		if err != nil {
			return nil, err
		}
		keys = append(keys, e.Key)
		n = n.Parent
	}
	for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
		keys[i], keys[j] = keys[j], keys[i]
	}
	return keys, nil
}
func renderDocument(m map[string]any) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		s, e := render(m[k])
		if e != nil {
			return "", e
		}
		b.WriteString(quoteKey(k) + " = " + s + "\n")
	}
	return b.String(), nil
}
func ownedSyntax(n *syntax.Node) []syntax.Edit {
	var edits []syntax.Edit
	if s, ok := n.Meta["headerStart"].(int); ok {
		edits = append(edits, edit(s, n.Meta["headerEnd"].(int), ""))
	}
	for _, e := range n.Entries {
		if e.Start >= 0 {
			edits = append(edits, edit(e.Start, e.End, ""))
		} else {
			edits = append(edits, ownedSyntax(e.Value)...)
		}
	}
	for _, v := range n.Items {
		edits = append(edits, ownedSyntax(v)...)
	}
	return edits
}

// removeLine expands only when the selected syntax is the sole content of its
// physical line(s). A trailing comment stays at its original position. Blank
// lines and comments outside the selected span are never globally normalized.
func removeLine(src []byte, start, end int) syntax.Edit {
	ls := bytes.LastIndexByte(src[:start], '\n') + 1
	if len(bytes.Trim(src[ls:start], " \t\r")) != 0 {
		return edit(start, end, "")
	}
	le := end
	for le < len(src) && (src[le] == ' ' || src[le] == '\t' || src[le] == '\r') {
		le++
	}
	if le == len(src) {
		return edit(ls, le, "")
	}
	if src[le] == '\n' {
		return edit(ls, le+1, "")
	}
	return edit(start, end, "")
}

func ownedEdits(src []byte, n *syntax.Node) []syntax.Edit {
	return removeOwnedLines(src, ownedSyntax(n), false)
}

// A removed section owns its following empty separator lines, up to the next
// comment or syntax. Keep leading separators and all comment lines verbatim.
func removeOwnedLines(src []byte, edits []syntax.Edit, separators bool) []syntax.Edit {
	for i, e := range edits {
		edits[i] = removeLine(src, e.Start, e.End)
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	var out []syntax.Edit
	for _, e := range edits {
		if len(out) > 0 {
			last := &out[len(out)-1]
			if e.Start >= last.End && len(bytes.TrimSpace(src[last.End:e.Start])) == 0 {
				last.End = e.End
				continue
			}
		}
		out = append(out, e)
	}
	for i := range out {
		if !separators {
			continue
		}
		e := &out[i]
		if e.End == 0 || src[e.End-1] != '\n' {
			continue
		}
		pos := e.End
		for pos < len(src) {
			end := pos
			for end < len(src) && (src[end] == ' ' || src[end] == '\t' || src[end] == '\r') {
				end++
			}
			if end == len(src) {
				e.End = end
				break
			}
			if src[end] != '\n' {
				break
			}
			pos = end + 1
			e.End = pos
		}
	}
	return out
}

// Merge syntax and its commas on the same physical line before expanding to
// whole lines. A comma on a surviving line must never absorb the next newline:
// doing so could attach a retained comment to the wrong value.
func removeInlineSyntax(src []byte, edits []syntax.Edit) []syntax.Edit {
	sort.Slice(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	var out []syntax.Edit
	for _, e := range edits {
		if len(out) > 0 {
			last := &out[len(out)-1]
			if e.Start >= last.End && len(bytes.Trim(src[last.End:e.Start], " \t")) == 0 {
				last.End = e.End
				continue
			}
		}
		out = append(out, e)
	}
	for i, e := range out {
		out[i] = removeLine(src, e.Start, e.End)
	}
	return out
}

// Replace plans replacement of a selected value.
func (f Format) Replace(src []byte, root, target *syntax.Node, value any) ([]syntax.Edit, error) {
	if err := validateValueVersion(value, f.Version(root), 0); err != nil {
		return nil, err
	}
	if root == nil || target == nil || target.Start >= 0 && (target.End < target.Start || target.End > len(src)) {
		return nil, fmt.Errorf("toml: invalid replacement target")
	}
	if target.Start >= 0 {
		if items, ok := value.([]any); ok && target.Kind == syntax.Array {
			// An unchanged prefix has a known correspondence. Append without
			// replacing the existing bytes or guessing comment ownership.
			if f.style.IsAuto() && len(items) > len(target.Items) && arrayPrefix(target, items) {
				return appendArrayStyled(src, target, items[len(target.Items):], f.style)
			}
			style := localInsertionStyle(src, target)
			style.policy = f.style
			style.preserveArrayLayout = true
			style.indent = lineIndent(src, target.Start)
			if bytes.Contains(src[target.Start:target.End], []byte("\n")) {
				style.nl = newlineStyle(src[target.Start:target.End])
			}
			style.column = sourceColumn(src, target.Start)
			text, err := renderInsertion(src, value, target, style, 0)
			if err != nil {
				return nil, err
			}
			return []syntax.Edit{edit(target.Start, target.End, text)}, nil
		}
		s, err := render(value)
		if err != nil {
			return nil, err
		}
		return []syntax.Edit{edit(target.Start, target.End, s)}, nil
	}
	if target.Parent == nil {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("TOML root must remain an object")
		}
		s, err := renderDocument(m)
		if err != nil {
			return nil, err
		}
		edits := ownedEdits(src, target)
		return append([]syntax.Edit{edit(0, 0, s)}, edits...), nil
	}
	if target.Parent.Kind == syntax.Array {
		m, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("an array-table item must remain an object; replace its whole array to change its type")
		}
		body, err := renderDocument(m)
		if err != nil {
			return nil, err
		}
		body = strings.ReplaceAll(body, "\n", newlineStyle(src))
		start := target.Meta["headerStart"].(int)
		end := target.Meta["headerEnd"].(int)
		// Keep the original header and its trailing comment in place.
		pos := end
		for pos < len(src) && src[pos] != '\n' {
			pos++
		}
		if pos < len(src) {
			pos++
		} else {
			body = newlineStyle(src) + body
		}
		edits := ownedSyntax(target)
		kept := edits[:0]
		for _, e := range edits {
			if e.Start != start || e.End != end {
				kept = append(kept, e)
			}
		}
		return append([]syntax.Edit{edit(pos, pos, body)}, removeOwnedLines(src, kept, false)...), nil
	}
	if owner := enclosingInline(target.Parent); owner != nil {
		_, path, err := context(target.Parent)
		if err != nil {
			return nil, err
		}
		member, err := parentEntry(target)
		if err != nil {
			return nil, err
		}
		path = append(path, member.Key)
		s, err := render(value)
		if err != nil {
			return nil, err
		}
		entries := inlineEntries(owner)
		var edits []syntax.Edit
		first := true
		for i, e := range entries {
			if !descendant(e.Value, target) {
				continue
			}
			if first {
				edits = append(edits, edit(e.Start, e.End, pathText(path)+" = "+s))
				first = false
			} else {
				edits = append(edits, edit(e.Start, e.End, ""))
				if i > 0 {
					c := comma(src, entries[i-1].End, e.Start)
					if c >= 0 {
						edits = append(edits, edit(c, c+1, ""))
					}
				}
			}
		}
		return edits, nil
	}
	e, err := parentEntry(target)
	if err != nil {
		return nil, err
	}
	ins, err := insertion(src, root, target.Parent, e.Key, value, f.style)
	if err != nil {
		return nil, err
	}
	return append(ins, ownedEdits(src, target)...), nil
}

func arrayPrefix(target *syntax.Node, items []any) bool {
	for i, child := range target.Items {
		if !reflect.DeepEqual(child.Interface(), items[i]) {
			return false
		}
	}
	return true
}
func comma(src []byte, start, end int) int {
	comment := false
	for i := start; i < end; i++ {
		if src[i] == '\n' {
			comment = false
		}
		if src[i] == '#' {
			comment = true
		}
		if src[i] == ',' && !comment {
			return i
		}
	}
	return -1
}
func deleteDelimited(src []byte, start, end, index int, prevEnd, nextStart int) ([]syntax.Edit, error) {
	c := comma(src, end, nextStart)
	if c >= 0 {
		// A comment between the value and comma must survive at its position.
		if len(bytes.TrimSpace(src[end:c])) == 0 {
			end = c + 1
			line := removeLine(src, start, end)
			if line.Start != start || line.End != end {
				return []syntax.Edit{line}, nil
			}
			padding := end
			for padding < nextStart && (src[padding] == ' ' || src[padding] == '\t') {
				padding++
			}
			if padding == nextStart || src[padding] != '#' {
				end = padding
			}
			return []syntax.Edit{edit(start, end, "")}, nil
		}
		return []syntax.Edit{removeLine(src, start, end), removeLine(src, c, c+1)}, nil
	} else if index > 0 {
		c = comma(src, prevEnd, start)
		if c < 0 {
			return nil, fmt.Errorf("missing separator")
		}
		line := removeLine(src, start, end)
		if line.Start != start || line.End != end {
			return []syntax.Edit{removeLine(src, c, c+1), line}, nil
		}
		if len(bytes.Trim(src[c+1:start], " \t")) == 0 {
			return []syntax.Edit{edit(c, end, "")}, nil
		}
		return []syntax.Edit{removeLine(src, c, c+1), line}, nil
	}
	// When the whole inline contents disappear, retain one side's original
	// padding rather than joining both sides into a doubled empty-container gap.
	if start > prevEnd && nextStart > end && len(bytes.Trim(src[prevEnd:start], " \t")) == 0 && len(bytes.Trim(src[end:nextStart], " \t")) == 0 {
		end = nextStart
	}
	return []syntax.Edit{removeLine(src, start, end)}, nil
}
func enclosingInline(n *syntax.Node) *syntax.Node {
	for n != nil {
		if n.Style == "inline" {
			return n
		}
		n = n.Parent
	}
	return nil
}
func inlineEntries(n *syntax.Node) []*syntax.Entry {
	var out []*syntax.Entry
	for _, e := range n.Entries {
		if e.Start >= 0 {
			out = append(out, e)
		} else {
			out = append(out, inlineEntries(e.Value)...)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}
func descendant(n, ancestor *syntax.Node) bool {
	for n != nil {
		if n == ancestor {
			return true
		}
		n = n.Parent
	}
	return false
}

// Delete plans removal of a node and its required syntax.
func (f Format) Delete(src []byte, root, target *syntax.Node) ([]syntax.Edit, error) {
	if root == nil || target == nil || target.Start >= 0 && (target.End < target.Start || target.End > len(src)) {
		return nil, fmt.Errorf("toml: invalid deletion target")
	}
	if target.Parent == nil {
		return nil, fmt.Errorf("cannot delete TOML root")
	}
	parent := target.Parent
	// A dotted assignment may be the only source spelling of its parent table.
	// Preserve the now-empty parent rather than silently deleting that table too.
	if parent.Kind == syntax.Object && len(parent.Entries) == 1 && parent.Start < 0 && parent.Meta["explicit"] != true && parent.Parent != nil {
		return f.Replace(src, root, parent, map[string]any{})
	}
	if parent.Kind == syntax.Array {
		if parent.Style == "tables" {
			if len(parent.Items) == 1 {
				return f.Replace(src, root, parent, []any{})
			}
			return removeOwnedLines(src, ownedSyntax(target), true), nil
		}
		index := parent.Index(target)
		prev := parent.Start + 1
		next := parent.End - 1
		if index > 0 {
			prev = parent.Items[index-1].End
		}
		if index+1 < len(parent.Items) {
			next = parent.Items[index+1].Start
		}
		return deleteDelimited(src, target.Start, target.End, index, prev, next)
	}
	if inline := enclosingInline(parent); inline != nil {
		entries := inlineEntries(inline)
		var edits []syntax.Edit
		for i := 0; i < len(entries); {
			if !descendant(entries[i].Value, target) {
				i++
				continue
			}
			first := i
			for i < len(entries) && descendant(entries[i].Value, target) {
				i++
			}
			last := i - 1
			// Comments between dotted assignments belong to the source gaps,
			// not to the synthetic parent. Do not swallow them in a run span.
			if first != last && bytes.Contains(src[entries[first].End:entries[last].Start], []byte("#")) {
				var run []syntax.Edit
				for j := first; j <= last; j++ {
					run = append(run, edit(entries[j].Start, entries[j].End, ""))
					if j < last {
						if c := comma(src, entries[j].End, entries[j+1].Start); c >= 0 {
							run = append(run, edit(c, c+1, ""))
						}
					}
				}
				c := comma(src, entries[last].End, inline.End-1)
				if i < len(entries) {
					c = comma(src, entries[last].End, entries[i].Start)
				}
				if c < 0 && first > 0 {
					c = comma(src, entries[first-1].End, entries[first].Start)
				}
				if c >= 0 {
					run = append(run, edit(c, c+1, ""))
				}
				edits = append(edits, removeInlineSyntax(src, run)...)
				continue
			}
			// The run includes its internal separators. Remove one boundary
			// separator and local padding using the same rules as arrays.
			start, end := entries[first].Start, entries[last].End
			prev, next := inline.Start+1, inline.End-1
			if first > 0 {
				prev = entries[first-1].End
			}
			if i < len(entries) {
				next = entries[i].Start
			}
			planned, err := deleteDelimited(src, start, end, first, prev, next)
			if err != nil {
				return nil, err
			}
			edits = append(edits, planned...)
		}
		return edits, nil
	}
	e, err := parentEntry(target)
	if err != nil {
		return nil, err
	}
	if e.Start >= 0 {
		return []syntax.Edit{removeLine(src, e.Start, e.End)}, nil
	}
	return removeOwnedLines(src, ownedSyntax(target), true), nil
}
