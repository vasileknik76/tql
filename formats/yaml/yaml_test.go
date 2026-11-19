package yaml

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vasileknik76/tql/syntax"
)

func parse(t *testing.T, src string) *syntax.Node {
	t.Helper()
	n, e := (Format{}).Parse([]byte(src))
	if e != nil {
		t.Fatalf("parse %q: %v", src, e)
	}
	return n
}
func apply(t *testing.T, src string, edits []syntax.Edit, e error) string {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
	out, e := syntax.Apply([]byte(src), edits)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = (Format{}).Parse(out); e != nil {
		t.Fatalf("invalid edit %q: %v", out, e)
	}
	return string(out)
}
func replace(t *testing.T, src string, n *syntax.Node, v any) string {
	t.Helper()
	r := n
	for r.Parent != nil {
		r = r.Parent
	}
	ed, e := (Format{}).Replace([]byte(src), r, n, v)
	return apply(t, src, ed, e)
}
func remove(t *testing.T, src string, n *syntax.Node) string {
	t.Helper()
	r := n
	for r.Parent != nil {
		r = r.Parent
	}
	ed, e := (Format{}).Delete([]byte(src), r, n)
	return apply(t, src, ed, e)
}
func insert(t *testing.T, src string, n *syntax.Node, k string, v any) string {
	t.Helper()
	r := n
	for r.Parent != nil {
		r = r.Parent
	}
	ed, e := (Format{}).Insert([]byte(src), r, n, k, v)
	return apply(t, src, ed, e)
}
func TestBlockAndFlow(t *testing.T) {
	src := "# head\nname: 'héllo' # retained\nitems:\n  - name: foo\n    enabled: true\n  - name: bar\n    values: [1, 2, {x: false}]\nempty:\n"
	n := parse(t, src)
	want := map[string]any{"name": "héllo", "items": []any{map[string]any{"name": "foo", "enabled": true}, map[string]any{"name": "bar", "values": []any{syntax.Number("1"), syntax.Number("2"), map[string]any{"x": false}}}}, "empty": nil}
	if !reflect.DeepEqual(n.Interface(), want) {
		t.Fatalf("%#v", n.Interface())
	}
	target := n.Get("items").Items[1].Get("name")
	out := replace(t, src, target, "baz")
	wantText := strings.Replace(src, "name: bar", "name: \"baz\"", 1)
	if out != wantText {
		t.Fatalf("%q != %q", out, wantText)
	}
}
func TestCoreScalars(t *testing.T) {
	cases := map[string]any{"null": nil, "~": nil, "NULL": nil, "true": true, "False": false, "yes": "yes", "on": "on", "012": syntax.Number("12"), "-0x10": "-0x10", "0o17": syntax.Number("15"), "+.5": syntax.Number("0.5"), "2.": syntax.Number("2.0"), "1e10": syntax.Number("1e10"), "2026-10-26": "2026-10-26", "123456789012345678901234567890": syntax.Number("123456789012345678901234567890")}
	for src, want := range cases {
		t.Run(src, func(t *testing.T) {
			n := parse(t, src)
			if !reflect.DeepEqual(n.Interface(), want) {
				t.Fatalf("%#v != %#v", n.Interface(), want)
			}
		})
	}
}
func TestQuotedEscapes(t *testing.T) {
	n := parse(t, `["a\n\x41\u20ac\U0001F600", 'it''s', "\N\_\L\P"]`)
	want := []any{"a\nA€😀", "it's", "\u0085\u00a0\u2028\u2029"}
	if !reflect.DeepEqual(n.Interface(), want) {
		t.Fatalf("%#v", n.Interface())
	}
	src := "x: 'old' # retain\n"
	if out := replace(t, src, parse(t, src).Get("x"), "it's"); out != "x: 'it''s' # retain\n" {
		t.Fatal(out)
	}
}
func TestPlainQuoteCharacters(t *testing.T) {
	src := "we're: a'b\nquote\"key: a\"b\n"
	n := parse(t, src)
	if n.Get("we're").Value != "a'b" || n.Get("quote\"key").Value != "a\"b" {
		t.Fatalf("%#v", n.Interface())
	}
}
func TestCRLineEndings(t *testing.T) {
	src := "a: 1 # c\rb: 2\r"
	out := insert(t, src, parse(t, src), "c", true)
	if out != "a: 1 # c\rb: 2\rc: true\r" {
		t.Fatalf("%q", out)
	}
	out = remove(t, src, parse(t, src).Get("a"))
	if out != "# c\rb: 2\r" {
		t.Fatalf("%q", out)
	}
	src = "x:\r  a: |\r    old\ry: 2\r"
	out = replace(t, src, parse(t, src).Get("x"), true)
	if out != "x:\r  true\ry: 2\r" {
		t.Fatalf("%q", out)
	}
}
func TestBlockScalars(t *testing.T) {
	cases := []struct{ src, want string }{{"x: |\n  hello\n  world\ny: 1\n", "hello\nworld\n"}, {"x: |-\n  a\n\n", "a"}, {"x: |+\n  a\n\n", "a\n\n"}, {"x: >\n  hello\n  world\n\n  again\n", "hello world\nagain\n"}, {"x: |2- # c\n  a\n  b\n", "a\nb"}, {"x: |\n  a", "a"}}
	for _, tt := range cases {
		t.Run(tt.src, func(t *testing.T) {
			n := parse(t, tt.src)
			if got := n.Get("x").Value; got != tt.want {
				t.Fatalf("%q != %q", got, tt.want)
			}
		})
	}
}
func TestReplaceBlockScalarKeepsComment(t *testing.T) {
	src := "x: |- # important\n  old\n  text\ny: 2 # yep\n"
	n := parse(t, src)
	out := replace(t, src, n.Get("x"), "new\ntext")
	if out != "x: \"new\\ntext\" # important\ny: 2 # yep\n" {
		t.Fatal(out)
	}
}
func TestInsertBlock(t *testing.T) {
	cases := []struct {
		src, key, want string
		value          any
	}{{"a: 1 # hi\n", "b", "a: 1 # hi\nb: 2\n", syntax.Number("2")}, {"a: 1", "b", "a: 1\nb: 2", syntax.Number("2")}, {"- x\n- y\n", "", "- x\n- y\n- 3\n", syntax.Number("3")}, {"# hi\r\na: 1\r\n", "b", "# hi\r\na: 1\r\nb: true\r\n", true}}
	for _, tt := range cases {
		t.Run(tt.src, func(t *testing.T) {
			out := insert(t, tt.src, parse(t, tt.src), tt.key, tt.value)
			if out != tt.want {
				t.Fatalf("%q != %q", out, tt.want)
			}
		})
	}
}
func TestInsertNested(t *testing.T) {
	src := "x:\n  a: 1\ny: 2\n"
	out := insert(t, src, parse(t, src).Get("x"), "b", true)
	want := "x:\n  a: 1\n  b: true\ny: 2\n"
	if out != want {
		t.Fatalf("%q", out)
	}
}
func TestDeleteBlock(t *testing.T) {
	cases := []struct{ src, key, want string }{{"a: 1\nb: 2\n", "a", "b: 2\n"}, {"a: 1 # keep\nb: 2\n", "a", "# keep\nb: 2\n"}, {"a: 1 # keep\n", "a", "{} # keep\n"}, {"x:\n  a: 1\ny: 2\n", "x", "y: 2\n"}}
	for _, tt := range cases {
		t.Run(tt.src, func(t *testing.T) {
			out := remove(t, tt.src, parse(t, tt.src).Get(tt.key))
			if out != tt.want {
				t.Fatalf("%q != %q", out, tt.want)
			}
		})
	}
}
func TestDeleteCompactMapFirstKey(t *testing.T) {
	src := "- a: 1\n  b: 2\n- a: 3\n"
	n := parse(t, src)
	out := remove(t, src, n.Items[0].Get("a"))
	if out != "- b: 2\n- a: 3\n" {
		t.Fatal(out)
	}
}
func TestDeleteCompactPreservesComment(t *testing.T) {
	for _, src := range []string{"- a: 1 # c\n  b: 2\n", "- - a # c\n  - b\n"} {
		n := parse(t, src)
		target := n.Items[0].Get("a")
		if n.Items[0].Kind == syntax.Array {
			target = n.Items[0].Items[0]
		}
		out := remove(t, src, target)
		if !strings.Contains(out, "# c") {
			t.Fatal(out)
		}
	}
}
func TestDeleteFlow(t *testing.T) {
	for _, src := range []string{"[1, 2, 3]", "[1, # a\n 2, 3]", "[1, 2, 3,]"} {
		for idx := range 3 {
			n := parse(t, src)
			out := remove(t, src, n.Items[idx])
			got := parse(t, out).Items
			if len(got) != 2 {
				t.Fatal(out)
			}
			if strings.Contains(src, "# a") && !strings.Contains(out, "# a") {
				t.Fatalf("lost comment: %q", out)
			}
		}
	}
	for _, src := range []string{"{a: 1, b: 2}", "{a: 1, b: 2,}"} {
		n := parse(t, src)
		out := remove(t, src, n.Get("b"))
		if len(parse(t, out).Entries) != 1 {
			t.Fatal(out)
		}
	}
}
func TestFlowInsert(t *testing.T) {
	for _, src := range []string{"[]", "[1]", "[1,]", "[1, # keep\n]"} {
		n := parse(t, src)
		out := insert(t, src, n, "", true)
		got := parse(t, out)
		if len(got.Items) != len(n.Items)+1 {
			t.Fatal(out)
		}
	}
}
func TestDocuments(t *testing.T) {
	for _, src := range []string{"", "# hi\n", "---\na: 1\n...\n", "%YAML 1.2\n---\na: 1\n", "\xef\xbb\xbfa: 1\r\n", "a:\n- 1\n- 2\n"} {
		parse(t, src)
	}
}

