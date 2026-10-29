package tql_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/vasileknik76/tql"
	jsonformat "github.com/vasileknik76/tql/formats/json"
	"github.com/vasileknik76/tql/syntax"
)

func document(t *testing.T, source, format string) *tql.Document {
	t.Helper()
	d, err := tql.Parse([]byte(source), format)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func exec(t *testing.T, d *tql.Document, query string) *tql.Result {
	t.Helper()
	r, err := d.Exec(query)
	if err != nil {
		t.Fatalf("Exec(%q): %v", query, err)
	}
	return r
}

func assertQuery(t *testing.T, d *tql.Document, query string, want ...any) {
	t.Helper()
	got, err := d.Query(query)
	if err != nil {
		t.Fatalf("Query(%q): %v", query, err)
	}
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Query(%q) = %#v; want %#v", query, got, want)
	}
}

func TestUserExamples(t *testing.T) {
	d := document(t, `{"foo":{"bar":0,"keep":true},"baz":[{"name":0},{"name":0}],"obj":null,"objs":[{"name":"foo"},{"name":"bar"},{"name":"foo"}]}`, "json")
	exec(t, d, `foo.bar = 1; baz[1].name = 3; obj = [1,2,3]; objs[name == "foo"] = "abc"; del(foo.bar)`)
	assertQuery(t, d, `foo.bar`)
	assertQuery(t, d, `foo.keep`, true)
	assertQuery(t, d, `baz[1].name`, tql.Number("3"))
	assertQuery(t, d, `obj`, []any{tql.Number("1"), tql.Number("2"), tql.Number("3")})
	assertQuery(t, d, `objs`, []any{"abc", map[string]any{"name": "bar"}, "abc"})
}

func TestBytePerfectScalarChanges(t *testing.T) {
	cases := []struct{ format, source, query, want string }{
		{"json", "\t{\r\n  \"é\" : [1, 2.00e0],\r\n \"keep\":\"\\u0061\"\r\n}\t", `.["é"][1] = 9007199254740993`, "\t{\r\n  \"é\" : [1, 9007199254740993],\r\n \"keep\":\"\\u0061\"\r\n}\t"},
		{"toml", "# heading\r\n[server] # table\r\nport  =  80 # comment\r\nname = 'Привет'\r\n", "server.port = 8080", "# heading\r\n[server] # table\r\nport  =  8080 # comment\r\nname = 'Привет'\r\n"},
		{"yaml", "# heading\r\nserver:\r\n  port: 80 # comment\r\n  name: 'Привет'\r\n", "server.port = 8080", "# heading\r\nserver:\r\n  port: 8080 # comment\r\n  name: 'Привет'\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			d := document(t, tc.source, tc.format)
			r := exec(t, d, tc.query)
			if string(r.Bytes) != tc.want || string(d.Bytes()) != tc.want {
				t.Fatalf("bytes = %q; want %q", r.Bytes, tc.want)
			}
			if !r.Changed {
				t.Fatal("mutation was not marked changed")
			}
		})
	}
}

func TestNoOpPreservesAllBytes(t *testing.T) {
	cases := []struct{ format, source, query string }{
		{"json", " {\r\n  \"a\" : 1.000e0, \"s\":\"\\u0061\",\"v\":[1.0,2e0]\r\n}\t", `a = 1; s = "a"; v = [1,2]; del(missing); missing[true] = 7`},
		{"toml", "# x\r\na = 1_000 # y\r\ns = 'abc'\r\n", `a = 1000; s = "abc"; del(missing)`},
		{"yaml", "# x\r\na: 1.00 # y\r\ns: 'abc'\r\n", `a = 1; s = "abc"; del(missing)`},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			d := document(t, tc.source, tc.format)
			r := exec(t, d, tc.query)
			if r.Changed || string(r.Bytes) != tc.source || string(d.Bytes()) != tc.source {
				t.Fatalf("no-op rewrote source: %q", r.Bytes)
			}
		})
	}
}

