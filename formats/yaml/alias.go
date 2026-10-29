package yaml

import (
	"fmt"
	"strings"

	"github.com/vasileknik76/tql/syntax"
)

type anchorInfo struct {
	name       string
	start, end int
	target     *syntax.Node
}

func property(src []byte, start, end int) (anchorInfo, int, error) {
	pos := start + 1
	for pos < end && !strings.ContainsRune(" \t\r\n[]{},", rune(src[pos])) {
		pos++
	}
	if pos < end && (src[pos] == '[' || src[pos] == '{') {
		return anchorInfo{}, pos, errAt(pos, "anchor property must be separated from its node")
	}
	if pos == start+1 {
		return anchorInfo{}, pos, errAt(start, "empty anchor or alias name")
	}
	name := string(src[start+1 : pos])
	return anchorInfo{name: name, start: start, end: pos}, pos, nil
}
func (p *parser) beginAnchor(a anchorInfo) *syntax.Node {
	if p.anchors == nil {
		p.anchors = map[string]*syntax.Node{}
	}
	target := p.arena.node(syntax.Node{Start: a.end, End: a.end})
	p.anchors[a.name] = target
	return target
}
func (p *parser) bind(n *syntax.Node, a anchorInfo) (*syntax.Node, error) {
	if _, alias := n.Meta["aliasName"]; alias {
		return nil, errAt(a.start, "alias nodes cannot have anchor properties")
	}
	if _, exists := n.Meta["anchorName"]; exists {
		return nil, errAt(a.start, "duplicate anchor properties")
	}
	n.Meta = meta(n.Meta)
	n.Meta["anchorName"] = a.name
	n.Meta["anchorStart"] = a.start
	n.Meta["anchorEnd"] = a.end
	if a.target == nil {
		a.target = p.beginAnchor(a)
	}
	*a.target = *n
	return a.target, nil
}
func (p *parser) alias(a anchorInfo) (*syntax.Node, error) {
	target := p.anchors[a.name]
	if target == nil {
		return nil, errAt(a.start, "unknown or forward alias "+a.name)
	}
	return p.arena.node(syntax.Node{
		Kind: syntax.Kind("yaml-alias"), Start: a.start, End: a.end, Style: "alias",
		Meta: map[string]any{"aliasName": a.name, "aliasTarget": target},
	}), nil
}
func pending(n *syntax.Node) (anchorInfo, bool) {
	a, ok := n.Meta["pendingAnchor"].(anchorInfo)
	return a, ok
}

type aliasStep struct {
	key    string
	index  int
	object bool
}

func aliasPath(n *syntax.Node) (*syntax.Node, []aliasStep) {
	var rev []aliasStep
	for current := n; current != nil; current = current.Parent {
		if _, ok := current.Meta["aliasName"]; ok {
			path := make([]aliasStep, len(rev))
			for i := range rev {
				path[len(rev)-1-i] = rev[i]
			}
			return current, path
		}
		parent := current.Parent
		if parent == nil {
			break
		}
		if parent.Kind == syntax.Object {
			e := parent.Entry(current)
			if e == nil {
				return nil, nil
			}
			rev = append(rev, aliasStep{key: e.Key, object: true})
		} else {
			rev = append(rev, aliasStep{index: parent.Index(current)})
		}
	}
	return nil, nil
}
func aliasEdit(src []byte, n *syntax.Node, op, key string, value any, policy syntax.Style) ([]syntax.Edit, bool, error) {
	root, path := aliasPath(n)
	if root == nil || op == "delete" && len(path) == 0 {
		return nil, false, nil
	}
	tree := root.Interface()
	var mutate func(any, []aliasStep) (any, error)
	mutate = func(current any, path []aliasStep) (any, error) {
		if len(path) == 0 {
			switch op {
			case "replace":
				return value, nil
			case "insert":
				switch x := current.(type) {
				case map[string]any:
					if _, exists := x[key]; exists {
						return nil, fmt.Errorf("yaml: mapping key already exists")
					}
					x[key] = value
					return x, nil
				case []any:
					if key != "" && key != "-" && key != fmt.Sprint(len(x)) {
						return nil, fmt.Errorf("yaml: array insertion only supports append")
					}
					return append(x, value), nil
				}
				return nil, fmt.Errorf("yaml: cannot insert into scalar")
			}
			return nil, fmt.Errorf("yaml: invalid alias operation")
		}
		s := path[0]
		if s.object {
			m := current.(map[string]any)
			if len(path) == 1 && op == "delete" {
				delete(m, s.key)
				return m, nil
			}
			v, e := mutate(m[s.key], path[1:])
			if e != nil {
				return nil, e
			}
			m[s.key] = v
			return m, nil
		}
		a := current.([]any)
		if len(path) == 1 && op == "delete" {
			return append(a[:s.index], a[s.index+1:]...), nil
		}
		v, e := mutate(a[s.index], path[1:])
		if e != nil {
			return nil, e
		}
		a[s.index] = v
		return a, nil
	}
	changed, e := mutate(tree, path)
	if e != nil {
		return nil, true, e
	}
	text, e := encode(changed)
	if !policy.IsAuto() || op == "insert" {
		s := styleAt(src, root)
		s.policy = policy
		s = s.overrides()
		text, _, e = s.render(src, changed, root, len(sourceIndent(src, root.Start))+s.step, true)
	}

	if e != nil {
		return nil, true, e
	}
	return []syntax.Edit{{Start: root.Start, End: root.End, Text: []byte(text)}}, true, nil
}
