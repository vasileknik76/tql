package tql_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/vasileknik76/tql"
	jsonformat "github.com/vasileknik76/tql/formats/json"
	"github.com/vasileknik76/tql/formats/toml"
	"github.com/vasileknik76/tql/formats/yaml"
)

func BenchmarkCompile(b *testing.B) {
	const query = `services[name == "api"].replicas |= . + 1; del(debug); services[].name`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := tql.Compile(query); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkSource() []byte {
	var source strings.Builder
	source.WriteString("{\n  \"services\": [\n")
	for i := range 1000 {
		if i > 0 {
			source.WriteString(",\n")
		}
		fmt.Fprintf(&source, `    {"name":"service-%d","port":%d,"enabled":true}`, i, 8000+i)
	}
	source.WriteString("\n  ]\n}\n")
	return []byte(source.String())
}

func BenchmarkCompiledRead(b *testing.B) {
	source := benchmarkSource()
	d, err := tql.Parse(source, "json")
	if err != nil {
		b.Fatal(err)
	}
	p, err := tql.Compile(`services[name == "service-500"].port`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := d.Execute(p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEvalSurgicalEdit(b *testing.B) {
	source := benchmarkSource()
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tql.Eval(source, "json", `services[500].port = 9999`); err != nil {
			b.Fatal(err)
		}
	}
}

func wideSource(size int) []byte {
	var source strings.Builder
	source.WriteByte('{')
	for i := range size {
		if i != 0 {
			source.WriteByte(',')
		}
		fmt.Fprintf(&source, `"key%d":%d`, i, i)
	}
	source.WriteByte('}')
	return []byte(source.String())
}

func BenchmarkWideObjectRead(b *testing.B) {
	source := wideSource(4096)
	doc, err := tql.Parse(source, "json")
	if err != nil {
		b.Fatal(err)
	}
	program, err := tql.Compile(`key4095`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := doc.Execute(program); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLargeMultiTargetEdit(b *testing.B) {
	source := wideSource(256)
	program, err := tql.Compile(`.[] += 1`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := tql.Parse(source, "json")
		if err != nil {
			b.Fatal(err)
		}
		if _, err := doc.Execute(program); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWideObjectParse(b *testing.B) {
	source := wideSource(4096)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tql.Parse(source, "json"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkYAMLParse(b *testing.B) {
	var source strings.Builder
	source.WriteString("services:\n")
	for i := range 200 {
		fmt.Fprintf(&source, "  - name: svc%d\n    port: %d\n    enabled: %t\n    tags: [a, b, c]\n", i, 8000+i, i%3 != 0)
	}
	data := []byte(source.String())
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tql.Parse(data, "yaml"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScriptForLoop(b *testing.B) {
	var source strings.Builder
	source.WriteString(`{"items":[`)
	for i := range 128 {
		if i > 0 {
			source.WriteByte(',')
		}
		fmt.Fprint(&source, i)
	}
	source.WriteString(`]}`)
	data := []byte(source.String())
	p, err := tql.Compile(`for $x in items[] { $x += 1; }`)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, e := tql.Parse(data, "json")
		if e != nil {
			b.Fatal(e)
		}
		if _, e = doc.Execute(p); e != nil {
			b.Fatal(e)
		}
	}
}

// BenchmarkStyledInsertion isolates backend planning: parsing, applying edits,
// and the engine's semantic verification are deliberately outside the timer.
func BenchmarkStyledInsertion(b *testing.B) {
	for _, format := range []struct {
		name    string
		backend tql.Format
	}{
		{"json", jsonformat.Format{}},
		{"toml", toml.Format{}},
		{"yaml", yaml.Format{}},
	} {
		for _, size := range []int{32, 2048} {
			b.Run(fmt.Sprintf("%s/%d", format.name, size), func(b *testing.B) {
				var text strings.Builder
				if format.name == "json" {
					text.WriteString("{\n")
				}
				for i := range size {
					switch format.name {
					case "json":
						if i > 0 {
							text.WriteString(",\n")
						}
						fmt.Fprintf(&text, `    "key%d" : ["before", "after"]`, i)
					case "toml":
						fmt.Fprintf(&text, "key%d = ['before', 'after'] # keep\n", i)
					case "yaml":
						fmt.Fprintf(&text, "key%d: ['before', 'after'] # keep\n", i)
					}
				}
				if format.name == "json" {
					text.WriteString("\n}\n")
				}
				source := []byte(text.String())
				root, err := format.backend.Parse(source)
				if err != nil {
					b.Fatal(err)
				}
				value := []any{"new", map[string]any{"name": "inserted", "enabled": true}}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := format.backend.Insert(source, root, root, "added", value); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
