package tql

import (
	"fmt"
	"strconv"

	"github.com/vasileknik76/tql/internal/query"
	"github.com/vasileknik76/tql/syntax"
)

func (d *Document) apply(edits []syntax.Edit) error {
	if len(edits) == 0 {
		return nil
	}
	src, err := syntax.Apply(d.source, edits)
	if err != nil {
		return err
	}
	root, err := parseOwned(d.format, src)
	if err != nil {
		return fmt.Errorf("format produced invalid output: %w", err)
	}
	if root == nil {
		return fmt.Errorf("format returned a nil tree")
	}
	syntax.Link(root)
	d.source = src
	d.root = root
	return nil
}

type creation struct {
	owner  *binding
	parent address
	steps  []query.Step
}

func build(steps []query.Step, v any) (any, error) {
	if len(steps) == 0 {
		return v, nil
	}
	tail, e := build(steps[1:], v)
	if e != nil {
		return nil, e
	}
	s := steps[0]
	if s.Kind == "key" {
		return map[string]any{s.Key: tail}, nil
	}
	if s.Index != 0 {
		return nil, fmt.Errorf("cannot create sparse array at index %d", s.Index)
	}
	return []any{tail}, nil
}
func (d *Document) createFrom(owner *binding, n *syntax.Node, steps []query.Step, v any) error {
	for i, s := range steps {
		if n.Kind == syntax.Null {
			nv, e := build(steps[i:], v)
			if e != nil {
				return e
			}
			return d.mutateOwned(owner, "replace", n, "", nv)
		}
		var child *syntax.Node
		key := s.Key
		if s.Kind == "key" {
			if n.Kind != syntax.Object {
				return fmt.Errorf("cannot create field %q on %s", key, n.Kind)
			}
			child = n.Get(key)
		} else {
			if n.Kind != syntax.Array {
				return fmt.Errorf("cannot index %s", n.Kind)
			}
			idx := s.Index
			if idx < 0 {
				idx += len(n.Items)
			}
			if idx < 0 || idx > len(n.Items) {
				return fmt.Errorf("array index %d out of bounds", s.Index)
			}
			key = strconv.Itoa(idx)
			if idx < len(n.Items) {
				child = n.Items[idx]
			}
		}
		if child == nil {
			nv, e := build(steps[i+1:], v)
			if e != nil {
				return e
			}
			return d.mutateOwned(owner, "insert", n, key, nv)
		}
		n = child
	}
	return nil
}

// mutate verifies both parse validity and the complete expected semantic tree.
// A faulty extension cannot silently modify a sibling or insert into a wrong table.
func (d *Document) mutate(op string, n *syntax.Node, key string, v any) error {
	a, err := addr(d.root, n)
	if err != nil {
		return err
	}

	var edits []syntax.Edit
	switch op {
	case "replace":
		edits, err = d.format.Replace(d.source, d.root, n, v)
	case "insert":
		edits, err = d.format.Insert(d.source, d.root, n, key, v)
	case "delete":
		edits, err = d.format.Delete(d.source, d.root, n)
	default:
		return fmt.Errorf("unknown mutation %q", op)
	}
	if err != nil {
		return err
	}
	expected := expectedChange(d.root, op, a, key, v)
	if err = d.apply(edits); err != nil {
		return err
	}
	if !query.EqualNodes(expected, d.root) {
		return fmt.Errorf("format edit changed an unexpected value; transaction aborted")
	}
	if d.state != nil {
		d.state.rebase(nil, op, a)
	}
	return nil
}
func change(v any, a address, fn func(any) any) any {
	if len(a) == 0 {
		return fn(v)
	}
	s := a[0]
	if s.array {
		xs := v.([]any)
		xs[s.index] = change(xs[s.index], a[1:], fn)
		return xs
	}
	m := v.(map[string]any)
	m[s.key] = change(m[s.key], a[1:], fn)
	return m
}
func (d *Document) mutateOwned(owner *binding, op string, n *syntax.Node, key string, v any) error {
	if err := writable(owner); err != nil {
		return err
	}
	if owner == nil {
		return d.mutate(op, n, key, v)
	}
	a, err := addr(owner.value, n)
	if err != nil {
		return err
	}
	updated := semanticChange(owner.value.Interface(), op, a, key, v)
	owner.value = query.Node(updated)
	d.state.rebase(owner, op, a)
	return nil
}
func semanticChange(tree any, op string, a address, key string, v any) any {
	switch op {
	case "replace":
		return change(tree, a, func(any) any { return v })
	case "insert":
		return change(tree, a, func(old any) any {
			if m, ok := old.(map[string]any); ok {
				m[key] = v
				return m
			}
			return append(old.([]any), v)
		})
	case "delete":
		if len(a) == 0 {
			return nil
		}
		last := a[len(a)-1]
		return change(tree, a[:len(a)-1], func(old any) any {
			if last.array {
				items := old.([]any)
				return append(items[:last.index], items[last.index+1:]...)
			}
			m := old.(map[string]any)
			delete(m, last.key)
			return m
		})
	}
	return tree
}