func TestSelections(t *testing.T) {
	d := document(t, `{"a":[0,1,2,3,4],"items":[{"name":"foo","x":1},{"name":"bar","x":2},{"name":"foo","x":3}],"map":{"z":1,"a":2},"odd.key":{"hyphen-key":"😀"}}`, "json")
	assertQuery(t, d, `a[-1]`, tql.Number("4"))
	assertQuery(t, d, `a[1:4]`, tql.Number("1"), tql.Number("2"), tql.Number("3"))
	assertQuery(t, d, `a[-3:-1]`, tql.Number("2"), tql.Number("3"))
	assertQuery(t, d, `a[:2]`, tql.Number("0"), tql.Number("1"))
	assertQuery(t, d, `a[3:]`, tql.Number("3"), tql.Number("4"))
	assertQuery(t, d, `a[99:100]`)
	assertQuery(t, d, `items[name == "foo" and x > 1].x`, tql.Number("3"))
	assertQuery(t, d, `items[] | select(.name == "foo") | .x`, tql.Number("1"), tql.Number("3"))
	assertQuery(t, d, `map.*`, tql.Number("1"), tql.Number("2"))
	assertQuery(t, d, `.["odd.key"]["hyphen-key"]`, "😀")
	exec(t, d, `a[1:3] += 10; items[name == "foo"].x *= 2`)
	assertQuery(t, d, `a[]`, tql.Number("0"), tql.Number("11"), tql.Number("12"), tql.Number("3"), tql.Number("4"))
	assertQuery(t, d, `items[].x`, tql.Number("2"), tql.Number("2"), tql.Number("6"))
}

