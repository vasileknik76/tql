package toml

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vasileknik76/tql/syntax"
)

func TestVersionGolden(t *testing.T) {
	const directory = "testdata/versions"
	files, err := filepath.Glob(filepath.Join(directory, "*.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("missing version golden fixtures")
	}
	for _, file := range files {
		t.Run(strings.TrimSuffix(filepath.Base(file), ".toml"), func(t *testing.T) {
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			for _, version := range []string{"auto", "1.0", "1.1"} {
				if version != "auto" {
					got.WriteByte('\n')
				}
				fmt.Fprintf(&got, "=== %s ===\n", version)
				backend, err := (Format{}).WithVersion(version)
				if err != nil {
					t.Fatal(err)
				}
				root, err := backend.Parse(source)
				if err != nil {
					fmt.Fprintf(&got, "parse error: %v\n", err)
					continue
				}
				target := backend.(syntax.VersionedFormat).Version(root)
				fmt.Fprintf(&got, "target: %s\n", target)
				backend, err = backend.(syntax.VersionedFormat).WithVersion(target)
				if err != nil {
					t.Fatal(err)
				}
				for _, edit := range []struct {
					label, key string
					value      any
				}{
					{"insert object", "added", map[string]any{"enabled": false}},
					{"insert time without seconds", "added_time", syntax.Temporal("14:00")},
				} {
					fmt.Fprintf(&got, "--- %s ---\n", edit.label)
					edits, err := backend.Insert(source, root, root, edit.key, edit.value)
					if err != nil {
						if len(edits) != 0 {
							t.Fatal("failed insertion returned partial edits")
						}
						fmt.Fprintf(&got, "error: %v\n", err)
						continue
					}
					output, err := syntax.Apply(source, edits)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := backend.Parse(output); err != nil {
						t.Fatalf("output violates target %s: %v", target, err)
					}
					got.Write(output)
				}
			}
			want, err := os.ReadFile(strings.TrimSuffix(file, ".toml") + ".golden")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal([]byte(got.String()), want) {
				t.Fatalf("version golden differs for %s\nwant:\n%s\ngot:\n%s", file, want, got.String())
			}
			unchanged, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(source, unchanged) {
				t.Fatal("version checks modified input")
			}
		})
	}
}

func TestVersionFeatures(t *testing.T) {
	for _, tc := range []struct {
		name, source, version string
		feature               feature
	}{
		{"empty", "", "1.0", ""},
		{"ordinary", "a = {b = 1}\ntime = 13:37:00\n", "1.0", ""},
		{"unicode escape", `value = "\u001b\u007f"`, "1.0", ""},
		{"literal escape", `value = '\e\x01'`, "1.0", ""},
		{"escaped slash", `value = "\\e\\x01"`, "1.0", ""},
		{"comment", "# \\e, 13:37, {a=1,}\na=1\n", "1.0", ""},
		{"array trivia", "value = [\n 1, # comment\n]\n", "1.0", ""},
		{"inline array", "value = {a = [\n 1, # comment\n]}\n", "1.0", ""},
		{"inline multiline string", "value = {a = '''\ntext\n'''}\n", "1.0", ""},
		{"escape", `value = "\e"`, "1.1", escapeEscape},
		{"hex", `value = "\xFF"`, "1.1", hexEscape},
		{"key", `"\x41" = 1`, "1.1", hexEscape},
		{"header", `["\e"]`, "1.1", escapeEscape},
		{"multiline escape", "value = \"\"\"\n\\e\"\"\"", "1.1", escapeEscape},
		{"time", "value = 13:37", "1.1", optionalSeconds},
		{"date", "value = 1979-05-27", "1.0", ""},
		{"local datetime", "value = 1979-05-27 07:32", "1.1", optionalSeconds},
		{"UTC datetime", "value = 1979-05-27t07:32z", "1.1", optionalSeconds},
		{"offset datetime", "value = 1979-05-27T07:32-07:00", "1.1", optionalSeconds},
		{"inline newline", "value = {\n a=1}", "1.1", inlineNewline},
		{"inline comment", "value = {# comment\n a=1}", "1.1", inlineComment},
		{"inline trailing comma", "value = {a=1,}", "1.1", inlineTrailingComma},
		{"nested trailing comma", "value = {a={b=1,}}", "1.1", inlineTrailingComma},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, target := range []string{"auto", "1.0", "1.1"} {
				backend, err := (Format{}).WithVersion(target)
				if err != nil {
					t.Fatal(err)
				}
				root, err := backend.Parse([]byte(tc.source))
				if target == "1.0" && tc.feature != "" {
					if err == nil || root != nil || !strings.Contains(err.Error(), string(tc.feature)) || !strings.Contains(err.Error(), "target is 1.0") {
						t.Fatalf("strict validation: root=%v err=%v", root, err)
					}
					continue
				}
				if err != nil {
					t.Fatalf("%s: %v", target, err)
				}
				want := target
				if target == "auto" {
					want = tc.version
				}
				if got := backend.(syntax.VersionedFormat).Version(root); got != want {
					t.Fatalf("%s: got version %s, want %s", target, got, want)
				}
			}
		})
	}
}