// Independent replacement edits share one source coordinate system. Parse and
// semantic verification happen once per batch; overlapping format envelopes
// (for example several descendants of the same YAML alias) use sequential edits.
func (d *Document) replaceMany(updates []assignment) error {
	if len(updates) > 1 {
		applied, err := d.replaceBatch(updates)
		if err != nil || applied {
			return err
		}
	}
	for _, u := range updates {
		n := locate(d.state.root(u.owner), u.path)
		if n == nil {
			return fmt.Errorf("assignment target disappeared")
		}
		if err := d.mutateOwned(u.owner, "replace", n, "", u.value); err != nil {
			return err
		}
	}
	return nil
}

// replaceBatch commits only when all format edits can share one source snapshot.
// A false result leaves the document untouched so sequential processing can
// handle overlapping format envelopes or structural dependencies.
func (d *Document) replaceBatch(updates []assignment) (bool, error) {
	for _, u := range updates {
		if u.owner != nil {
			return false, nil
		}
	}
	var edits []syntax.Edit
	expected := d.root.Interface()
	for _, u := range updates {
		n := locate(d.root, u.path)
		planned, err := d.format.Replace(d.source, d.root, n, u.value)
		if err != nil {
			return false, err
		}
		edits = append(edits, planned...)
		expected = semanticChange(expected, "replace", u.path, "", u.value)
	}
	source, err := syntax.Apply(d.source, edits)
	if err != nil {
		return false, nil //nolint:nilerr // A rejected batch falls back to sequential edits.
	}
	root, err := d.format.Parse(source)
	if err != nil || root == nil {
		return false, nil //nolint:nilerr // Invalid batch output falls back to sequential edits.
	}
	syntax.Link(root)
	if !query.EqualNodeValue(root, expected) {
		return false, nil
	}
	d.source, d.root = source, root
	for _, u := range updates {
		d.state.rebase(nil, "replace", u.path)
	}
	return true, nil
}

// Deletions with disjoint format envelopes can also share a parse. If their
// separators overlap, or removing the final item needs new container syntax,
// fall back to the existing ordered operations without changing any state.
func (d *Document) deleteBatch(paths []address) (bool, error) {
	var edits []syntax.Edit
	expected := d.root.Interface()
	for _, path := range paths {
		n := locate(d.root, path)
		planned, err := d.format.Delete(d.source, d.root, n)
		if err != nil {
			return false, err
		}
		edits = append(edits, planned...)
		expected = semanticChange(expected, "delete", path, "", nil)
	}
	source, err := syntax.Apply(d.source, edits)
	if err != nil {
		return false, nil //nolint:nilerr // A rejected batch falls back to sequential edits.
	}
	root, err := d.format.Parse(source)
	if err != nil || root == nil {
		return false, nil //nolint:nilerr // Invalid batch output falls back to sequential edits.
	}
	syntax.Link(root)
	if !query.EqualNodeValue(root, expected) {
		return false, nil
	}
	d.source, d.root = source, root
	for _, path := range paths {
		d.state.rebase(nil, "delete", path)
	}
	return true, nil
}
