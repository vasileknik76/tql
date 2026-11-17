package syntax

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vasileknik76/tql/internal/editmodel"
)

// Path is an immutable sequence of literal object keys and nonnegative array
// indexes. Its zero value addresses the document root.
type Path = editmodel.Path

// Root returns the document root path.
func Root() Path { return Path{} }

// Key returns a path through literal object keys, starting at the root.
func Key(keys ...string) Path { return Root().Key(keys...) }

// Operation is an immutable structured edit. Its zero value is invalid.
type Operation = editmodel.Operation

// OperationKind identifies a structured edit command.
type OperationKind = editmodel.Kind

const (
	// SetOperation assigns a value.
	SetOperation = editmodel.SetOperation
	// DelOperation removes an object member or array element.
	DelOperation = editmodel.DelOperation
)

// Set constructs an assignment, capturing a normalized snapshot of value.
// Invalid values or paths are reported when the command is applied or exported.
func Set(path Path, value any) Operation {
	v, err := NormalizeValue(value)
	return editmodel.New(SetOperation, path, v, err)
}

// NormalizeValue validates a Go value and returns an independent TQL value tree.
// Integers and finite floats become Number values; containers are copied deeply.
// Unsupported types, cycles, invalid strings and excessive nesting are errors.
func NormalizeValue(value any) (any, error) {
	return normalize(value, make(map[visit]bool), 0)
}

// Del constructs a deletion. Missing targets are no-ops; deleting the root is
// invalid. Errors are reported when the command is applied or exported.
func Del(path Path) Operation { return editmodel.New(DelOperation, path, nil, nil) }

type visit struct {
	kind reflect.Kind
	ptr  uintptr
	len  int
}

const maxValueDepth = 256

func normalize(v any, active map[visit]bool, depth int) (any, error) {
	if depth > maxValueDepth {
		return nil, fmt.Errorf("value nesting exceeds %d levels", maxValueDepth)
	}
	switch x := v.(type) {
	case nil, bool:
		return v, nil
	case string:
		if !utf8.ValidString(x) {
			return nil, fmt.Errorf("invalid UTF-8 string")
		}
		return x, nil
	case Number:
		if !jsonNumber.MatchString(string(x)) && x != "inf" && x != "+inf" && x != "-inf" && x != "nan" && x != "+nan" && x != "-nan" {
			return nil, fmt.Errorf("invalid number %q", x)
		}
		return x, nil
	case Temporal:
		if !validTemporal(string(x)) {
			return nil, fmt.Errorf("invalid TOML date/time %q", x)
		}
		return x, nil
	case int:
		return Number(strconv.FormatInt(int64(x), 10)), nil
	case int8:
		return Number(strconv.FormatInt(int64(x), 10)), nil
	case int16:
		return Number(strconv.FormatInt(int64(x), 10)), nil
	case int32:
		return Number(strconv.FormatInt(int64(x), 10)), nil
	case int64:
		return Number(strconv.FormatInt(x, 10)), nil
	case uint:
		return Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint8:
		return Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint16:
		return Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint32:
		return Number(strconv.FormatUint(uint64(x), 10)), nil
	case uint64:
		return Number(strconv.FormatUint(x, 10)), nil
	case uintptr:
		return Number(strconv.FormatUint(uint64(x), 10)), nil
	case float32:
		return normalizeFloat(float64(x), 32)
	case float64:
		return normalizeFloat(x, 64)
	case map[string]any:
		id := visit{kind: reflect.Map, ptr: reflect.ValueOf(x).Pointer()}
		if active[id] {
			return nil, fmt.Errorf("cyclic object value")
		}
		active[id] = true
		defer delete(active, id)
		out := make(map[string]any, len(x))
		for k, v := range x {
			if !utf8.ValidString(k) {
				return nil, fmt.Errorf("invalid UTF-8 object key")
			}
			next, err := normalize(v, active, depth+1)
			if err != nil {
				return nil, fmt.Errorf("key %q: %w", k, err)
			}
			out[k] = next
		}
		return out, nil
	case []any:
		id := visit{kind: reflect.Slice, ptr: reflect.ValueOf(x).Pointer(), len: len(x)}
		if len(x) > 0 && active[id] {
			return nil, fmt.Errorf("cyclic array value")
		}
		active[id] = true
		defer delete(active, id)
		out := make([]any, len(x))
		for i, v := range x {
			next, err := normalize(v, active, depth+1)
			if err != nil {
				return nil, fmt.Errorf("index %d: %w", i, err)
			}
			out[i] = next
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported value %T", v)
	}
}

func normalizeFloat(v float64, bits int) (any, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, fmt.Errorf("non-finite float; use Number for TOML special values")
	}
	return Number(strconv.FormatFloat(v, 'g', -1, bits)), nil
}

var temporalPattern = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}([Tt ]\d{2}:\d{2}(:\d{2}(\.[0-9]+)?)?([Zz]|[+-]\d{2}:\d{2})?)?|\d{2}:\d{2}(:\d{2}(\.[0-9]+)?)?)$`)

func validTemporal(s string) bool {
	if !temporalPattern.MatchString(s) {
		return false
	}
	if len(s) >= 6 && (s[len(s)-6] == '-' || s[len(s)-6] == '+') && s[len(s)-3] == ':' {
		zone := s[len(s)-5:]
		if zone[:2] > "23" || zone[3:] > "59" {
			return false
		}
	}
	s = strings.NewReplacer("t", "T", "z", "Z", " ", "T").Replace(s)
	clock := 0
	if len(s) > 10 && s[10] == 'T' {
		clock = 11
	}
	if len(s) >= clock+5 && s[clock+2] == ':' && (len(s) == clock+5 || s[clock+5] != ':') {
		s = s[:clock+5] + ":00" + s[clock+5:]
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02", "15:04:05.999999999"} {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}
