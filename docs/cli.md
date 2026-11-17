# Command-line usage

## Installation

### Prebuilt binaries

Download the archive matching your machine from
[GitHub Releases](https://github.com/vasileknik76/tql/releases/latest). Release
binaries do not require Go. `amd64` means x86-64; `arm64` covers Apple Silicon and
64-bit ARM Linux systems. Windows binaries have a `.exe` suffix.

| Operating system | Architectures |
| --- | --- |
| Linux | `amd64`, `arm64`, `386`, `armv7` |
| macOS (`darwin`) | `amd64`, `arm64` |
| Windows | `amd64`, `arm64` |

Archives contain the `tql` executable, README, and license. Verify the archive
against the release's SHA-256 checksums before extracting it. For example, for
Linux x86-64 and version 0.1.0:

```sh
curl -fLO https://github.com/vasileknik76/tql/releases/download/v0.1.0/tql_0.1.0_linux_amd64.tar.gz
curl -fLO https://github.com/vasileknik76/tql/releases/download/v0.1.0/SHA256SUMS
sha256sum --ignore-missing --check SHA256SUMS
tar -xzf tql_0.1.0_linux_amd64.tar.gz
./tql --version
```

On an Apple Silicon Mac, download `tql_0.1.0_darwin_arm64.tar.gz`; on an Intel Mac,
use `tql_0.1.0_darwin_amd64.tar.gz`. Run `shasum -a 256 ARCHIVE` and compare it with
the matching line in `SHA256SUMS`, then extract with `tar -xzf ARCHIVE`.
Move the executable to a directory on your `PATH`, or invoke it with `./tql`.

On Windows x86-64, download `tql_0.1.0_windows_amd64.zip` and `SHA256SUMS`, then
use PowerShell:

```powershell
Get-FileHash .\tql_0.1.0_windows_amd64.zip -Algorithm SHA256
# Compare the hash with SHA256SUMS before extracting.
Expand-Archive .\tql_0.1.0_windows_amd64.zip -DestinationPath .\tql
.\tql\tql.exe --version
```

### From source

Requires Go 1.23 or later:

```sh
go install github.com/vasileknik76/tql/cmd/tql@latest
```

Use `@v0.1.0` to install a specific release. Ensure Go's installation directory
(`$(go env GOPATH)/bin` by default) is on your `PATH`.

From a checkout, run `go build -o bin/tql ./cmd/tql` or `go install ./cmd/tql`.
A build can set its version with:

```sh
go build -ldflags '-X main.version=0.1.0' -o bin/tql ./cmd/tql
```

## Invocation

```text
tql [options] 'query' [file|-]
tql [options] --query-file program.tql [file|-]
tql [options] --program program.tqlc [file|-]
tql [options] --assemble program.tqla [file|-]
```

Put all flags before the first positional argument. Quote the query as a single
shell argument. A missing filename or `-` reads standard input; stdin requires
`--format`. File extensions `.json`, `.toml`, `.yaml`, and `.yml` determine the
format unless `--format` overrides it. Unknown extensions require an explicit
format. Each invocation processes one document, not a directory or a stream of
YAML documents.

| Flag | Meaning |
| --- | --- |
| `-f`, `--format NAME` | Input format: `json`, `toml`, `yaml`, or `yml` |
| `--format-version V` | TOML target: `auto` (default), strict `1.0`, or strict `1.1` |
| `--style STYLE` | New collections: `auto` (default), `block`, `flow` |
| `--object-style STYLE` | New objects: `auto`, `block`, `flow`, `dotted` (TOML) |
| `--array-style STYLE` | New arrays: `auto`, `block`, `flow` |
| `--string-style STYLE` | `auto`, `single`, `double`, `literal`, `plain`/`folded` (YAML), `multiline` (TOML) |
| `--key-style STYLE` | New keys: `auto`, `plain`, `single`, `double` |
| `--number-style STYLE` | New integers: `auto`, `decimal`, `hex`, `octal`, `binary` (TOML) |
| `--indent UNIT` | `auto`, 1–16 spaces (number), `tab` (TOML) |
| `--line-ending STYLE` | New text: `auto`, `lf`, `crlf` |
| `-i`, `--in-place` | Replace a regular input file after successful execution |
| `-r`, `--raw` | Print result strings without JSON quotes |
| `--values` | Print read results even when the program contains mutations |
| `--check` | Print and write nothing; report whether source bytes would change |
| `--query-file FILE` | Read the TQL program from a file |
| `--program FILE` | Load a compiled bytecode image (`.tqlc`) |
| `--assemble FILE` | Assemble bytecode text (`.tqla`) |
| `--emit-bytecode FILE` | Write a compiled image and exit |
| `--emit-assembly FILE` | Write canonical bytecode assembly and exit (`-` for stdout) |
| `--disassemble` | Print canonical bytecode assembly and exit |
| `--arg NAME=VALUE` | Bind a string input parameter; repeat for different names |
| `--argjson NAME=JSON` | Bind one JSON value as an input parameter |
| `--params-file FILE` | Read input parameters from a JSON object; explicit flags override it |
| `--version` | Print the build version (`dev` unless set at build time) |
| `-h`, `--help` | Show help |

```sh
# Read one JSON value per match, or a raw string.
tql 'server' config.toml
tql -r 'server.host' config.yaml

# Read standard input.
tql -f json 'users[].name' < config.json

# Write the edited document to stdout.
tql 'server.port = 9090' config.toml > updated.toml

# Update the input file.
tql -i 'server.port = 9090' config.toml

# Return read values after an edit.
tql --values 'server.port = 9090; server.port' config.toml

# Compile once, then run the image or inspect its assembly.
tql --query-file migrate.tql --emit-bytecode migrate.tqlc
tql --program migrate.tqlc config.toml
tql --program migrate.tqlc --disassemble
tql --assemble migrate.tqla --emit-bytecode migrate.tqlc
tql --query-file migrate.tql --emit-assembly migrate.tqla
```

Explicit per-kind collection flags override `--style` regardless of order. Style
settings control additions, not whole-file formatting. See [generation style](style.md)
for examples, supported representations and inference rules.

## Output

A read prints one JSON value per line. Object keys are sorted in this output,
regardless of their source order. Large finite numbers remain exact JSON numbers.
TOML date/time values become JSON strings; TOML `inf`, `nan`, and signed variants
become strings containing their spelling. `--raw` affects only strings and prints
any embedded newlines literally.

A program containing any assignment or deletion prints the **final source bytes**
by default, even if it also contains reads or makes no actual change. No extra
newline is added. `--values` prints only the program's read results; a program
containing only assignments produces no value output. No read results are emitted
if execution fails.

Mutation detection is conservative: changing only a local script variable still
selects source output. A `let` declaration alone does not count as a mutation.
Use `--values` to print a script's computed results.

## In-place edits

`--in-place` requires a regular file and a program containing a mutation.
Symlinks are rejected. It cannot be combined with `--check` or `--values`.
`--raw` with a mutation requires `--values`.

TQL executes the entire program in memory before writing. It writes and syncs a
temporary file in the input directory, preserves Unix permission bits, checks the
input's identity and contents, then renames the temporary file over the input.
Failures clean up the temporary file. A byte-for-byte no-op does not rewrite the
file or change its modification time.

On Unix-like systems, the same-directory rename provides atomic replacement.
On Windows, Go's `os.Rename` does not guarantee atomicity, even in the same
directory. Transactional script evaluation and the pre-write checks apply on
all supported platforms.

Replacement creates a new inode. Ownership, ACLs, extended attributes, and hard
link relationships are not explicitly preserved. The input check reduces the
risk of overwriting concurrent changes; it is not an interprocess lock. Avoid
concurrent writers to the same file.

Do not redirect output into the input file (`tql '...' file > file`): the shell
truncates it before TQL starts. Use `--in-place` instead.

## Exit status

| Status | Meaning |
| --- | --- |
| `0` | Success; with `--check`, the source would be unchanged |
| `1` | `--check` found a byte-level change |
| `2` | Argument, query, parsing, editing, read, or write error |

For example, a CI check can enforce a setting without modifying the checkout:

```sh
tql --check 'server.tls = true' config.toml
```

The program and document are loaded fully into memory. Execution errors identify
the TQL statement, and format parse errors include source positions.

## Script files

```sh
tql -i --query-file examples/migrate.tql config.toml
tql --values --query-file examples/migrate.tql config.toml
```

The whole script, including every loop iteration, is one transaction. See the
[scripting guide](scripts.md) for variables, control flow, and tree traversal, or
the [language reference](syntax.md) for expression syntax.

## Compiled bytecode

Compile a script once and reuse the image:

```sh
tql --query-file migrate.tql --emit-bytecode migrate.tqlc
tql --program migrate.tqlc config.toml
```

`--program` reads the versioned image produced by this build. A version or
checksum mismatch is an error; recompile the script with the same TQL version.
The image contains the program, parameter metadata and debug names, never the
document or the script text.

Inspect and edit the textual form:

```sh
tql --program migrate.tqlc --disassemble > migrate.tqla
tql --assemble migrate.tqla config.toml
tql --assemble migrate.tqla --emit-bytecode migrate.tqlc
tql --query-file migrate.tql --emit-assembly migrate.tqla
```

The disassembly is canonical and can be assembled back without losing behavior.
`--disassemble`, `--emit-assembly` and `--emit-bytecode` do not read a document.
`--emit-bytecode` requires a file path and does not write to stdout;
`--emit-assembly` accepts `-` for stdout. A complete walkthrough of the three
forms is in [examples/bytecode](../examples/bytecode/README.md).

## Input parameters

Declare inputs with `param` in the query or script file:

```sh
tql --arg name=alice --argjson enabled=true \
    'param $name; param $enabled = false; users[name == $name].enabled = $enabled' \
    config.yaml
```

`--arg` always binds a string: `--arg version=001` supplies `"001"`, and
`--arg enabled=false` supplies `"false"`. Use `--argjson` for typed values:

```sh
tql -f json --argjson 'settings={"timeout":30,"ports":[80,443]}' \
    'param $settings; server = $settings' < config.json
```

JSON is parsed as data, with exact numbers and duplicate-key rejection, using
TQL's JSON parser. Expressions such as `count=1+2` are not JSON. Split `NAME=VALUE`
at the first `=`; the rest belongs to the value. Names omit `$` and must be valid
TQL variable identifiers. Flags precede the query and input filename as usual.

A parameters file contains a single JSON object, for example:

```json
{"service":"api","image":"api:v2","replicas":3}
```

```sh
tql -i --params-file production.json --arg image=api:v3 \
    --query-file rollout.tql compose.yaml
```

Defaults in the script have the lowest priority, followed by the parameters file,
then explicit `--arg` / `--argjson` flags, regardless of flag order. Explicit
`null` overrides a default. Repeating a name across CLI flags is an error,
including across `--arg` and `--argjson`. Only one `--params-file` is accepted;
it must be a file path, since stdin is reserved for the input document.

Missing required inputs, unknown names, and malformed parameter values exit with
status `2`, without script output or an in-place write. Parameters work with
inline queries, `--query-file`, stdin documents, `--values`, `--raw`, `--check`,
and `--in-place`. See [parameter semantics](scripts.md#input-parameters) and the
[runnable example](../examples/parameters/README.md).
