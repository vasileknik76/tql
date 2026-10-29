package tql

import (
	"bytes"
	"errors"

	jsonformat "github.com/vasileknik76/tql/formats/json"
	"github.com/vasileknik76/tql/syntax"
)

// Parse copies source and parses it using a registered format name.
func Parse(source []byte, format string, options ...ParseOption) (*Document, error) {
	return defaultRegistry.Parse(source, format, options...)
}

// ParseWith uses an explicit backend without modifying a registry.
func ParseWith(source []byte, format Format, options ...ParseOption) (*Document, error) {
	if format == nil {
		return nil, errors.New("nil format")
	}
	format, err := configuredFormat(format, options)
	if err != nil {
		return nil, err
	}
	src := bytes.Clone(source)
	root, err := parseOwned(format, src)
	if err != nil {
		return nil, err
	}
	if root == nil {
		return nil, errors.New("format returned a nil tree")
	}
	syntax.Link(root)
	return &Document{source: src, root: root, format: format}, nil
}

// parseOwned lets the built-in JSON backend reference the document's immutable
// copy instead of copying every string out of it. The check is deliberately an
// exact type test: a wrapper that embeds the backend and overrides Parse must
// keep control of parsing.
func parseOwned(format Format, src []byte) (*syntax.Node, error) {
	if _, ok := format.(jsonformat.Format); ok {
		return jsonformat.ParseOwned(src)
	}
	return format.Parse(src)
}

// Document owns a parsed configuration and its original source. Create one with
// Parse or ParseWith; the zero value is not ready for use. A Document must not be
// read or modified concurrently without external synchronization.
type Document struct {
	source []byte
	root   *syntax.Node
	format Format
	state  documentState
}

// Bytes returns a copy of the current source.
func (d *Document) Bytes() []byte { return bytes.Clone(d.source) }

// Value returns a detached representation using maps, slices, and scalar values.
func (d *Document) Value() any { return d.root.Interface() }

// Result contains read outputs and the final source. Mutation statements have no
// read output. Bytes and Values are independent of the Document's internal data.
type Result struct {
	Values  []any
	Bytes   []byte
	Changed bool
}

var errProgram = errors.New("nil program")

// Exec compiles and executes a query or script. Any error leaves d unchanged.
func (d *Document) Exec(text string, options ...ExecOption) (*Result, error) {
	p, err := Compile(text)
	if err != nil {
		return nil, err
	}
	return d.Execute(p, options...)
}

// Query evaluates a read-only program and refuses mutation statements.
func (d *Document) Query(text string, options ...ExecOption) ([]any, error) {
	p, err := Compile(text)
	if err != nil {
		return nil, err
	}
	if p.Mutates() {
		return nil, errors.New("query accepts read-only expressions; use Exec for mutations")
	}
	o, err := executionOptions(options)
	if err != nil {
		return nil, err
	}
	r, err := d.executeVM(p, false, o)
	if err != nil {
		return nil, err
	}
	return r.Values, nil
}

// Eval is the one-shot equivalent of Parse followed by Exec.
func Eval(source []byte, format, text string, options ...EvalOption) (*Result, error) {
	var o evalOptions
	for _, option := range options {
		if option == nil {
			return nil, errors.New("nil evaluation option")
		}
		if err := option.applyEval(&o); err != nil {
			return nil, err
		}
	}
	d, err := Parse(source, format, o.parse...)
	if err != nil {
		return nil, err
	}
	p, err := Compile(text)
	if err != nil {
		return nil, err
	}
	return d.executeVM(p, true, o.exec)
}

// Execute runs a compiled program and commits only if every statement succeeds.
// Program may be reused concurrently with other Documents.
func (d *Document) Execute(p *Program, options ...ExecOption) (*Result, error) {
	o, err := executionOptions(options)
	if err != nil {
		return nil, err
	}
	return d.executeVM(p, true, o)
}

// executeVM runs the compiled bytecode on a work copy.
func (d *Document) executeVM(p *Program, wantBytes bool, options execOptions) (*Result, error) {
	if p == nil || p.code == nil {
		return nil, errors.New("nil program")
	}
	if d == nil || d.root == nil || d.format == nil {
		return nil, errors.New("uninitialized document")
	}
	bindings, err := options.bindings(p)
	if err != nil {
		return nil, err
	}
	work := &Document{source: d.source, root: d.root, format: d.format}
	machine := newVM(work, bindings, p.code)
	defer releaseVM(machine)
	work.state = machine
	result := &Result{Values: []any{}}
	if err := machine.run(result); err != nil {
		return nil, err
	}
	if wantBytes {
		result.Bytes = bytes.Clone(work.source)
	}
	result.Changed = !bytes.Equal(d.source, work.source)
	d.source, d.root = work.source, work.root
	return result, nil
}
