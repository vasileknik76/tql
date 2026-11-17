# Go API

Import `github.com/vasileknik76/tql`. The module requires Go 1.23 or later and has
no third-party dependencies.

The [structured editing API](edit.md) builds commands from Go paths and values,
diffs documents, and plans merges through package `edit`.

## Parse and execute

```go
package main

import (
    "fmt"
    "log"
    "os"

    "github.com/vasileknik76/tql"
)

func main() {
    if err := run(); err != nil {
        log.Fatal(err)
    }
}

func run() error {
    source, err := os.ReadFile("config.toml")
    if err != nil {
        return err
    }
    doc, err := tql.Parse(source, "toml")
    if err != nil {
        return err
    }
    values, err := doc.Query(`server.port`)
    if err != nil {
        return err
    }
    fmt.Println(values)

    result, err := doc.Exec(`server.port = 9090; server.port`)
    if err != nil {
        return err // doc still contains its original bytes
    }
    fmt.Print(string(result.Bytes))
    return nil
}
```

`Parse` copies its input. `Bytes` returns a copy of the source, and `Value` returns
an independent semantic tree. Modifying those results does not change the document.
`Parse` requires an explicit format name; `DetectFormat(filename)` recognizes
filename extensions without reading file contents.

`Exec(text)` compiles and executes a program. `Execute(program)` runs a previously
compiled program. Both commit changes only when the **entire** program succeeds.
An error returns no partial read results and leaves the document unchanged.

`Result` contains:

| Field | Meaning |
| --- | --- |
| `Bytes []byte` | Final source bytes, independent of the document's storage |
| `Values []any` | Results produced by read expressions; mutations produce none |
| `Changed bool` | Whether the final source differs byte-for-byte from the source before execution |

`Query(text)` returns only read values and rejects programs containing assignments
or deletions. This check is conservative: assignments to local variables also
require `Exec`/`Execute`, even if they leave `Result.Changed` false.
A `let` declaration alone is not classified as a mutation.

For a single operation, `tql.Eval(source, "toml", "server.port = 9090")` combines
parsing and execution.

## Generation style

`Parse`, `ParseWith`, `Registry.Parse`, and `Eval` accept `tql.WithStyle`:

```go
doc, err := tql.Parse(source, "toml", tql.WithStyle(tql.Style{
    Objects: "block",
    Arrays: "auto",
    Strings: "single",
}))
```

Empty fields mean `auto`. The policy applies to inserted members and appended
items through both script execution and `Apply`; it never reformats untouched
source. Invalid styles and unsupported format combinations return errors.
See [generation style](style.md) for all fields, format constraints, inference
rules, and the configuration design.

## Format versions

`Parse`, `ParseWith`, `Registry.Parse`, and `Eval` accept `tql.WithVersion`.
For TOML, the default (`""` or `"auto"`) starts at 1.0 and selects 1.1 only
when the input uses a 1.1 feature. The selected target stays fixed for the
document's lifetime, including after removal of the feature that selected it.

```go
doc, err := tql.Parse(source, "toml", tql.WithVersion("1.0"))
```

Explicit `"1.0"` and `"1.1"` targets are strict: incompatible input fails to
parse, and incompatible inserted or replaced values fail atomically. For example,
`tql.Temporal("14:00")` requires 1.1; use `tql.Temporal("14:00:00")` with 1.0.
Versions do not reformat or downgrade existing source. Style and version options
can be combined in either order. The last version option wins. Unsupported
versions and explicit versions for JSON/YAML return errors.

Custom backends can implement the optional `syntax.VersionedFormat` interface.
`WithVersion` configures an independent backend, and `Version(root)` returns the
explicit or detected target; TQL pins that target before executing edits.
Wrappers must override `WithVersion` and return their own backend type, preserving
custom parsing and editing methods. An inherited factory that unwraps the backend
is ignored during automatic pinning and rejected for an explicit `WithVersion`
option. Without a wrapper-preserving factory, automatic version detection follows
the current source instead of keeping a fixed target across edits.

## Reuse a compiled program

The following fragment assumes `inputs` is a `[][]byte` and the enclosing function
returns an error:

