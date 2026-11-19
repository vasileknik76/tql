package yaml

import (
	"reflect"
	"strings"
	"testing"

	"github.com/vasileknik76/tql/syntax"
)

func TestNativeTags(t *testing.T) {
	cases := []struct {
		src  string
		want any
	}{{"!!str 123", "123"}, {"!!str null", "null"}, {"!!str .inf", ".inf"}, {"!!str", ""}, {"!!int '0x10'", syntax.Number("16")}, {"!!float '1.2'", syntax.Number("1.2")}, {"!!bool 'True'", true}, {"!!null ''", nil}, {"!!seq [1, 2]", []any{syntax.Number("1"), syntax.Number("2")}}, {"!!map {a: 1}", map[string]any{"a": syntax.Number("1")}}, {"!<tag:yaml.org,2002:str> false", "false"}, {"!!map\na: 1\n", map[string]any{"a": syntax.Number("1")}}}
	for _, tt := range cases {
		t.Run(tt.src, func(t *testing.T) {
			n := parse(t, tt.src)
			if !reflect.DeepEqual(n.Interface(), tt.want) {
				t.Fatalf("%#v != %#v", n.Interface(), tt.want)
			}
		})
	}
}
func TestEmptyFlowProperties(t *testing.T) {
	for _, src := range []string{"[&a, *a]", "[!!str, !!null]", "{a: !!str, b: !!null}"} {
		parse(t, src)
	}
}
func TestYAMLEscapedControls(t *testing.T) {
	src := "x: 'old'\n"
	value := "\x00\x7f\u009f\ufffe"
	out := replace(t, src, parse(t, src).Get("x"), value)
	if parse(t, out).Get("x").Value != value {
		t.Fatalf("%q", out)
	}
	out = insert(t, src, parse(t, src), "\x7f", value)
	if parse(t, out).Get("\x7f").Value != value {
		t.Fatalf("%q", out)
	}
}
func TestTagAnchorCombinations(t *testing.T) {
	for _, src := range []string{"base: &a !!map\n  name: foo\ncopy: *a\n", "base: !!map &a\n  name: foo\ncopy: *a\n", "base: &a !!str 123\ncopy: *a\n", "base: !!str &a 123\ncopy: *a\n", "[&a !!str 123, *a]"} {
		n := parse(t, src)
		if n.Kind == syntax.Object && !reflect.DeepEqual(n.Get("base").Interface(), n.Get("copy").Interface()) {
			t.Fatal(n.Interface())
		}
	}
}
func TestTaggedReplacement(t *testing.T) {
	src := "x: !!str 123 # c\n"
	n := parse(t, src)
	out := replace(t, src, n.Get("x"), "new")
	if out != "x: !!str \"new\" # c\n" {
		t.Fatal(out)
	}
	out = replace(t, src, n.Get("x"), true)
	if parse(t, out).Get("x").Value != true {
		t.Fatal(out)
	}
	src = "x: !!int 123\n"
	out = replace(t, src, parse(t, src).Get("x"), syntax.Number("1.2"))
	if parse(t, out).Get("x").Value != syntax.Number("1.2") {
		t.Fatal(out)
	}
	src = "x: !!map\n  a: 1\ny: 2\n"
	out = replace(t, src, parse(t, src).Get("x"), true)
	if parse(t, out).Get("x").Value != true {
		t.Fatal(out)
	}
}
func TestInvalidNativeTags(t *testing.T) {
	for _, src := range []string{"!!int 1.5", "!!bool yes", "!!null false", "!!map []", "!!seq {}", "!!str []", "!!float .inf", "!!unknown x", "!!str !!int 1", "a: &x 1\nb: !!int *x\n"} {
		if _, e := (Format{}).Parse([]byte(src)); e == nil {
			t.Fatalf("accepted %q", src)
		}
	}
}

func TestRejectDuplicatePendingTags(t *testing.T) {
	for _, src := range []string{
		"! !\r0", "! !\n0", "! !\r\n0", "!!str !!str\n0", "!!str !!str",
		"!!str !!int\n0", "! ! # comment\n0",
		"x: ! !\n  0", "- ! !\n  0",
		"! &a !\n0", "&a ! !\n0", "! ! &a\n0",
	} {
		t.Run(src, func(t *testing.T) {
			if _, err := ParseStream([]byte(src)); err == nil || !strings.Contains(err.Error(), "duplicate tag properties") {
				t.Fatalf("expected duplicate tag error for %q, got %v", src, err)
			}
		})
	}
}

func TestRemovingRootTagKeepsPropertyIndentation(t *testing.T) {
	for _, source := range []string{"!!str\t", "!!str\t0", "!!map\t{}", "!!seq\t[]", "!!str &0\n0", "  !!map &0\n  a: 0", "!!str\t&a\n0", "&a !!str\n0", "!!str &a\r0", "&a !!str\r\n0", "!!str &a", "&a !!str"} {
		t.Run(source, func(t *testing.T) {
			root := parse(t, source)
			out := replace(t, source, root, true)
			if got := parse(t, out).Interface(); got != true {
				t.Fatalf("%q resolved to %#v", out, got)
			}
		})
	}
}