func TestDeleteManyAdjacentTargets(t *testing.T) {
	cases := []struct{ source, query, want string }{
		{`[0, 1, 2, 3, 4]`, `del(.[1:4])`, `[0,    4]`},
		{`[0, 1, 2]`, `del(.[])`, `[  ]`},
		{`{"a":1,"b":2,"c":3}`, `del(a,b)`, `{"c":3}`},
		{`{"a":{"b":1,"c":2},"z":3}`, `del(a,a.b,a)`, `{"z":3}`},
		{`[0,1,2,3]`, `del(.[1],.[1],.[2])`, `[0,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			d := document(t, tc.source, "json")
			r := exec(t, d, tc.query)
			if string(r.Bytes) != tc.want {
				t.Fatalf("got %q; want %q", r.Bytes, tc.want)
			}
		})
	}
}

func TestSnapshotRightHandSides(t *testing.T) {
	d := document(t, `{"a":[1,2,3],"inc":10}`, "json")
	exec(t, d, `a[] |= . + $.a[2]`)
	assertQuery(t, d, `a[]`, tql.Number("4"), tql.Number("5"), tql.Number("6"))
	exec(t, d, `a[] = $.a[2]`)
	assertQuery(t, d, `a[]`, tql.Number("6"), tql.Number("6"), tql.Number("6"))
	exec(t, d, `a[] += $.inc; inc = 20; a[] |= . + $.inc`)
	assertQuery(t, d, `a[]`, tql.Number("36"), tql.Number("36"), tql.Number("36"))
}

func TestNestedPathCreation(t *testing.T) {
	cases := []struct {
		source, query, path string
		want                any
	}{
		{`{}`, `a.b[0].name = "x"`, `a.b[0].name`, "x"},
		{`{"a":[]}`, `a[0].name = "x"`, `a[0].name`, "x"},
		{`{"a":[1]}`, `a[1] = 2`, `a[1]`, tql.Number("2")},
		{`{"a":null}`, `a.b.name = "x"`, `a.b.name`, "x"},
		{`{"a":null}`, `a[0].name = "x"`, `a[0].name`, "x"},
		{`{}`, `a ??= {b:2}`, `a.b`, tql.Number("2")},
	}
	for _, tc := range cases {
		t.Run(tc.query+tc.source, func(t *testing.T) {
			d := document(t, tc.source, "json")
			exec(t, d, tc.query)
			assertQuery(t, d, tc.path, tc.want)
		})
	}
}

func TestMissingLeavesBehindSelections(t *testing.T) {
	d := document(t, `{"items":[{"name":"foo","x":1},{"name":"foo"},{"name":"bar","x":null},{"name":"foo","x":3}],"fallback":7}`, "json")
	// Existing targets and newly created fields share the original RHS snapshot.
	exec(t, d, `items[name == "foo"].x = $.items[0].x + 1`)
	assertQuery(t, d, `items[].x`, tql.Number("2"), tql.Number("2"), nil, tql.Number("2"))
	exec(t, d, `items[].x ??= $.fallback`)
	assertQuery(t, d, `items[].x`, tql.Number("2"), tql.Number("2"), tql.Number("7"), tql.Number("2"))
	exec(t, d, `items[0:2].nested[0].value = "new"`)
	assertQuery(t, d, `items[].nested[0].value`, "new", "new")
	exec(t, d, `items[name == "absent"].new = 1`)
	assertQuery(t, d, `items[].new`)
}

func TestSelectedUpdatesAcrossFormats(t *testing.T) {
	cases := []struct{ format, source string }{
		{"toml", "# keep\nnums = [1, 2, 3]\n[[items]]\nname = 'foo'\nx = 1 # inline\n[[items]]\nname = \"bar\"\nx = 2\n"},
		{"yaml", "# keep\nnums: [1, 2, 3]\nitems:\n  - name: 'foo'\n    x: 1 # inline\n  - name: bar\n    x: 2\n"},
	}
	for _, tc := range cases {
		t.Run(tc.format, func(t *testing.T) {
			d := document(t, tc.source, tc.format)
			r := exec(t, d, `nums[1] += 10; items[name == "foo"].x = 9`)
			want := strings.Replace(tc.source, "[1, 2, 3]", "[1, 12, 3]", 1)
			want = strings.Replace(want, "1 # inline", "9 # inline", 1)
			if string(r.Bytes) != want {
				t.Fatalf("got %q; want %q", r.Bytes, want)
			}
			assertQuery(t, d, `nums[1]`, tql.Number("12"))
			assertQuery(t, d, `items[].x`, tql.Number("9"), tql.Number("2"))
		})
	}
}

func TestTOMLTemporalToStringChangesType(t *testing.T) {
	source := "dob = 1979-05-27 # keep\n"
	d := document(t, source, "toml")
	assertQuery(t, d, `type(dob)`, "datetime")
	r := exec(t, d, `dob = "1979-05-27"`)
	if string(r.Bytes) != "dob = \"1979-05-27\" # keep\n" || !r.Changed {
		t.Fatalf("type-changing assignment did not update source: %q", r.Bytes)
	}
	assertQuery(t, d, `type(dob)`, "string")
	assertQuery(t, d, `dob`, "1979-05-27")
}

func TestAtomicRollback(t *testing.T) {
	cases := []string{
		`a = 2; b = 1 / 0`,
		`a = 2; b = 1 / 3`,
		`a = 2; a.x = 1`,
		`a = 2; arr[4] = 1`,
		`a = 2; missing.x[1] = 1`,
		`a = 2; arr = arr[]`,
		`a = 2; del(.)`,
		`a = 2; (a + 1) = 3`,
	}
	for _, q := range cases {
		t.Run(q, func(t *testing.T) {
			source := `{"a":1,"arr":[1,2]}`
			d := document(t, source, "json")
			if _, err := d.Exec(q); err == nil {
				t.Fatal("expected execution failure")
			}
			if string(d.Bytes()) != source {
				t.Fatalf("failed transaction changed source: %q", d.Bytes())
			}
			assertQuery(t, d, `a`, tql.Number("1"))
		})
	}
}

func TestExactNumbers(t *testing.T) {
	d := document(t, `{"big":9007199254740993,"precise":0.1,"exp":1e30,"keep":-0.0000e+0}`, "json")
	assertQuery(t, d, `big`, tql.Number("9007199254740993"))
	assertQuery(t, d, `big > 9007199254740992`, true)
	assertQuery(t, d, `precise + 0.2`, tql.Number("0.3"))
	assertQuery(t, d, `10 / 4`, tql.Number("2.5"))
	assertQuery(t, d, `exp + 1`, tql.Number("1000000000000000000000000000001"))
	exec(t, d, `big += 1; precise *= 3; keep = 0`)
	assertQuery(t, d, `big`, tql.Number("9007199254740994"))
	assertQuery(t, d, `precise`, tql.Number("0.3"))
	if !bytes.Contains(d.Bytes(), []byte(`"keep":-0.0000e+0`)) {
		t.Fatal("numeric no-op changed spelling")
	}
	encoded, err := json.Marshal(d.Value())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"big":9007199254740994`)) {
		t.Fatalf("encoding/json lost numeric type or precision: %s", encoded)
	}
}