```go
program, err := tql.Compile(`items[].price |= . * 2`)
if err != nil {
    return err
}
for _, source := range inputs {
    doc, err := tql.Parse(source, "json")
    if err != nil {
        return err
    }
    result, err := doc.Execute(program)
    if err != nil {
        return err
    }
    fmt.Print(string(result.Bytes))
}
```

A `Program` is immutable and safe to reuse concurrently with different documents.
`Program.Mutates()` reports whether it contains any assignment or deletion,
including local variable changes. A `Document` is not synchronized: use separate
documents or external locking for concurrent access. Registries are safe for
concurrent registration and lookup.

Scripts use the same API:

```go
program, err := tql.Compile(`
    let $delta = 10;
    for $server in servers[] {
        if $server.enabled { $server.port += $delta; }
    }
`)
```

Check the compilation error, then call `doc.Execute(program)`. Each execution has
its own variable environment. An error in any iteration rolls back the whole
execution. See [scripting semantics](scripts.md).

## Bytecode images and assembly

A compiled program can be saved and loaded without its source:

```go
program, err := tql.Compile(`services[].port += 1`)
if err != nil {
    return err
}
image, err := program.MarshalBinary()
if err != nil {
    return err
}
loaded, err := tql.LoadProgram(image)
if err != nil {
    return err
}
result, err := doc.Execute(loaded)
```

`MarshalBinary` is deterministic and versioned. `LoadProgram` validates the
image and rejects corrupt, truncated, unknown-version or structurally invalid
input. A loaded `Program` is immutable and safe to reuse concurrently; parameters
declared in the source remain available through `tql.WithParams`.

`Disassemble` returns canonical text that `Assemble` accepts:

```go
text := program.Disassemble()
same, err := tql.Assemble(text)
```

Assembly is intended for debugging, review and reproducible tooling, not as a
faster source format. The binary image and the assembly text are versioned
together; use a matching TQL build.

## Input parameters

Declare inputs in the script and pass their values per execution:

```go
program, err := tql.Compile(`
    param $service;
    param $image;
    param $replicas = 2;
    services[$service].image = $image;
    services[$service].deploy.replicas = $replicas;
`)
if err != nil {
    return err
}
result, err := doc.Execute(program, tql.WithParams(map[string]any{
    "service": "api",
    "image": "api:v2",
    "replicas": 3,
}))
if err != nil {
    return err
}
fmt.Print(string(result.Bytes))
```

