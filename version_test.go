package tql_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/vasileknik76/tql"
	"github.com/vasileknik76/tql/edit"
	tomlformat "github.com/vasileknik76/tql/formats/toml"
)

func TestDocumentVersion(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		options      []tql.ParseOption
		wantError    bool
	}{
		{"default", "old=0\n", nil, true},
		{"strict old", "old=0\n", []tql.ParseOption{tql.WithVersion("1.0")}, true},
		{"strict new", "old=0\n", []tql.ParseOption{tql.WithVersion("1.1")}, false},
		{"inferred new", "old=13:37\n", nil, false},
		{"inferred escape", "old=\"\\e\"\n", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := tql.Parse([]byte(tc.source), "toml", tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := doc.Exec("del(old)"); err != nil {
				t.Fatal(err)
			}
			before := doc.Bytes()
			_, err = doc.Apply(edit.Set(edit.Key("first"), 1), edit.Set(edit.Key("time"), tql.Temporal("14:00")))
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "requires TOML 1.1") {
					t.Fatalf("got %v", err)
				}
				if !bytes.Equal(before, doc.Bytes()) {
					t.Fatal("failed edit changed document")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(doc.Bytes(), []byte("14:00")) {
					t.Fatalf("missing time: %s", doc.Bytes())
				}
			}
		})
	}
}

func TestConservativeInlineGeneration(t *testing.T) {
	for _, source := range []string{"x={a=[\n1\n]}\n", "x={a=\"\"\"\ntext\n\"\"\"}\n"} {
		for _, tc := range []struct{ query, want string }{
			{"x.b=2", strings.TrimSuffix(source, "}\n") + ",b=2}\n"},
			{"new={b:2}", source + "new={b=2}\n"},
		} {
			for _, options := range [][]tql.EvalOption{nil, {tql.WithVersion("1.0")}} {
				result, err := tql.Eval([]byte(source), "toml", tc.query, options...)
				if err != nil {
					t.Fatal(err)
				}
				if string(result.Bytes) != tc.want {
					t.Fatalf("got %q; want %q", result.Bytes, tc.want)
				}
			}
		}
	}
}

func TestVersionOptions(t *testing.T) {
	for _, version := range []string{"1.2", "latest", "1.0.0"} {
		if _, err := tql.Parse(nil, "toml", tql.WithVersion(version)); err == nil {
			t.Fatalf("accepted %q", version)
		}
	}
	for _, format := range []string{"json", "yaml"} {
		if _, err := tql.Parse(nil, format, tql.WithVersion("1.0")); err == nil {
			t.Fatalf("accepted version for %s", format)
		}
	}
	for _, options := range [][]tql.ParseOption{
		{tql.WithVersion("1.1"), tql.WithStyle(tql.Style{Keys: "double"})},
		{tql.WithStyle(tql.Style{Keys: "double"}), tql.WithVersion("1.1")},
		{tql.WithVersion("1.0"), tql.WithVersion("1.1")},
	} {
		doc, err := tql.Parse(nil, "toml", options...)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.Apply(edit.Set(edit.Key("time"), tql.Temporal("14:00"))); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := (tomlformat.Format{}).WithVersion("1.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tql.ParseWith([]byte("time=14:00"), backend, tql.WithStyle(tql.Style{})); err == nil {
		t.Fatal("style cleared explicit version")
	}
	if _, err := tql.Eval([]byte("time=14:00"), "toml", ".", tql.WithVersion("1.0")); err == nil {
		t.Fatal("Eval ignored version")
	}
	registry := tql.NewRegistry()
	if err := registry.Register("toml", tomlformat.Format{}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Parse([]byte("time=14:00"), "toml", tql.WithVersion("1.0")); err == nil {
		t.Fatal("registry ignored version")
	}
	styled, err := (tomlformat.Format{}).WithStyle(tql.Style{Keys: "double"})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := tql.ParseWith(nil, styled, tql.WithVersion("1.0"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := doc.Exec("new=1")
	if err != nil || string(result.Bytes) != "\"new\" = 1\n" {
		t.Fatalf("version cleared explicit style: %v, %v", result, err)
	}
	for _, target := range []string{"1.0", "1.1"} {
		result, err := tql.Eval(nil, "toml", "param $time; time=$time", tql.WithVersion(target), tql.WithParams(map[string]any{"time": tql.Temporal("14:00")}))
		if target == "1.0" {
			if err == nil || result != nil {
				t.Fatal("parameter bypassed target version")
			}
		} else if err != nil || !bytes.Contains(result.Bytes, []byte("14:00")) {
			t.Fatalf("1.1 parameter: %v, %v", result, err)
		}
	}
}

type versionedTOMLGuard struct{ guardedTOMLFormat }

func (f *versionedTOMLGuard) WithVersion(version string) (tql.Format, error) {
	backend, err := f.Format.WithVersion(version)
	if err != nil {
		return nil, err
	}
	configured := *f
	configured.Format = backend.(tomlformat.Format)
	return &configured, nil
}

func TestVersionOptionRejectsUnwrappingBackend(t *testing.T) {
	for _, version := range []string{"auto", "1.0", "1.1"} {
		parses := 0
		wrapper := &guardedTOMLFormat{reject: "Insert", parses: &parses}
		if _, err := tql.ParseWith([]byte("a=1\n"), wrapper, tql.WithVersion(version)); err == nil || !strings.Contains(err.Error(), "wrappers must return their own backend type") {
			t.Fatalf("version %s unwrapped the backend: %v", version, err)
		}
		if parses != 0 {
			t.Fatal("invalid backend configuration reached parsing")
		}
	}
}

func TestVersionedWrapperPreservesOverrides(t *testing.T) {
	for _, version := range []string{"auto", "1.0", "1.1"} {
		parses := 0
		wrapper := &versionedTOMLGuard{guardedTOMLFormat{reject: "Insert", parses: &parses}}
		doc, err := tql.ParseWith([]byte("a=1\n"), wrapper, tql.WithVersion(version))
		if err != nil {
			t.Fatal(err)
		}
		if result, err := doc.Exec("b=2"); err == nil || !strings.Contains(err.Error(), "wrapper rejected Insert") || result != nil {
			t.Fatalf("version %s bypassed wrapper: result=%v err=%v", version, result, err)
		}
		if string(doc.Bytes()) != "a=1\n" {
			t.Fatal("rejected insertion changed source")
		}
	}
}

func TestPinnedVersionForPointerAndWrapper(t *testing.T) {
	parses := 0
	for _, backend := range []tql.Format{
		&tomlformat.Format{},
		&versionedTOMLGuard{guardedTOMLFormat{parses: &parses}},
	} {
		doc, err := tql.ParseWith([]byte("clock=14:00\n"), backend)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := doc.Exec("del(clock)"); err != nil {
			t.Fatal(err)
		}
		if _, err := doc.Apply(edit.Set(edit.Key("clock"), tql.Temporal("15:00"))); err != nil {
			t.Fatalf("backend %T lost its pinned version: %v", backend, err)
		}
		if string(doc.Bytes()) != "clock = 15:00\n" {
			t.Fatalf("unexpected output: %q", doc.Bytes())
		}
	}
	if parses != 3 {
		t.Fatalf("wrapper Parse was bypassed: got %d calls, want 3", parses)
	}
}
