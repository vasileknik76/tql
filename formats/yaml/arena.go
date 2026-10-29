package yaml

import "github.com/vasileknik76/tql/syntax"

// arenaSize is the number of nodes and entries allocated per block. Trees keep
// individual elements alive, so blocks never need to be tracked separately.
const arenaSize = 1024

// nextBlock doubles a block up to arenaSize, so short parses stay small while
// large documents reach full blocks after a few allocations.
func nextBlock(current int) int {
	if current == 0 {
		return 16
	}
	return min(current*2, arenaSize)
}

// nodeArena hands out tree nodes and object entries from blocks, so a document
// costs one allocation per block instead of one per element.
type nodeArena struct {
	nodes   []syntax.Node
	entries []syntax.Entry
}

func (a *nodeArena) node(n syntax.Node) *syntax.Node {
	if len(a.nodes) == cap(a.nodes) {
		a.nodes = make([]syntax.Node, 0, nextBlock(cap(a.nodes)))
	}
	a.nodes = append(a.nodes, n)
	return &a.nodes[len(a.nodes)-1]
}

func (a *nodeArena) entry(e syntax.Entry) *syntax.Entry {
	if len(a.entries) == cap(a.entries) {
		a.entries = make([]syntax.Entry, 0, nextBlock(cap(a.entries)))
	}
	a.entries = append(a.entries, e)
	return &a.entries[len(a.entries)-1]
}