The fragment assumes a parsed `doc` and an enclosing function returning an error.
`Exec`, `Execute`, and `Query` accept optional `ExecOption` values. `WithParams`
is allowed once per call; omitted options mean no supplied parameters. Parameters
belong to an execution, not to parsing, the document, or the compiled program.
Missing required parameters and unknown supplied names fail before any statement.
See [script parameters](scripts.md#input-parameters) for defaults and scope.

`WithParams` captures a normalized deep snapshot when called. Changing the
original map or its nested containers afterward has no effect. The option and
compiled program can be reused concurrently across separate documents; each
execution gets independent parameter trees and defaults. Returned read values
are independent as well. Do not mutate the input map concurrently with the
`WithParams` call that captures it.

Values use the types listed below. Go integer types and finite `float32`/`float64`
are also accepted and normalized to `tql.Number`. For exact decimal input, use
`tql.Number("0.1")`; normalization cannot recover precision already lost in a
floating-point value. Containers must be `map[string]any` or `[]any`. Unsupported
types, cyclic containers, invalid UTF-8, invalid numbers or dates, and nesting
beyond 256 levels are rejected. Errors captured by `WithParams` are returned by
the execution call. The same normalization is available as `syntax.NormalizeValue`
and is used by structured `Set` operations.

Parameters are read-only, including nested values and references obtained through
loops. `let $copy = $parameter` creates a mutable local snapshot. Supplying `nil`
for a named parameter means explicit TQL `null`; it does not select its default.
Target-format restrictions apply when a value is written: a `null` parameter can
be read in a TOML script, but cannot be assigned into the TOML document.

`Eval` accepts both parsing and execution options:

```go
result, err := tql.Eval(source, "yaml", script,
    tql.WithStyle(tql.Style{Strings: "double"}),
    tql.WithParams(params),
)
```

| Option | `Parse` / `ParseWith` / `Registry.Parse` | `Exec` / `Execute` / `Query` | `Eval` |
| --- | --- | --- | --- |
| `WithStyle` | Yes | No | Yes |
| `WithVersion` | Yes | No | Yes |
| `WithParams` | No | Yes | Yes |

The option interfaces enforce this distinction at compile time: `ParseOption`
and `ExecOption` both extend `EvalOption`. This replaces the former `Option`
function type. Migrate stored parse-option slices to `[]tql.ParseOption` and
stored `Eval` option slices to `[]tql.EvalOption`. `Exec`, `Execute`, and `Query`
now have variadic options; update interface declarations or method-function
types that referred to their old signatures. Calls without options still work.

## Value types

| TQL value | Go type |
| --- | --- |
| Object | `map[string]any` |
| Array | `[]any` |
| String | `string` |
| Number | `tql.Number`, an alias of `syntax.Number` |
| Boolean | `bool` |
| Null | `nil` |
| TOML date/time | `tql.Temporal`, an alias of `syntax.Temporal` |

`Number` is a named string, such as `tql.Number("9007199254740993")`. Its `String`
and `MarshalJSON` methods preserve exact finite numbers when passed to
`encoding/json`. TOML `inf` and `nan` values marshal as strings; TQL arithmetic on
them returns an error. Convert explicitly when an application needs another
numeric type, for example `strconv.ParseInt(string(number), 10, 64)`.

TOML/YAML numeric spellings are normalized in semantic values (`0xFF` becomes
`255`), while source bytes keep their spelling until edited. TOML date/time is a
separate semantic type that retains its textual spelling, rather than `time.Time`.

## Add a format

A format implements `syntax.Format`, also exported as `tql.Format`:

```go
type Format interface {
    Parse(source []byte) (*syntax.Node, error)
    Replace(source []byte, root, target *syntax.Node, value any) ([]syntax.Edit, error)
    Insert(source []byte, root, parent *syntax.Node, key string, value any) ([]syntax.Edit, error)
    Delete(source []byte, root, target *syntax.Node) ([]syntax.Edit, error)
}
```

Use an implementation directly without changing a registry:

```go
import tomlformat "github.com/vasileknik76/tql/formats/toml"

// Inside a function; check err before using doc.
doc, err := tql.ParseWith(source, tomlformat.Format{})
```

Or use an isolated registry:

```go
registry := tql.NewRegistry() // starts empty
if err := registry.Register("myconfig", myFormat); err != nil {
    return err
}
doc, err := registry.Parse(source, "myconfig")
```

`RegisterFormat` adds an implementation to the default JSON/TOML/YAML registry.
Duplicate names return an error; registration names are trimmed and lowercased.
A zero-value `Registry` also supports registration. Format implementations must
be safe for concurrent calls.

### Source-aware trees and edits

`Parse` returns nodes with exact half-open byte ranges into the UTF-8 source.
`syntax.Link` fills parent links and lookup indexes; the library calls it after
parsing. Synthetic nodes such as implicit TOML tables may have `Start == -1`.
`Style` and `Meta` hold format-specific data. Edit methods must not mutate the
input buffer or tree.

`Insert` receives a missing object key or an array index equal to its length.
The built-in formats also accept `"-"` for an append. Return an error when a value
cannot be represented: TOML, for example, cannot store `null` or a scalar root.

The library validates edit ranges, applies edits with `syntax.Apply`, reparses the
result, and compares the entire semantic tree with the expected change. An edit
that loses an unrelated field or inserts into the wrong table fails and rolls
back, even if the output is syntactically valid.

Use a format's `Parse` method directly to inspect its `syntax.Node` tree. Formatting
lives in the source buffer and node ranges. After `syntax.Link`, reading a tree
does not populate caches. Do not mutate it during evaluation; after constructing
or rebuilding a tree manually, call `syntax.Link` again while holding exclusive
ownership. See [architecture](architecture.md) and [performance](performance.md)
for the full editing model.
