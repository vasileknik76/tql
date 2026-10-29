package toml

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vasileknik76/tql/syntax"
)

func TestParseArenaReturnsDistinctNodes(t *testing.T) {
	var source strings.Builder
	for i := range 200 {
		fmt.Fprintf(&source, "[[services]]\nname = \"svc%d\"\nport = %d\nenabled = true\n\n", i, 8000+i)
	}
	n := parse(t, source.String())
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
	if len(seen) < 200*3 {
		t.Fatalf("expected a large tree, walked %d nodes", len(seen))
	}
}

func TestParsedDocumentsDoNotShareNodes(t *testing.T) {
	source := []byte("a = 1\nb = \"two\"\n")
	first := parse(t, string(source))
	second := parse(t, string(source))
	if first.Get("a") == second.Get("a") {
		t.Fatal("documents share tree nodes")
	}
	first.Get("a").Value = syntax.Number("99")
	if second.Get("a").Value != syntax.Number("1") {
		t.Fatal("nodes are aliased between documents")
	}
}
