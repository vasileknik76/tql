package tql

import (
	"fmt"

	"github.com/vasileknik76/tql/syntax"
)

// Style controls newly generated YAML and TOML syntax. Its zero value is auto.
type Style = syntax.Style

type parseOptions struct {
	style      Style
	styleSet   bool
	version    string
	versionSet bool
}
type styleOption struct{ style Style }

// WithStyle sets a document's generation policy. It does not reformat source.
// Each call replaces the previous policy. Options are document-local.
func WithStyle(style Style) ParseOption {
	return styleOption{style: style}
}
func (s styleOption) applyParse(o *parseOptions) error {
	o.style = s.style
	o.styleSet = true
	return nil
}
func (s styleOption) applyEval(o *evalOptions) error {
	o.parse = append(o.parse, s)
	return nil
}

func configuredFormat(format Format, options []ParseOption) (Format, error) {
	if len(options) == 0 {
		return format, nil
	}
	var o parseOptions
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("nil parse option")
		}
		if err := option.applyParse(&o); err != nil {
			return nil, err
		}
	}
	return o.configure(format)
}

func (o parseOptions) configure(format Format) (Format, error) {
	if o.versionSet {
		configured, err := configureVersion(format, o.version)
		if err != nil {
			return nil, err
		}
		if !versionPreservesFormat(format, configured) {
			return nil, fmt.Errorf("%T.WithVersion returned %T; wrappers must return their own backend type", format, configured)
		}
		format = configured
	}
	if !o.styleSet {
		return format, nil
	}
	f, ok := format.(syntax.StyledFormat)
	if !ok {
		if o.style.IsAuto() {
			return format, nil
		}
		return nil, fmt.Errorf("format does not support explicit styles")
	}
	configured, err := f.WithStyle(o.style)
	if err != nil {
		return nil, err
	}
	if configured == nil {
		return nil, fmt.Errorf("format returned a nil styled backend")
	}
	return configured, nil
}
