package yaml

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vasileknik76/tql/syntax"
)

func TestScalarNumberClassification(t *testing.T) {
	cases := []struct {
		input string
		kind  syntax.Kind
		value string
	}{
		{"7", syntax.NumberKind, "7"},
		{"+7", syntax.NumberKind, "7"},
		{"007", syntax.NumberKind, "7"},
		{"-0", syntax.NumberKind, "0"},
		{"1.5", syntax.NumberKind, "1.5"},
		{"1.", syntax.NumberKind, "1.0"},
		{".5", syntax.NumberKind, "0.5"},
		{"-.5", syntax.NumberKind, "-0.5"},
		{"1e3", syntax.NumberKind, "1e3"},
		{"1E+3", syntax.NumberKind, "1E+3"},
		{"0x1f", syntax.NumberKind, "31"},
		{"0o17", syntax.NumberKind, "15"},
		{"123456789012345678901234567890", syntax.NumberKind, "123456789012345678901234567890"},
		{"1_000", syntax.String, "1_000"},
		{"1.2.3", syntax.String, "1.2.3"},
		{"12:30", syntax.String, "12:30"},
		{"1e", syntax.String, "1e"},
		{".", syntax.String, "."},
		{"", syntax.Null, ""},
	}
	for _, tc := range cases {
		n, err := (Format{}).Parse([]byte(tc.input))
		if err != nil {
			t.Fatalf("%q: %v", tc.input, err)
		}
		if n.Kind != tc.kind {
			t.Fatalf("%q: kind %q, want %q", tc.input, n.Kind, tc.kind)
		}
		if tc.kind == syntax.Null {
			continue
		}
		got := fmt.Sprint(n.Value)
		if number, ok := n.Value.(syntax.Number); ok {
			got = string(number)
		}
		if got != tc.value {
			t.Fatalf("%q: value %q, want %q", tc.input, got, tc.value)
		}
	}
}

func TestParseArenaReturnsDistinctNodes(t *testing.T) {
	var source strings.Builder
	source.WriteString("services:\n")
	for i := range 500 {
		fmt.Fprintf(&source, "  - name: svc%d\n    port: %d\n    enabled: true\n", i, 8000+i)
	}
	n, err := (Format{}).Parse([]byte(source.String()))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[*syntax.Node]bool{}
	var walk func(*syntax.Node)
	walk = func(node *syntax.Node) {
		if node == nil || seen[node] {
			return
		}
		seen[node] = true
		for _, entry := range node.Entries {
			walk(entry.Value)
		}
		for _, item := range node.Items {
			walk(item)
		}
	}
	walk(n)
	if len(seen) < 500*4 {
		t.Fatalf("expected a large tree, walked %d nodes", len(seen))
	}
}

func TestParsedDocumentsDoNotShareMetadata(t *testing.T) {
	source := []byte("a: 1\nb: two\n")
	first, err := (Format{}).Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Format{}).Parse(source)
	if err != nil {
		t.Fatal(err)
	}
	first.Meta["marker"] = true
	if second.Meta["marker"] != nil {
		t.Fatal("metadata maps are shared between documents")
	}
	if first.Get("a") == second.Get("a") {
		t.Fatal("documents share tree nodes")
	}
}
