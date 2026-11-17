# Format support

TQL provides handwritten parsers and source-preserving edit backends for JSON, TOML,
and YAML. Documents do not pass through an unmarshal/marshal round trip. The support
below describes the implemented scope. The pinned upstream TOML, YAML, and JSON
corpora run in ordinary Go tests and CI; see [parser conformance](../conformance/README.md)
for exact versions, checks, and intentional policy differences.

## JSON

The parser targets [RFC 8259](https://www.rfc-editor.org/info/rfc8259/). It supports
all JSON value types, decimal numbers without `float64` rounding, string escapes,
and UTF-16 surrogate pairs. Input must be UTF-8, with nesting limited to 1,024
levels. Comments, trailing commas, and a byte order mark (BOM) are rejected.

Object keys must be unique after decoding: `"a"` and `"\u0061"` conflict. Unpaired
surrogates are rejected so decoded strings remain valid Unicode. These are
interoperability policies; other JSON implementations may accept such inputs.

Scalar replacement changes only the token. Deletion removes a member or element
and the necessary comma while leaving other whitespace in place; blank lines may
remain. Replacements use compact JSON. Insertions infer separator spacing,
indentation, line endings, and collection layout from their surroundings; nested
new collections can follow a nearby collection of the same kind. With no style
evidence, they use compact JSON. Deleting the root is an error.

## TOML

The parser targets [TOML 1.1](https://toml.io/en/v1.1.0). It supports implicit and
explicit tables, quoted and dotted keys, inline tables, arrays of tables, mixed and
multiline arrays, all four string forms, Unicode escapes, integer bases, floats,
dates and times, `inf`, and `nan`. Inline tables may span lines, contain comments,
and end with a trailing comma. Strings support `\e` and `\xHH` escapes; times may
omit seconds.

The default target for generation is TOML 1.0. Input using 1.1 features
automatically selects 1.1 for that document. Use `--format-version 1.0` or
`tql.WithVersion("1.0")` to reject newer syntax, or select `1.1` explicitly.
See [format versions](library.md#format-versions) for API behavior.

Integers are limited to signed 64-bit values; float overflow is rejected. Decimal
float spelling is retained for calculations rather than rounded to `float64`.
Dates are validated, but leap seconds are unsupported. Duplicate keys and
conflicting table definitions are errors. Value nesting and key paths are limited
to 256 levels. The root is always an object, and `null` is not representable at any
level. Date/time values retain their source spelling, including omitted seconds.

Inserting into a root or header table leaves unrelated sections untouched. Dotted
keys retain their individual source ranges. Deleting the last field retains an
empty parent object. Arrays of tables are exposed as arrays of objects: items can
be edited, appended, and deleted. Replacing one such item requires an object;
replacing the entire array can change its representation.

New entries infer assignment spacing, key and string quoting, and array layout
from nearby entries. Literal quotes are used only when the new text is legal in
that form. Object values follow nearby table, inline or dotted conventions; object arrays
can use array tables. With no structural evidence, new objects use tables.
[Generation policies](style.md) can explicitly select each representation. Existing
comments and scalar spellings remain untouched.

See the [TOML backend](../formats/toml/README.md) for edit behavior and corpus tests.

## YAML

The parser reads [YAML 1.2.2](https://yaml.org/spec/1.2.2/) syntax. The public
`yaml.ParseStream` API preserves document streams, typed or complex mapping keys,
custom tags, anchors, and alias edges in a source-aware graph. It copies its input
and returns no partial stream on error.

The TQL query backend projects one document into its value-tree model. It supports
block and flow collections,
compact collections, comments, UTF-8 with an optional BOM, multiple line-ending
styles, core scalar resolution, quoted and plain strings, and literal/folded block
scalars with indentation and chomping indicators. Plain `yes`, `on`, and dates
remain strings, following YAML 1.2 rather than YAML 1.1 implicit typing.

For queries, anchors and backward aliases resolve into a value tree. Reusing an
anchor name binds subsequent aliases to the new definition; earlier aliases retain
their original target. Editing inside an alias
materializes only that alias expression and preserves the anchor definition.
Editing an anchor that would change unselected aliases is rejected by the engine's
full-tree verification and rolled back. Recursive aliases are retained by the graph
API but rejected by the query projection. Unknown or forward aliases and excessive
query-time alias expansion are rejected.

Query values use string-keyed objects. Complex or empty null keys, custom tags,
merge keys, non-finite numbers, and multiple documents cannot be projected without
losing YAML information, so queries return explicit errors for these inputs. The
graph API retains their syntax and metadata instead. Plain primitive key spelling
remains the query key; projected key collisions are errors. See the
[YAML APIs and limits](../formats/yaml/README.md) for graph traversal, native tags,
size limits, and alias rules.

Inserted keys, strings, and collections follow nearby quoting, indentation, and
block or flow layout where possible. Strings that could resolve as another type
are quoted. Replacements otherwise use JSON-compatible YAML flow syntax.
Empty block collections become
`{}` or `[]` after deleting their final member, preserving the collection type.
Deleting the root replaces it with `null`.

## Failed edits

A value must be representable in the selected format. Assigning TOML `null`,
replacing the TOML root with an array, or requesting an unsupported structural edit
returns an error. Parsing errors, backend errors, and unexpected semantic changes
roll back the complete execution. A successful parse means the document is accepted
by TQL's implemented profile, not that every feature of its format is supported.

For the exact byte-preservation contract, see [architecture](architecture.md#byte-preservation).
