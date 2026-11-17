package tql

import (
	"fmt"
	"reflect"

	tomlformat "github.com/vasileknik76/tql/formats/toml"
	"github.com/vasileknik76/tql/syntax"
)

type versionOption struct{ version string }

// WithVersion selects a strict format version. Empty or auto detects the minimum
// version required by the source, starting with the backend's conservative default.
func WithVersion(version string) ParseOption { return versionOption{version} }

func (v versionOption) applyParse(o *parseOptions) error {
	o.version = v.version
	o.versionSet = true
	return nil
}

func (v versionOption) applyEval(o *evalOptions) error {
	o.parse = append(o.parse, v)
	return nil
}

func configureVersion(format Format, version string) (Format, error) {
	f, ok := format.(syntax.VersionedFormat)
	if !ok {
		if version == "" || version == "auto" {
			return format, nil
		}
		return nil, fmt.Errorf("format does not support explicit versions")
	}
	configured, err := f.WithVersion(version)
	if err != nil {
		return nil, err
	}
	if configured == nil {
		return nil, fmt.Errorf("format returned a nil versioned backend")
	}
	return configured, nil
}

func versionPreservesFormat(original, configured Format) bool {
	switch original.(type) {
	case tomlformat.Format, *tomlformat.Format:
		_, ok := configured.(tomlformat.Format)
		return ok
	default:
		return reflect.TypeOf(original) == reflect.TypeOf(configured)
	}
}