func TestVersionGeneration(t *testing.T) {
	for _, tc := range []struct {
		name, source, version string
		value                 any
		wantError             bool
	}{
		{"default", "a=0\n", "auto", syntax.Temporal("13:37"), true},
		{"explicit old", "a=0\n", "1.0", syntax.Temporal("13:37"), true},
		{"explicit new", "a=0\n", "1.1", syntax.Temporal("13:37"), false},
		{"inferred new", "a=0\nb=13:37\n", "auto", syntax.Temporal("14:00"), false},
		{"nested array", "a=0\n", "auto", []any{syntax.Temporal("14:00")}, true},
		{"nested object", "a=0\n", "auto", map[string]any{"t": syntax.Temporal("14:00")}, true},
		{"seconds", "a=0\n", "auto", syntax.Temporal("14:00:00"), false},
		{"date", "a=0\n", "auto", syntax.Temporal("1979-05-27"), false},
		{"control string", "a=0\n", "auto", "\x1b\x7f\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, err := (Format{}).WithVersion(tc.version)
			if err != nil {
				t.Fatal(err)
			}
			root, err := backend.Parse([]byte(tc.source))
			if err != nil {
				t.Fatal(err)
			}
			for _, replace := range []bool{false, true} {
				var edits []syntax.Edit
				if replace {
					edits, err = backend.Replace([]byte(tc.source), root, root.Get("a"), tc.value)
				} else {
					edits, err = backend.Insert([]byte(tc.source), root, root, "new", tc.value)
				}
				if tc.wantError {
					if err == nil || len(edits) != 0 {
						t.Fatalf("expected version error, got %v", err)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				out, err := syntax.Apply([]byte(tc.source), edits)
				if err != nil {
					t.Fatal(err)
				}
				strict, err := (Format{}).WithVersion(backend.(syntax.VersionedFormat).Version(root))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := strict.Parse(out); err != nil {
					t.Fatalf("invalid output: %s: %v", out, err)
				}
			}
		})
	}
}

func TestVersionBackendStyleIsolation(t *testing.T) {
	for _, version := range []string{"auto", "1.0", "1.1"} {
		t.Run(version, func(t *testing.T) {
			original, err := (Format{}).WithVersion(version)
			if err != nil {
				t.Fatal(err)
			}
			quoted, err := original.(syntax.StyledFormat).WithStyle(syntax.Style{Keys: "double"})
			if err != nil {
				t.Fatal(err)
			}
			again, err := (Format{}).WithVersion(version)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				backend syntax.Format
				want    string
			}{
				{original, "added = true\n"},
				{quoted, "\"added\" = true\n"},
				{again, "added = true\n"},
			} {
				root, err := tc.backend.Parse(nil)
				if err != nil {
					t.Fatal(err)
				}
				edits, err := tc.backend.Insert(nil, root, root, "added", true)
				if got := apply(t, "", edits, err); got != tc.want {
					t.Fatalf("style leaked between backends: got %q, want %q", got, tc.want)
				}
			}
		})
	}
}
