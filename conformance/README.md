# Parser conformance

`go test ./...` runs the pinned upstream parser corpora without network access,
Python, external executables, submodules, or third-party Go dependencies. CI runs
them on every OS/Go test-matrix entry, including its race-enabled entries.
The named `TOML version golden tests and reference suites (1.0, 1.1, auto)` step
also runs the version goldens and both TOML reference corpora explicitly, with
the test cache disabled.

```sh
make conformance
make yaml-suite                         # only the YAML corpus
go test ./conformance -run TestTOMLTestSuite -v
go test ./conformance -run TestJSONTestSuite -v
```

## Sources

Only fixture data and licenses are copied; upstream test runners and parsers are
not included. Inputs and reference files remain byte-for-byte upstream data,
including invalid UTF-8, CRLF, BOMs, tabs, and intentionally malformed documents.
`.gitattributes` disables text conversion for the entire fixture tree.

| Directory | Upstream | Pinned revision | Cases |
| --- | --- | --- | ---: |
| `testdata/toml` | [toml-lang/toml-test v2.2.0](https://github.com/toml-lang/toml-test/tree/ce08da1ddb075d1c7596d663c7fcba9a2ae02c5c) | `ce08da1ddb075d1c7596d663c7fcba9a2ae02c5c` | 679 (1.0), 681 (1.1) |
| `testdata/yaml` | [yaml/yaml-test-suite data-2022-01-17](https://github.com/yaml/yaml-test-suite/tree/6e6c296ae9c9d2d5c4134b4b64d01b29ac19ff6f) | `6e6c296ae9c9d2d5c4134b4b64d01b29ac19ff6f` | 402 |
| `testdata/json` | [nst/JSONTestSuite](https://github.com/nst/JSONTestSuite/tree/1ef36fa01286573e846ac449e8683f8833c5b26a) | `1ef36fa01286573e846ac449e8683f8833c5b26a` | 318 |

Each directory includes its upstream MIT license. The YAML data export does not
include its license; `testdata/yaml/LICENSE` comes from the upstream source
repository's [License](https://github.com/yaml/yaml-test-suite/blob/main/License).

## Assertions and policies

- **TOML:** strict 1.0 runs every `.toml` file in `files-toml-1.0.0`: 205 valid
  and 474 invalid documents. Strict 1.1 and auto each run `files-toml-1.1.0`:
  214 valid and 467 invalid documents. This includes inputs forbidden in 1.0
  but allowed in 1.1. The lists also name the JSON references; these are
  not extra TOML inputs. Valid documents must match the typed JSON reference,
  including array/table shapes, scalar types, decoded strings, integer values,
  float values and signed zero, NaNs/infinities, and temporal types. Finite floats
  are compared at the reference's float64 precision; TQL retains its own exact
  decimal representation. Invalid inputs must produce an error and no tree.
- **YAML:** every `in.yaml`, including numbered subcases, is tested: 308 valid
  and 94 invalid documents. Valid documents must match the complete `test.event`
  stream, including styles, document markers, scalar text, tags, anchors, and
  aliases. Available `in.json` companions additionally check constructed values
  and document counts. JSON numbers compare exactly, without float64 rounding.
  Invalid inputs must produce an error and no partial stream. Every parse must
  preserve its input bytes, and successful streams must return those exact bytes.
- **JSON:** all 318 `test_parsing/*.json` files are tested: 95 `y_` (accept),
  188 `n_` (reject), and 35 `i_` (either result). TQL deliberately rejects
  `y_object_duplicated_key.json` and `y_object_duplicated_key_and_value.json` with
  a duplicate-key error, matching its documented requirement for unambiguous
  query paths. These two cases are asserted as rejections, not skipped. Every
  accepted input must preserve its decoded value, with exact numeric comparison
  even for enormous exponents. `i_` cases must not panic or return a partial tree.

Missing files/references, unexpected results, and changed corpus counts fail the
run. No known failures are skipped. The suites exercise parser behavior; existing
unit, integration, and fuzz tests continue to check editing and query semantics.

## Updating the snapshots

1. Check out the chosen upstream revisions in temporary directories. For
   JSONTestSuite, use a sparse checkout of `test_parsing` to avoid downloading its
   large results and third-party parser collections.
2. Replace the TOML fixture directory with the union of files named by
   `tests/files-toml-1.0.0` and `tests/files-toml-1.1.0`, both manifests, and
   `LICENSE`. Copy both `.toml` and `.json` entries, without newline conversion.
3. Replace the YAML fixture directory with every directory containing `in.yaml`.
   Copy `in.yaml`, `test.event`, `in.json`, `error`, and `===` when present,
   retaining numbered subdirectories. Copy the upstream license separately.
   The `name` and `tags` indexes and emitter outputs are not parser fixtures.
4. Replace the JSON fixture directory with `test_parsing/*.json` and `LICENSE`.
5. Update this table and the explicit corpus counts in the tests. Run
   `make conformance` and `make check`; investigate every new failure instead of
   dropping tests or automatically accepting new deviations.