func TestReadFunctionsAndExpressions(t *testing.T) {
	d := document(t, `{"a":[3,1,2],"s":"é😀","obj":{"z":1,"a":2},"n":null}`, "json")
	assertQuery(t, d, `a | sort`, []any{tql.Number("1"), tql.Number("2"), tql.Number("3")})
	assertQuery(t, d, `a | reverse`, []any{tql.Number("2"), tql.Number("1"), tql.Number("3")})
	assertQuery(t, d, `a | first`, tql.Number("3"))
	assertQuery(t, d, `a | last`, tql.Number("2"))
	assertQuery(t, d, `length(s)`, tql.Number("2"))
	assertQuery(t, d, `obj | keys`, []any{"a", "z"})
	assertQuery(t, d, `obj | values`, []any{tql.Number("2"), tql.Number("1")})
	assertQuery(t, d, `has(obj,"a"); contains(a,2); startswith(s,"é"); endswith(s,"😀"); test(s,"^é"); type(n)`, true, true, true, true, true, "null")
	assertQuery(t, d, `missing ?? 7; false ?? 7; n ?? "default"`, tql.Number("7"), false, "default")
	assertQuery(t, d, `false and (1 / 0); true or (1 / 0)`, false, true)
	assertQuery(t, d, `{sum: 1 + 2 * 3, names:[s, "x"], positive: not false}`, map[string]any{"sum": tql.Number("7"), "names": []any{"é😀", "x"}, "positive": true})
}

func TestReadOnlyAndOwnership(t *testing.T) {
	source := []byte(`{"a":[{"x":1}]}`)
	d, err := tql.Parse(source, "json")
	if err != nil {
		t.Fatal(err)
	}
	source[0] = '!'
	if _, err = d.Query(`a = []`); err == nil {
		t.Fatal("Query accepted mutation")
	}
	assertQuery(t, d, `a[0].x`, tql.Number("1"))
	copyBytes := d.Bytes()
	copyBytes[0] = '!'
	copyValue := d.Value().(map[string]any)
	copyValue["a"].([]any)[0].(map[string]any)["x"] = tql.Number("99")
	r := exec(t, d, `a`)
	r.Values[0].([]any)[0].(map[string]any)["x"] = tql.Number("88")
	r.Bytes[0] = '!'
	assertQuery(t, d, `a[0].x`, tql.Number("1"))
	if d.Bytes()[0] != '{' {
		t.Fatal("returned bytes alias Document")
	}
}

