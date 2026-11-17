package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func invoke(args []string, input string) (int, string, string) {
	var out, err bytes.Buffer
	code := run(args, strings.NewReader(input), &out, &err)
	return code, out.String(), err.String()
}
func TestCLI(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		input, output string
		code          int
	}{
		{"read", []string{"-f", "json", "a"}, `{"a":42}`, "42\n", 0},
		{"raw", []string{"-f", "yaml", "-r", "name"}, "name: 'Никита' # keep\n", "Никита\n", 0},
		{"mutation", []string{"-f", "toml", "a = 2"}, "a = 1 # keep\n", "a = 2 # keep\n", 0},
		{"version auto", []string{"-f", "toml", "copy=time"}, "time=14:00\n", "time=14:00\ncopy=14:00\n", 0},
		{"version strict", []string{"-f", "toml", "--format-version", "1.0", "."}, "time=14:00\n", "", 2},
		{"version new", []string{"-f", "toml", "--format-version", "1.1", "copy=time"}, "time=14:00\n", "time=14:00\ncopy=14:00\n", 0},
		{"version unknown", []string{"-f", "toml", "--format-version", "1.2", "."}, "a=1\n", "", 2},
		{"version unsupported", []string{"-f", "json", "--format-version", "1.0", "."}, "{}", "", 2},
		{"stream", []string{"-f", "json", "a[] | select(.n > 1) | .n"}, `{"a":[{"n":1},{"n":2},{"n":3}]}`, "2\n3\n", 0},
		{"values", []string{"-f", "json", "--values", "a = 2; a"}, `{"a":1}`, "2\n", 0},
		{"check changed", []string{"-f", "json", "--check", "a = 2"}, `{"a":1}`, "", 1},
		{"check unchanged", []string{"-f", "json", "--check", "a = 1"}, `{"a":1}`, "", 0},
		{"missing format", []string{"a"}, `{"a":1}`, "", 2},
		{"unknown format", []string{"-f", "xml", "a"}, "", "", 2},
		{"bad query", []string{"-f", "json", "a ="}, `{"a":1}`, "", 2},
		{"bad input", []string{"-f", "json", "a"}, `{"a":1,}`, "", 2},
		{"no partial stdout", []string{"-f", "json", "a; a = 1 / 0"}, `{"a":1}`, "", 2},
		{"stdin in place", []string{"-f", "json", "-i", "a = 2"}, `{"a":1}`, "", 2},
		{"conflict", []string{"-f", "json", "-i", "--check", "a = 2", "no.json"}, "", "", 2},
		{"raw mutation", []string{"-f", "json", "-r", "a = 2"}, `{"a":1}`, "", 2},
		{"unknown flag", []string{"--no-such-option"}, "", "", 2},
		{"no args", nil, "", "", 2},
		{"version", []string{"--version"}, "", "tql dev\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, err := invoke(tt.args, tt.input)
			if code != tt.code || out != tt.output {
				t.Fatalf("got code=%d out=%q err=%s; want code=%d out=%q", code, out, err, tt.code, tt.output)
			}
			if tt.code == 2 && err == "" {
				t.Fatal("missing diagnostic")
			}
		})
	}
	if code, out, err := invoke([]string{"--help"}, ""); code != 0 || !strings.Contains(out, "Usage: tql") || err != "" {
		t.Fatalf("help: %d %q %s", code, out, err)
	}
}
func TestInPlace(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.toml")
	source := "# head\r\nport  = 8080 # keep\r\n"
	if err := os.WriteFile(file, []byte(source), 0640); err != nil {
		t.Fatal(err)
	}
	original, e := os.Stat(file)
	if e != nil {
		t.Fatal(e)
	}
	code, out, err := invoke([]string{"-i", "port = 9000", file}, "")
	if code != 0 || out != "" {
		t.Fatalf("%d %q %s", code, out, err)
	}
	data, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	if string(data) != strings.Replace(source, "8080", "9000", 1) {
		t.Fatalf("unexpected bytes: %q", data)
	}
	info, e := os.Stat(file)
	if e != nil {
		t.Fatal(e)
	}
	if info.Mode().Perm() != original.Mode().Perm() {
		t.Fatalf("mode=%v", info.Mode())
	}
	before := info.ModTime()
	code, _, err = invoke([]string{"-i", "port = 9000", file}, "")
	if code != 0 {
		t.Fatal(err)
	}
	info, _ = os.Stat(file)
	if !info.ModTime().Equal(before) {
		t.Fatal("no-op replaced file")
	}
	code, _, _ = invoke([]string{"-i", "port = 1; port = null", file}, "")
	if code != 2 {
		t.Fatal("expected TOML null failure")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(data, after) {
		t.Fatal("failed transaction wrote the file")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".tql-*"))
	if len(leftovers) != 0 {
		t.Fatalf("leftover temp files: %v", leftovers)
	}
}

