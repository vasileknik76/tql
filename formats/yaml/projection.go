package yaml

import (
	"fmt"
	"maps"
	"strings"

	"github.com/vasileknik76/tql/syntax"
)

func projectDocument(source []byte) (*syntax.Node, error) {
	documents, err := parseDocuments(source)
	if err != nil {
		return nil, err
	}
	if len(documents) == 0 {
		return &syntax.Node{Kind: syntax.Null, Start: len(source), End: len(source)}, nil
	}
	if len(documents) != 1 {
		return nil, errAt(0, "multiple YAML documents cannot be queried; use ParseStream")
	}
	projector := queryProjection{source: source, active: make(map[*syntax.Node]bool)}
	root, err := projector.node(documents[0], 0, nil)
	if err != nil {
		return nil, err
	}
	syntax.Link(root)
	return root, nil
}

type queryProjection struct {
	arena      nodeArena
	source     []byte
	active     map[*syntax.Node]bool
	aliasNodes int
}

func (p *queryProjection) node(source *syntax.Node, depth int, alias *segment) (*syntax.Node, error) {
	if source == nil {
		return nil, fmt.Errorf("yaml: missing graph node")
	}
	if p.active[source] {
		return nil, errAt(source.Start, "recursive YAML aliases cannot be queried; use ParseStream")
	}
	p.active[source] = true
	defer delete(p.active, source)
	if source.Kind == syntax.Kind("yaml-alias") {
		target, ok := source.Meta["aliasTarget"].(*syntax.Node)
		if !ok || target == nil {
			return nil, errAt(source.Start, "alias has no target")
		}
		if alias != nil {
			return p.node(target, depth, alias)
		}
		span := segment{source.Start, source.End}
		out, err := p.node(target, depth, &span)
		if err != nil {
			return nil, err
		}
		out.Meta = maps.Clone(source.Meta)
		delete(out.Meta, "aliasTarget")
		return out, nil
	}
	if alias != nil {
		p.aliasNodes++
		if p.aliasNodes > 100000 {
			return nil, errAt(alias.start, "alias expansion limit exceeded")
		}
	}
	// Meta is written only while parsing; the projected tree shares it with the
	// raw tree instead of copying a map into every node and entry.
	out := p.arena.node(syntax.Node{Kind: source.Kind, Value: source.Value, Start: source.Start, End: source.End, Style: source.Style, Meta: source.Meta})
	if source.Kind == syntax.Object || source.Kind == syntax.Array {
		depth++
		if depth > 256 {
			return nil, errAt(source.Start, "nesting limit exceeded")
		}
	}
	for _, item := range source.Items {
		child, err := p.node(item, depth, alias)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, child)
	}
	seen := make(map[string]bool, len(source.Entries))
	for _, entry := range source.Entries {
		key, err := p.key(entry)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, errAt(entry.KeyStart, "mapping keys collide in the TQL string-key projection")
		}
		if key == "<<" {
			return nil, errAt(entry.KeyStart, "merge keys cannot be queried; use ParseStream")
		}
		seen[key] = true
		child, err := p.node(entry.Value, depth, alias)
		if err != nil {
			return nil, err
		}
		projected := p.arena.entry(syntax.Entry{Key: key, KeyStart: entry.KeyStart, KeyEnd: entry.KeyEnd, Start: entry.Start, End: entry.End, Value: child, Meta: entry.Meta})
		if alias != nil {
			projected.KeyStart, projected.KeyEnd = alias.start, alias.end
			projected.Start, projected.End, projected.Meta = alias.start, alias.end, nil
		}
		out.Entries = append(out.Entries, projected)
	}
	if err := p.construct(out); err != nil {
		return nil, err
	}
	if out.Kind == syntax.NumberKind && nonFinite(out.Value.(syntax.Number)) {
		return nil, errAt(source.Start, "non-finite YAML numbers cannot be queried; use ParseStream")
	}
	if alias != nil {
		out.Start, out.End, out.Style, out.Meta = alias.start, alias.end, "alias", nil
	}
	return out, nil
}

func (p *queryProjection) construct(node *syntax.Node) error {
	uri, _ := node.Meta["tagURI"].(string)
	if uri == "" {
		return nil
	}
	switch uri {
	case "!", "tag:yaml.org,2002:str", "tag:yaml.org,2002:int", "tag:yaml.org,2002:float", "tag:yaml.org,2002:bool", "tag:yaml.org,2002:null", "tag:yaml.org,2002:seq", "tag:yaml.org,2002:map":
		tag, ok := node.Meta["tag"].(tagInfo)
		if !ok {
			return errAt(node.Start, "tag source information is missing")
		}
		return (&parser{src: p.source}).constructTag(node, tag)
	default:
		return errAt(node.Start, "custom YAML tags cannot be queried; use ParseStream")
	}
}

func (p *queryProjection) key(entry *syntax.Entry) (string, error) {
	key, ok := entry.Meta["keyNode"].(*syntax.Node)
	if !ok || key == nil {
		return "", errAt(entry.KeyStart, "mapping key graph is missing")
	}
	wasAlias := key.Kind == syntax.Kind("yaml-alias")
	visited := make(map[*syntax.Node]bool)
	for key.Kind == syntax.Kind("yaml-alias") {
		if visited[key] {
			return "", errAt(entry.KeyStart, "recursive YAML mapping keys cannot be queried; use ParseStream")
		}
		visited[key] = true
		target, ok := key.Meta["aliasTarget"].(*syntax.Node)
		if !ok || target == nil {
			return "", errAt(entry.KeyStart, "alias has no target")
		}
		key = target
	}
	if key.Kind == syntax.Array || key.Kind == syntax.Object {
		return "", errAt(entry.KeyStart, "complex YAML mapping keys cannot be queried; use ParseStream")
	}
	projected := p.arena.node(syntax.Node{Kind: key.Kind, Value: key.Value, Start: key.Start, End: key.End, Style: key.Style, Meta: key.Meta})
	if err := p.construct(projected); err != nil {
		return "", err
	}
	if projected.Kind == syntax.String {
		return projected.Value.(string), nil
	}
	text, _ := key.Meta["scalarValue"].(string)
	if projected.Kind == syntax.Null && (key.Start == key.End || text == "") {
		return "", errAt(entry.KeyStart, "empty null YAML mapping keys cannot be queried; use ParseStream")
	}
	if wasAlias {
		return "", errAt(entry.KeyStart, "non-string alias keys cannot be queried; use ParseStream")
	}
	if projected.Kind == syntax.NumberKind && nonFinite(projected.Value.(syntax.Number)) {
		return "", errAt(entry.KeyStart, "non-finite YAML mapping keys cannot be queried; use ParseStream")
	}
	if value, ok := key.Meta["scalarValue"].(string); ok {
		return value, nil
	}
	return entry.Key, nil
}

func nonFinite(number syntax.Number) bool {
	value := strings.ToLower(string(number))
	return strings.Contains(value, "inf") || strings.Contains(value, "nan")
}