func TestBlockScalarBoundaries(t *testing.T) {
	src := "x:\n  a: |\n    text\ny: 2\n"
	n := parse(t, src)
	if out := remove(t, src, n.Get("x")); out != "y: 2\n" {
		t.Fatal(out)
	}
	out := insert(t, src, n.Get("x"), "b", true)
	if out != "x:\n  a: |\n    text\n  b: true\ny: 2\n" {
		t.Fatal(out)
	}
	out = replace(t, src, n.Get("x"), map[string]any{"b": true})
	if out != "x:\n  {\"b\":true}\ny: 2\n" {
		t.Fatal(out)
	}
}
func TestEmptyValuesAndCollections(t *testing.T) {
	src := "a: # c\nb: 2\n"
	n := parse(t, src)
	if out := replace(t, src, n.Get("a"), true); out != "a: true # c\nb: 2\n" {
		t.Fatal(out)
	}
	for _, s := range []string{"[1,]", "{a: 1,}"} {
		n := parse(t, s)
		target := n.Get("a")
		if n.Kind == syntax.Array {
			target = n.Items[0]
		}
		out := remove(t, s, target)
		if n := parse(t, out); len(n.Items)+len(n.Entries) != 0 {
			t.Fatal(out)
		}
	}
	src = "a: # c\n"
	out := remove(t, src, parse(t, src).Get("a"))
	if out != "{} # c\n" {
		t.Fatal(out)
	}
}
func TestInvalidAndUnsupported(t *testing.T) {
	for _, src := range []string{"a: 1\na: 2\n", "[1 2,] ]", "a: [1,", "x: \"\\uD800\"", "x: \"unterminated", "\ta: 1", "a: *anchor", "a: !thing value", "---\na: 1\n---\nb: 2", "x: |0\n  value", "{a: 1, a: 2}", ".inf", "{<<: 1}", "x: \"two\nlines\""} {
		t.Run(src, func(t *testing.T) {
			if _, e := (Format{}).Parse([]byte(src)); e == nil {
				t.Fatalf("accepted %q", src)
			}
		})
	}
}
func TestMultilineScalars(t *testing.T) {
	cases := []struct{ src, want string }{{"x: first\n  second\ny: 2\n", "first second"}, {"x: first\n\n  second\n", "first\nsecond"}, {"x: first\n  second # there\ny: 2\n", "first second"}, {"x: 'first\n  second'\n", "first second"}, {"x: \"first  \n  second\"\n", "first second"}, {"x: \"first\n\n  second\"\n", "first\nsecond"}, {"x: \"first\\\n  second\"\n", "firstsecond"}, {"x: \"first\\\n\n  second\"\n", "first\nsecond"}}
	for _, tt := range cases {
		t.Run(tt.src, func(t *testing.T) {
			n := parse(t, tt.src)
			if n.Get("x").Value != tt.want {
				t.Fatalf("%q != %q", n.Get("x").Value, tt.want)
			}
			out := replace(t, tt.src, n.Get("x"), "new")
			if parse(t, out).Get("x").Value != "new" {
				t.Fatal(out)
			}
			if strings.Contains(tt.src, "# there") && !strings.Contains(out, "# there") {
				t.Fatal(out)
			}
		})
	}
}
func TestCompactNestedSequences(t *testing.T) {
	src := "- - a\n  - b\n- - c\n  - d\n"
	n := parse(t, src)
	want := []any{[]any{"a", "b"}, []any{"c", "d"}}
	if !reflect.DeepEqual(n.Interface(), want) {
		t.Fatalf("%#v", n.Interface())
	}
	out := remove(t, src, n.Items[0].Items[0])
	if out != "- - b\n- - c\n  - d\n" {
		t.Fatal(out)
	}
	out = insert(t, src, n.Items[0], "", true)
	if out != "- - a\n  - b\n  - true\n- - c\n  - d\n" {
		t.Fatal(out)
	}
}
func TestReplaceEmptyDocumentAndValues(t *testing.T) {
	for _, src := range []string{"# comment", "---", "...", "---\n...", "a:", "-"} {
		n := parse(t, src)
		target := n
		if n.Kind == syntax.Array {
			target = n.Items[0]
		}
		if n.Kind == syntax.Object {
			target = n.Get("a")
		}
		out := replace(t, src, target, true)
		changed := parse(t, out)
		if changed.Kind == syntax.Array {
			changed = changed.Items[0]
		}
		if changed.Kind == syntax.Object {
			changed = changed.Get("a")
		}
		if changed.Value != true {
			t.Fatalf("%q -> %q", src, out)
		}
	}
}
func FuzzParse(f *testing.F) {
	for _, src := range []string{"a: 1\n", "[1, {b: 'a'}]", "x: |\n  text\n", "- a: 1\n  b: 2\n", "base: &x [1, {y: true}]\ncopy: *x\n", "x: !!str 123\n", "!!map\nx: 1\n"} {
		f.Add([]byte(src))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		n, e := (Format{}).Parse(src)
		if e != nil {
			return
		}
		var check func(*syntax.Node)
		check = func(n *syntax.Node) {
			if n.Start < 0 || n.End < n.Start || n.End > len(src) {
				t.Fatalf("invalid span [%d,%d)", n.Start, n.End)
			}
			for _, e := range n.Entries {
				check(e.Value)
			}
			for _, v := range n.Items {
				check(v)
			}
		}
		check(n)
	})
}
func FuzzEdits(f *testing.F) {
	for _, src := range []string{"! !\r0", "!!str !!str\n0", "!!str &a\n0", "&a !!str\n0"} {
		f.Add([]byte(src))
	}
	for _, src := range []string{"a: 1\n", "[1, {b: 'a'}]", "x: |\n  text\n", "- a: 1\n  b: 2\n", "x: first\n  second\n", "- - a\n  - b\n", "# hi", "---\n...\n", "x: !!str 123\n", "!!map\nx: 1\n", "!!str", "[!!str 1, 2]", "\t{}"} {
		f.Add([]byte(src))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if len(src) > 2000 {
			return
		}
		format := Format{}
		n, e := format.Parse(src)
		if e != nil {
			return
		}
		ed, e := format.Replace(src, n, n, true)
		if e != nil {
			t.Fatal(e)
		}
		out, e := syntax.Apply(src, ed)
		if e != nil {
			t.Fatal(e)
		}
		got, e := format.Parse(out)
		if e != nil || got.Kind != syntax.Bool || got.Value != true {
			t.Fatalf("replace %q -> %q: %v (%#v)", src, out, e, got)
		}
		if n.Kind == syntax.Array || n.Kind == syntax.Object {
			key := "new"
			if n.Kind == syntax.Array {
				key = ""
			}
			if n.Get(key) != nil {
				return
			}
			ed, e = format.Insert(src, n, n, key, true)
			if e != nil {
				t.Fatal(e)
			}
			out, e = syntax.Apply(src, ed)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = format.Parse(out); e != nil {
				t.Fatalf("insert %q -> %q: %v", src, out, e)
			}
			var children []*syntax.Node
			children = append(children, n.Items...)
			for _, entry := range n.Entries {
				children = append(children, entry.Value)
			}
			for _, v := range children {
				ed, e = format.Delete(src, n, v)
				if e != nil {
					t.Fatal(e)
				}
				out, e = syntax.Apply(src, ed)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = format.Parse(out); e != nil {
					t.Fatalf("delete %q -> %q: %v", src, out, e)
				}
			}
		}
	})
}