func TestStrictVersionInPlace(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.toml")
	source := []byte("time=14:00\n")
	if err := os.WriteFile(file, source, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, diagnostic := invoke([]string{"--format-version", "1.0", "-i", "time=1", file}, "")
	if code != 2 || out != "" || !strings.Contains(diagnostic, "requires TOML 1.1") {
		t.Fatalf("code=%d out=%q diagnostic=%s", code, out, diagnostic)
	}
	got, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(got, source) {
		t.Fatalf("failed validation changed input: %q, %v", got, err)
	}
}
func TestFileOptions(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "input.yaml")
	q := filepath.Join(dir, "read.tql")
	if e := os.WriteFile(input, []byte("name: alice\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(q, []byte("# query file\nname"), 0600); e != nil {
		t.Fatal(e)
	}
	if code, out, err := invoke([]string{"--query-file", q, input}, ""); code != 0 || out != "\"alice\"\n" {
		t.Fatalf("%d %q %s", code, out, err)
	}
	if code, _, _ := invoke([]string{"-i", "name", input}, ""); code != 2 {
		t.Fatal("read-only in-place accepted")
	}
	link := filepath.Join(dir, "link.yaml")
	if e := os.Symlink(input, link); e != nil {
		t.Skip(e)
	}
	if code, _, _ := invoke([]string{"-i", `name = "bob"`, link}, ""); code != 2 {
		t.Fatal("symlink accepted")
	}
	if code, _, _ := invoke([]string{"name", input, input}, ""); code != 2 {
		t.Fatal("multiple input files accepted")
	}
}
func TestConcurrentChangeGuard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.json")
	if e := os.WriteFile(path, []byte(`{"a":1}`), 0600); e != nil {
		t.Fatal(e)
	}
	original, _ := os.Stat(path)
	if e := os.WriteFile(path, []byte(`{"a":2}`), 0600); e != nil {
		t.Fatal(e)
	}
	if e := atomicWrite(path, []byte(`{"a":1}`), []byte(`{"a":3}`), original); e == nil {
		t.Fatal("overwrote changed input")
	}
	data, _ := os.ReadFile(path)
	if string(data) != `{"a":2}` {
		t.Fatal("source changed")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }
func TestOutputFailure(t *testing.T) {
	if code := run([]string{"-f", "json", "a"}, strings.NewReader(`{"a":1}`), failingWriter{}, io.Discard); code != 2 {
		t.Fatalf("code=%d", code)
	}
}

func TestCLIStyle(t *testing.T) {
	for _, tt := range []struct {
		args      []string
		src, want string
		code      int
	}{
		{[]string{"-f", "toml", "new.v=2"}, "old={v=1}\n", "old={v=1}\nnew={v=2}\n", 0},
		{[]string{"-f", "toml", "--style", "block", "new.v=2"}, "old={v=1}\n", "old={v=1}\n\n[new]\nv=2\n", 0},
		{[]string{"-f", "toml", "--style", "flow", "--object-style", "block", "new.v=2"}, "old={v=1}\n", "old={v=1}\n\n[new]\nv=2\n", 0},
		{[]string{"-f", "toml", "--object-style", "block", "--style", "flow", "new.v=2"}, "old={v=1}\n", "old={v=1}\n\n[new]\nv=2\n", 0},
		{[]string{"-f", "toml", "--object-style", "auto", "--style", "block", "new.v=2"}, "old={v=1}\n", "old={v=1}\nnew={v=2}\n", 0},
		{[]string{"-f", "toml", "--array-style", "block", "--indent", "4", "new=[2,3]"}, "old=1\n", "old=1\nnew=[\n    2,\n    3\n]\n", 0},
		{[]string{"-f", "yaml", "--key-style", "single", "--string-style", "single", "new=\"yes\""}, "old: before\n", "old: before\n'new': 'yes'\n", 0},
		{[]string{"-f", "toml", "--number-style", "hex", "new=255"}, "old=1\n", "old=1\nnew=0xff\n", 0},
		{[]string{"-f", "yaml", "--indent", "tab", "new=1"}, "old: 1\n", "", 2},
		{[]string{"-f", "toml", "--indent", "0", "new=1"}, "old=1\n", "", 2},
		{[]string{"-f", "yaml", "--style", "inline", "new=1"}, "old: 1\n", "", 2},
		{[]string{"-f", "yaml", "--number-style", "binary", "new=1"}, "old: 1\n", "", 2},
		{[]string{"-f", "json", "--style", "block", "new=1"}, "{}", "", 2},
		{[]string{"-f", "toml", "--line-ending", "crlf", "new=2"}, "old=1\n", "old=1\nnew=2\r\n", 0},
		{[]string{"-f", "toml", "--style", "block", "--check", "new.v=1"}, "old=1\n", "", 1},
	} {
		code, out, err := invoke(tt.args, tt.src)
		if code != tt.code || out != tt.want {
			t.Errorf("%v: code=%d out=%q err=%s, want %d %q", tt.args, code, out, err, tt.code, tt.want)
		}
		if code == 2 && err == "" {
			t.Error("missing diagnostic")
		}
	}
}

func TestCLIBytecode(t *testing.T) {
	dir := t.TempDir()
	queryFile := filepath.Join(dir, "program.tql")
	imageFile := filepath.Join(dir, "program.tqlc")
	asmFile := filepath.Join(dir, "program.tqla")
	if err := os.WriteFile(queryFile, []byte(`a[] += 1; a`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, err := invoke([]string{"--query-file", queryFile, "--emit-bytecode", imageFile}, ""); code != 0 || out != "" {
		t.Fatalf("emit: %d %q %s", code, out, err)
	}
	if code, out, err := invoke([]string{"--program", imageFile, "-f", "json"}, `{"a":[1,2]}`); code != 0 || out != `{"a":[2,3]}` {
		t.Fatalf("run: %d %q %s", code, out, err)
	}
	code, text, err := invoke([]string{"--program", imageFile, "--disassemble"}, "")
	if code != 0 || !strings.Contains(text, ".version 1") || !strings.Contains(text, "HALT") {
		t.Fatalf("disassemble: %d %q %s", code, text, err)
	}
	if err := os.WriteFile(asmFile, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, err := invoke([]string{"--query-file", queryFile, "--emit-assembly", asmFile}, ""); code != 0 || out != "" {
		t.Fatalf("emit assembly: %d %q %s", code, out, err)
	}
	written, readErr := os.ReadFile(asmFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(written) != text {
		t.Fatal("emit-assembly differs from disassemble")
	}
	if code, out, err := invoke([]string{"--query-file", queryFile, "--emit-assembly", "-"}, ""); code != 0 || out != text {
		t.Fatalf("emit assembly stdout: %d %q %s", code, out, err)
	}
	if code, _, _ := invoke([]string{"--query-file", queryFile, "--emit-assembly", asmFile, "--emit-bytecode", imageFile}, ""); code != 2 {
		t.Fatal("conflicting output modes accepted")
	}
	if code, out, err := invoke([]string{"--assemble", asmFile, "-f", "json"}, `{"a":[1,2]}`); code != 0 || out != `{"a":[2,3]}` {
		t.Fatalf("assemble: %d %q %s", code, out, err)
	}
	if code, _, _ := invoke([]string{"--program", imageFile, "--assemble", asmFile}, ""); code != 2 {
		t.Fatal("mutually exclusive sources accepted")
	}
	if code, _, _ := invoke([]string{"--program", imageFile, "--emit-bytecode", "-"}, ""); code != 2 {
		t.Fatal("stdout image accepted")
	}
}
