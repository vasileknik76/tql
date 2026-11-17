package toml

import (
	"fmt"

	"github.com/vasileknik76/tql/syntax"
)

type feature string

const (
	escapeEscape        feature = `\e escape`
	hexEscape           feature = `\xHH escape`
	optionalSeconds     feature = "omitted seconds"
	inlineNewline       feature = "newline in inline table"
	inlineComment       feature = "comment in inline table"
	inlineTrailingComma feature = "trailing comma in inline table"
)

var featureVersions = map[feature]string{
	escapeEscape:        "1.1",
	hexEscape:           "1.1",
	optionalSeconds:     "1.1",
	inlineNewline:       "1.1",
	inlineComment:       "1.1",
	inlineTrailingComma: "1.1",
}

// Default-style backends are immutable and can share their interface boxes.
var (
	autoVersionFormat syntax.Format = Format{}
	version10Format   syntax.Format = Format{version: "1.0"}
	version11Format   syntax.Format = Format{version: "1.1"}
)

// WithVersion selects TOML 1.0 or 1.1 strictly. Empty and auto detect from source.
func (f Format) WithVersion(version string) (syntax.Format, error) {
	var configured syntax.Format
	switch version {
	case "", "auto":
		f.version = ""
		configured = autoVersionFormat
	case "1.0", "1.1":
		f.version = version
		if version == "1.0" {
			configured = version10Format
		} else {
			configured = version11Format
		}
	default:
		return nil, fmt.Errorf("toml: unsupported version %q (expected auto, 1.0, 1.1)", version)
	}
	if f.style.IsAuto() {
		return configured, nil
	}
	return f, nil
}

// Version returns the explicit target or the source's minimum version (default 1.0).
func (f Format) Version(root *syntax.Node) string {
	if f.version != "" {
		return f.version
	}
	if root != nil {
		if version, ok := root.Meta["version"].(string); ok {
			return version
		}
	}
	return "1.0"
}

func (p *parser) useFeature(feature feature) error {
	version := featureVersions[feature]
	if p.version != "" && p.version < version {
		return p.err(fmt.Sprintf("%s requires TOML %s; target is %s", feature, version, p.version))
	}
	if p.minimum < version {
		p.minimum = version
	}
	return nil
}

func (p *parser) inlineTrivia(n *syntax.Node) error {
	for {
		p.spaces()
		if p.peek() == '#' {
			if err := p.useFeature(inlineComment); err != nil {
				return err
			}
			if err := p.comment(); err != nil {
				return err
			}
		}
		if p.peek() != '\n' && p.peek() != '\r' {
			return nil
		}
		if err := p.useFeature(inlineNewline); err != nil {
			return err
		}
		if !p.newline() {
			return p.err("expected newline")
		}
		n.Meta["multiline"] = true
	}
}

func omittedSeconds(s string) bool {
	clock := 0
	if len(s) > 10 {
		clock = 11
	}
	return len(s) >= clock+5 && s[clock+2] == ':' && (len(s) == clock+5 || s[clock+5] != ':')
}

func validateValueVersion(value any, version string, depth int) error {
	if depth > 256 {
		return fmt.Errorf("TOML value nesting exceeds 256 levels")
	}
	switch v := value.(type) {
	case syntax.Temporal:
		if omittedSeconds(string(v)) && version < featureVersions[optionalSeconds] {
			return fmt.Errorf("toml: %s requires TOML %s; target is %s", optionalSeconds, featureVersions[optionalSeconds], version)
		}
	case []any:
		for _, item := range v {
			if err := validateValueVersion(item, version, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range v {
			if err := validateValueVersion(item, version, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}