func TestCompileAndConcurrentReuse(t *testing.T) {
	p, err := tql.Compile(`a += 2; a`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Mutates() {
		t.Fatal("compiled mutation not detected")
	}
	read, err := tql.Compile(`a | type`)
	if err != nil {
		t.Fatal(err)
	}
	if read.Mutates() {
		t.Fatal("read marked mutation")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := tql.Parse([]byte(`{"a":1}`), "json")
			if err != nil {
				errs <- err
				return
			}
			r, err := d.Execute(p)
			if err != nil {
				errs <- err
				return
			}
			if !reflect.DeepEqual(r.Values, []any{tql.Number("3")}) {
				errs <- fmt.Errorf("got %#v", r.Values)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	var nilProgram *tql.Program
	if nilProgram.Mutates() {
		t.Fatal("nil program mutates")
	}
	if _, err := document(t, `{}`, "json").Execute(nilProgram); err == nil {
		t.Fatal("nil program accepted")
	}
}

type invalidOutputFormat struct {
	jsonformat.Format
	invalidEdits bool
	nilTree      bool
}

type wrongValueFormat struct{ jsonformat.Format }

func (f wrongValueFormat) Replace(source []byte, root, target *syntax.Node, value any) ([]syntax.Edit, error) {
	if n, ok := value.(tql.Number); ok && n == tql.Number("4") {
		return []syntax.Edit{{Start: target.Start, End: target.End, Text: []byte("999")}}, nil
	}
	return f.Format.Replace(source, root, target, value)
}

type wrongSiblingFormat struct{ jsonformat.Format }

func (f wrongSiblingFormat) Replace(source []byte, root, target *syntax.Node, value any) ([]syntax.Edit, error) {
	edits, err := f.Format.Replace(source, root, target, value)
	if err != nil {
		return nil, err
	}
	sibling := root.Get("b")
	return append(edits, syntax.Edit{Start: sibling.Start, End: sibling.End, Text: []byte("999")}), nil
}

func TestValidButWrongBackendOutputRollsBack(t *testing.T) {
	source := []byte(`{"a":1,"b":2}`)
	d, err := tql.ParseWith(source, wrongValueFormat{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`a=3; b=4`); err == nil {
		t.Fatal("backend silently changed requested value")
	}
	if !bytes.Equal(d.Bytes(), source) {
		t.Fatalf("incorrect backend output committed: %s", d.Bytes())
	}
	assertQuery(t, d, `a;b`, tql.Number("1"), tql.Number("2"))
	// A correct target replacement must still fail if another value also changed.
	d, err = tql.ParseWith(source, wrongSiblingFormat{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Exec(`a=3`); err == nil {
		t.Fatal("backend silently changed a sibling")
	}
	if !bytes.Equal(d.Bytes(), source) {
		t.Fatalf("sibling corruption committed: %s", d.Bytes())
	}
}

func (f invalidOutputFormat) Replace(source []byte, _ *syntax.Node, target *syntax.Node, value any) ([]syntax.Edit, error) {
	if f.invalidEdits {
		return []syntax.Edit{{Start: len(source) + 1, End: len(source) + 2}}, nil
	}
	return []syntax.Edit{{Start: target.Start, End: target.End, Text: []byte("INVALID")}}, nil
}
func (f invalidOutputFormat) Parse(source []byte) (*syntax.Node, error) {
	if f.nilTree {
		return nil, nil
	}
	return f.Format.Parse(source)
}

func TestRegistryAndCustomBackend(t *testing.T) {
	r := tql.NewRegistry()
	if err := r.Register(" Custom ", jsonformat.Format{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("custom", jsonformat.Format{}); err == nil {
		t.Fatal("duplicate format registration accepted")
	}
	if err := r.Register("", jsonformat.Format{}); err == nil {
		t.Fatal("empty format registration accepted")
	}
	if err := r.Register("nil", nil); err == nil {
		t.Fatal("nil backend accepted")
	}
	d, err := r.Parse([]byte(`{"a":1}`), "CUSTOM")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, d, `a = 2`)
	assertQuery(t, d, `a`, tql.Number("2"))
	if _, err = r.Parse([]byte(`{}`), "missing"); err == nil {
		t.Fatal("unknown format accepted")
	}
	if _, err = tql.ParseWith([]byte(`{}`), nil); err == nil {
		t.Fatal("nil format accepted")
	}
	if _, err = tql.ParseWith([]byte(`{}`), invalidOutputFormat{nilTree: true}); err == nil {
		t.Fatal("nil parse tree accepted")
	}
	for _, bad := range []invalidOutputFormat{{}, {invalidEdits: true}} {
		t.Run(fmt.Sprint(bad.invalidEdits), func(t *testing.T) {
			source := []byte(`{"a":1}`)
			d, err := tql.ParseWith(source, bad)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.Exec(`a=2`); err == nil {
				t.Fatal("invalid backend output accepted")
			}
			if !bytes.Equal(d.Bytes(), source) {
				t.Fatalf("invalid backend output committed: %q", d.Bytes())
			}
		})
	}
}

func TestDetectAndInvalidInput(t *testing.T) {
	for _, tc := range []struct{ name, format string }{{"a.JSON", "json"}, {"a.yaml", "yaml"}, {"a.yml", "yaml"}, {"a.toml", "toml"}} {
		format, err := tql.DetectFormat(tc.name)
		if err != nil || format != tc.format {
			t.Fatalf("DetectFormat(%q) = %q, %v", tc.name, format, err)
		}
	}
	if _, err := tql.DetectFormat("stdin"); err == nil {
		t.Fatal("extensionless format detected")
	}
	if _, err := tql.Parse([]byte(`{broken`), "json"); err == nil {
		t.Fatal("invalid input accepted")
	}
	for _, q := range []string{"", `a =`, `a[`, `a = 01`, `a = "\uD800"`, `a = {x:1,x:2}`, `a = 1 b = 2`, strings.Repeat("(", 300) + "1" + strings.Repeat(")", 300)} {
		if _, err := tql.Compile(q); err == nil {
			t.Fatalf("invalid query %q accepted", q)
		}
	}
}

func ExampleEval() {
	r, err := tql.Eval([]byte("{ \"port\" : 80 }\n"), "json", `port = 8080; port`)
	if err != nil {
		panic(err)
	}
	fmt.Print(string(r.Bytes))
	fmt.Println(r.Values[0])
	// Output:
	// { "port" : 8080 }
	// 8080
}

// The public transaction boundary must never commit a malformed result or leave
// partial edits after an error, regardless of how the query parser is exercised.
func FuzzExec(f *testing.F) {
	for _, query := range []string{
		`a[]`, `a[-2:] += 1; a`, `items[name == "foo"].x = 3`,
		`a[] |= . + $.a[0]`, `del(a[0],a[1])`, `new.deep[0].key = "é"`,
		`items[].missing ??= 2`, `a = 2; b = 1 / 0`, `(.a | reverse)`,
		`{x:[a[]],y:length(items)}`, `a = "\uD834\uDD1E"`,
		`let $delta = 1; for $x in a[] { $x += $delta; }`,
		`let $i = 0; while $i < 3 { a[$i] += 1; $i += 1; }`,
		`for $node in walk(.) { if type($node) == "number" { $node += 1; } }`,
		`let $copy = a; for $x in $copy[] { if $x == 2 { del($x); } } b = $copy`,
		`param $delta = 2; a[] += $delta`,
		`param $settings = {items:[1,2]}; for $x in $settings.items[] { $x += 1 }`,
		`param $required; a = $required`,
	} {
		f.Add(query)
	}
	f.Fuzz(func(t *testing.T, query string) {
		if len(query) > 4096 {
			t.Skip()
		}
		source := []byte(`{"a":[1,2,3],"items":[{"name":"foo","x":1},{"name":"bar"}],"b":null}`)
		d, err := tql.Parse(source, "json")
		if err != nil {
			t.Fatal(err)
		}
		r, err := d.Exec(query)
		if err != nil {
			if !bytes.Equal(source, d.Bytes()) {
				t.Fatalf("failed query %q committed partial edits: %s", query, d.Bytes())
			}
			return
		}
		if !json.Valid(r.Bytes) {
			t.Fatalf("query %q produced invalid JSON: %s", query, r.Bytes)
		}
		if r.Changed != !bytes.Equal(source, r.Bytes) {
			t.Fatal("Changed disagrees with output bytes")
		}
		if !bytes.Equal(r.Bytes, d.Bytes()) {
			t.Fatal("result and document bytes disagree")
		}
		assertQuery(t, d, `.`, d.Value())
	})
}

// wrapperFormat embeds the JSON backend and overrides Parse, like a caller that
// validates or instruments parsing.
type wrapperFormat struct {
	jsonformat.Format
	parse int
}

func (f *wrapperFormat) Parse(source []byte) (*syntax.Node, error) {
	f.parse++
	return nil, fmt.Errorf("wrapper rejected the document")
}

func TestParseWithRespectsWrapperParse(t *testing.T) {
	spy := &wrapperFormat{}
	if _, err := tql.ParseWith([]byte(`{"a":1}`), spy); err == nil || !strings.Contains(err.Error(), "wrapper rejected") {
		t.Fatalf("wrapper Parse was bypassed: err=%v", err)
	}
	if spy.parse != 1 {
		t.Fatalf("wrapper Parse calls: %d", spy.parse)
	}
}

// plainSpyFormat implements only the base Format interface.
type plainSpyFormat struct {
	inner jsonformat.Format
	parse int
}

func (f *plainSpyFormat) Parse(source []byte) (*syntax.Node, error) {
	f.parse++
	return f.inner.Parse(source)
}

func (f *plainSpyFormat) Replace(source []byte, root, target *syntax.Node, value any) ([]syntax.Edit, error) {
	return f.inner.Replace(source, root, target, value)
}

func (f *plainSpyFormat) Insert(source []byte, root, parent *syntax.Node, key string, value any) ([]syntax.Edit, error) {
	return f.inner.Insert(source, root, parent, key, value)
}

func (f *plainSpyFormat) Delete(source []byte, root, target *syntax.Node) ([]syntax.Edit, error) {
	return f.inner.Delete(source, root, target)
}

func TestParseWithFallsBackToParse(t *testing.T) {
	spy := &plainSpyFormat{}
	d, err := tql.ParseWith([]byte(`{"a":1}`), spy)
	if err != nil {
		t.Fatal(err)
	}
	if spy.parse != 1 {
		t.Fatalf("parse calls: %d", spy.parse)
	}
	assertQuery(t, d, `a`, tql.Number("1"))
}

func TestParseOwnedPathAvoidsStringCopies(t *testing.T) {
	source := []byte(`{"k0":"v0","k1":"v1","k2":"v2","k3":"v3","k4":"v4","k5":"v5","k6":"v6","k7":"v7"}`)
	backend := jsonformat.Format{}
	copying := testing.AllocsPerRun(50, func() {
		if _, err := backend.Parse(source); err != nil {
			t.Fatal(err)
		}
	})
	owned := testing.AllocsPerRun(50, func() {
		if _, err := tql.Parse(source, "json"); err != nil {
			t.Fatal(err)
		}
	})
	if owned >= copying {
		t.Fatalf("owned parse allocates %.1f, copying parse %.1f", owned, copying)
	}
}
