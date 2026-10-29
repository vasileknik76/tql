package yaml

import (
	"bytes"
	"fmt"

	"github.com/vasileknik76/tql/syntax"
)

// NodeKind identifies a node in a YAML syntax graph.
type NodeKind string

const (
	ScalarNode   NodeKind = "scalar"
	SequenceNode NodeKind = "sequence"
	MappingNode  NodeKind = "mapping"
	AliasNode    NodeKind = "alias"
)

// Node preserves YAML keys, tags, anchors and alias identity without JSON coercion.
// Content holds sequence elements or alternating mapping keys and values.
type Node struct {
	Kind        NodeKind
	Tag         string
	Value       string // Decoded scalar lexeme or alias name, without schema construction.
	Anchor      string
	Alias       *Node
	Content     []*Node
	Start, End  int // Source span including any leading tag or anchor properties.
	Style       string
	ExplicitTag bool
}

// Document is one root graph and its source span, including document markers.
type Document struct {
	Root                       *Node
	Start, End                 int
	ExplicitStart, ExplicitEnd bool
	Version                    string
}

// Stream owns the original source and the documents parsed from it.
type Stream struct {
	Documents []*Document
	source    []byte
}

// Bytes returns an independent copy of the stream's original source.
func (s *Stream) Bytes() []byte { return bytes.Clone(s.source) }

// ParseStream reads all YAML documents, retaining non-string keys and alias edges.
// An error returns no partial stream. The returned graph owns its source copy.
func ParseStream(source []byte) (*Stream, error) {
	src := bytes.Clone(source)
	roots, err := parseDocuments(src)
	if err != nil {
		return nil, err
	}
	stream := &Stream{source: src, Documents: make([]*Document, 0, len(roots))}
	for _, root := range roots {
		graph := graphBuilder{source: src, nodes: make(map[*syntax.Node]*Node)}
		node, err := graph.node(root)
		if err != nil {
			return nil, err
		}
		doc := &Document{Root: node, Start: node.Start, End: node.End}
		if start, ok := root.Meta["documentStart"].(int); ok {
			doc.Start = start
		}
		if end, ok := root.Meta["documentEnd"].(int); ok {
			doc.End = end
		}
		doc.ExplicitStart, _ = root.Meta["explicitStart"].(bool)
		doc.ExplicitEnd, _ = root.Meta["explicitEnd"].(bool)
		doc.Version, _ = root.Meta["version"].(string)
		stream.Documents = append(stream.Documents, doc)
	}
	return stream, nil
}

type graphBuilder struct {
	source []byte
	nodes  map[*syntax.Node]*Node
}

func (b *graphBuilder) node(source *syntax.Node) (*Node, error) {
	if source == nil {
		return nil, fmt.Errorf("yaml: missing graph node")
	}
	if node := b.nodes[source]; node != nil {
		return node, nil
	}
	node := &Node{Start: source.Start, End: source.End, Style: source.Style}
	b.nodes[source] = node
	node.Anchor, _ = source.Meta["anchorName"].(string)
	if start, ok := source.Meta["anchorStart"].(int); ok && start < node.Start {
		node.Start = start
	}
	if tag, ok := source.Meta["tag"].(tagInfo); ok {
		node.ExplicitTag = true
		if tag.start < node.Start {
			node.Start = tag.start
		}
	}
	node.Tag, _ = source.Meta["tagURI"].(string)
	switch source.Kind {
	case syntax.Kind("yaml-alias"):
		node.Kind = AliasNode
		node.Value, _ = source.Meta["aliasName"].(string)
		target, ok := source.Meta["aliasTarget"].(*syntax.Node)
		if !ok || target == nil {
			return nil, errAt(source.Start, "alias has no target")
		}
		alias, err := b.node(target)
		if err != nil {
			return nil, err
		}
		node.Alias = alias
	case syntax.Object:
		node.Kind = MappingNode
		if node.Tag == "" {
			node.Tag = "tag:yaml.org,2002:map"
		}
		if node.Style == "flow-pair" {
			node.Style = "flow"
		}
		node.Content = make([]*Node, 0, len(source.Entries)*2)
		for _, entry := range source.Entries {
			keySource, ok := entry.Meta["keyNode"].(*syntax.Node)
			if !ok || keySource == nil {
				return nil, errAt(entry.KeyStart, "mapping key graph is missing")
			}
			key, err := b.node(keySource)
			if err != nil {
				return nil, err
			}
			value, err := b.node(entry.Value)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, key, value)
		}
	case syntax.Array:
		node.Kind = SequenceNode
		if node.Tag == "" {
			node.Tag = "tag:yaml.org,2002:seq"
		}
		node.Content = make([]*Node, 0, len(source.Items))
		for _, item := range source.Items {
			child, err := b.node(item)
			if err != nil {
				return nil, err
			}
			node.Content = append(node.Content, child)
		}
	default:
		node.Kind = ScalarNode
		if value, ok := source.Meta["scalarValue"].(string); ok {
			node.Value = value
		} else if value, ok := source.Value.(string); ok {
			node.Value = value
		} else if source.Start == source.End {
			node.Value = ""
		} else {
			return nil, errAt(source.Start, "scalar graph value is missing")
		}
		if node.Tag == "" {
			switch source.Kind {
			case syntax.Null:
				node.Tag = "tag:yaml.org,2002:null"
			case syntax.Bool:
				node.Tag = "tag:yaml.org,2002:bool"
			case syntax.NumberKind:
				name := "float"
				if intLiteral(string(source.Value.(syntax.Number))) {
					name = "int"
				}
				node.Tag = "tag:yaml.org,2002:" + name
			default:
				node.Tag = "tag:yaml.org,2002:str"
			}
		}
		if node.Style == "block-scalar" && source.Start >= 0 && source.Start < len(b.source) {
			if b.source[source.Start] == '|' {
				node.Style = "literal"
			} else {
				node.Style = "folded"
			}
		}
	}
	return node, nil
}
