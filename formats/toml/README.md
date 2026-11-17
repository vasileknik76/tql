# TOML backend

`toml.Format{}` implements `syntax.Format` with a handwritten
[TOML 1.1](https://toml.io/en/v1.1.0) parser and source-preserving edits. It has no
third-party runtime dependencies.

Generation defaults to TOML 1.0. Parsing detects features requiring 1.1 and
allows them for subsequent edits of that document. `tql.WithVersion("1.0")`
and CLI `--format-version 1.0` strictly reject newer input and generated values;
`1.1` explicitly permits them. Empty or `auto` selects detection. Existing source
is never downgraded. The backend's `WithVersion` and `Version(root)` implement
the optional `syntax.VersionedFormat` interface.

## Supported syntax

The parser supports quoted and dotted keys, implicit and explicit tables, arrays
of tables, inline tables, mixed and multiline arrays, all four string forms,
Unicode escapes, integer bases, floats, infinities and NaNs, and offset/local dates
and times. TOML 1.1 adds `\e` and `\xHH` escapes in basic strings and quoted keys,
optional seconds in times and date-times, and newlines, comments, and trailing
commas in inline tables. For example:

```toml
name = "Jos\xE9"
meeting = 1979-05-27T07:32Z
settings = {
  enabled = true,
}
```

Values retain source offsets. Header and dotted-key tables are synthetic object
nodes because their contents may occupy separate parts of the document. Arrays of
tables are synthetic arrays containing synthetic objects. Dates and times use
`syntax.Temporal`; numbers use `syntax.Number`. Integer spelling is normalized in
the value tree, while source bytes retain their original spelling until edited.
Temporal values retain their input spelling, including omitted seconds.

The parser rejects malformed UTF-8, duplicate definitions, invalid escapes and
dates, unsupported value tokens, integers outside signed 64-bit range, and float
overflow. Value nesting and dotted key paths are limited to 256 levels. Leap seconds
are unsupported.

## Edit behavior

Scalar, array, and inline-table replacements affect only the value span. Deletion
removes the selected assignment or value and any necessary comma. Syntax-only
assignment, header, and collection-item lines are removed with their indentation
and line ending. Inline deletions remove adjacent separator padding; multiline
containers keep their layout. Removing a header section also removes its following
empty separator lines, stopping at comments or surviving syntax. This is local to
the removed section: unrelated blank lines are not normalized. Unrelated
assignments, headers, whitespace, and comments remain unchanged. A removed key's
trailing comment can remain as a standalone comment. Removing the final array-table
item produces `key = []`.

Bracket-array replacements reuse the selected array's layout, indentation,
quoting, and comma style. With all styles set to `auto`, a replacement whose old
items are an exact prefix of the new items appends the remaining values while
preserving the existing bytes and internal comments. Other replacements may
remove internal comments. Matching is conservative: equivalent numbers with
different value representations can fall back to replacement. To explicitly
retain surviving elements, edit them in place and append by index. For example,
`let $index = length(commands); commands[$index] = "run b"` keeps existing array
bytes and comments while adding an item with the local insertion style. This path
is appropriate when the existing items are a prefix of the desired result.
Arbitrary reordering or merging does not establish which value a comment belongs
to; the backend does not guess or move comments onto different values.

Keeping a deleted item's comment as source text does not merge its description
with another item. A caller with an explicit comment-to-item mapping can collect
those notes and place them at the intended destination using comment-only
`syntax.Apply` edits on a source copy, then parse and edit that copy with TQL.
Commit the result only after both stages succeed. TQL scripts do not expose
comment reads or writes; semantic grouping alone cannot determine ownership.

Insertions infer local assignment spacing, key and string quotes, array separators,
indentation, and line endings. Nearby values of the same kind provide templates
for new collections. A literal string falls back to basic quotes when its contents
cannot be represented safely. With no useful evidence, the backend uses its
deterministic defaults. New object values use inline tables.
New keys in a header table are inserted into that section, while keys in implicit
dotted tables use dotted spelling. Inserted assignments precede the section's
trailing blank lines and comment blocks, preserving the separator before the
next header. New arrays can use body values in adjacent array-table items as
layout examples. New scalar arrays under `auto` use a soft 100-column limit when
their inferred inline representation is long; existing arrays, inline-table
contents, and explicit `flow` keep their chosen layout. Items are never split,
so one long scalar can exceed that limit. Array appends accept `"-"` or the current
array length as the insertion key. Nested arrays of tables are appended in the
section belonging to the selected parent item.

Replacing or deleting a header table changes its assignments and headers while
retaining comments. Replacing an individual array-table item requires an object
and retains its header; replace the whole array to change its representation.
Root replacement requires an object. TOML has no `null`: inserting or replacing a
null value fails, including a null nested in another value.

Format methods return byte-range edits without mutating the source or linked tree.
Apply a batch with `syntax.Apply`, then parse again before planning another batch.
The TQL engine performs this process and verifies the full resulting value tree;
see [architecture](../../docs/architecture.md).

## Conformance checks

The 679 TOML 1.0 and 681 TOML 1.1 parser cases from `toml-lang/toml-test` v2.2.0
are pinned in `conformance/testdata/toml` and run in `go test ./...` and CI.
Each strict target uses its version's upstream manifest; auto uses the 1.1 corpus.
Valid inputs are
compared with the upstream typed JSON values, including integer/float distinctions,
negative zero, special floats, and local/offset temporal types. Invalid inputs must
return an error without a partial tree. No downloads or Python are needed.
See [parser conformance](../../conformance/README.md) for details.

Version golden tests pair each input in `testdata/versions/*.toml` with a
`.golden` file showing all three targets: auto, strict 1.0, and strict 1.1.
They record the selected version, exact generated object/time bytes, and errors.
Examples: [conservative input](testdata/versions/conservative.golden),
[1.1 inline table](testdata/versions/inline-newline.golden), and
[a multiline array inside a 1.0 inline table](testdata/versions/nested-multiline-array.golden).
Expected files are reviewed and edited explicitly; tests never rewrite them.

```sh
go test ./formats/toml -run '^TestVersionGolden$' -count=1
go test ./conformance -run '^TestTOMLTestSuite$' -count=1
```
