package query

import (
	"encoding/json"
	"github.com/thadeu/ndb/internal/jsondoc"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Kind uint8

// The order of the constants is the cross-type order.
const (
	Missing Kind = iota
	Null
	Number
	String
	Time
	Object
	Array
	Bool
)

// Value is a JSON value with its type order. The zero Value is Missing.
type Value struct {
	Kind Kind
	Num  float64
	Int  int64  // Time: unix nanoseconds
	Str  string // String, or canonical JSON of Object and Array
	Bool bool

	// Raw is the decoded JSON value, kept for cursors.
	Raw any
}

// Decode reads a document with numbers as json.Number.
func Decode(raw []byte) (map[string]any, error) {
	return jsondoc.Decode(raw)
}

// FromJSON classifies a decoded JSON value. With isTime, a string in
// RFC 3339 becomes a Time.
func FromJSON(v any, isTime bool) Value {
	switch x := v.(type) {
	case nil:
		return Value{Kind: Null, Raw: v}
	case json.Number:
		f, _ := x.Float64()

		return Value{Kind: Number, Num: f + 0, Raw: v}
	case float64:
		return Value{Kind: Number, Num: x + 0, Raw: v}
	case int:
		return Value{Kind: Number, Num: float64(x), Raw: v}
	case string:
		if isTime {
			if t, ok := parseTime(x); ok {
				return Value{Kind: Time, Int: t, Raw: v}
			}
		}

		return Value{Kind: String, Str: x, Raw: v}
	case bool:
		return Value{Kind: Bool, Bool: x, Raw: v}
	case map[string]any:
		return Value{Kind: Object, Str: canon(v), Raw: v}
	case []any:
		return Value{Kind: Array, Str: canon(v), Raw: v}
	}

	return Value{Kind: Missing}
}

func parseTime(s string) (int64, bool) {
	if len(s) < 20 {
		return 0, false
	}

	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.Year() < 1678 || t.Year() > 2261 {
		return 0, false
	}

	return t.UnixNano(), true
}

// Compare is the total order: kinds first, then values of the same kind.
func Compare(a, b Value) int {
	if a.Kind != b.Kind {
		if a.Kind < b.Kind {
			return -1
		}

		return 1
	}

	switch a.Kind {
	case Number:
		return cmpOrdered(a.Num, b.Num)
	case Time:
		return cmpOrdered(a.Int, b.Int)
	case String, Object, Array:
		return strings.Compare(a.Str, b.Str)
	case Bool:
		if a.Bool == b.Bool {
			return 0
		}

		if !a.Bool {
			return -1
		}

		return 1
	}

	return 0
}

func cmpOrdered[T int64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

// Scalar reports whether v can be an index key.
func (v Value) Scalar() bool {
	return v.Kind == Null || v.Kind == Number || v.Kind == String || v.Kind == Time || v.Kind == Bool
}

// canon is JSON with sorted keys and normalized numbers.
func canon(v any) string {
	var sb strings.Builder

	writeCanon(&sb, v)

	return sb.String()
}

func writeCanon(sb *strings.Builder, v any) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))

		for k := range x {
			keys = append(keys, k)
		}

		slices.Sort(keys)
		sb.WriteByte('{')

		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}

			b, _ := json.Marshal(k)
			sb.Write(b)
			sb.WriteByte(':')
			writeCanon(sb, x[k])
		}

		sb.WriteByte('}')
	case []any:
		sb.WriteByte('[')

		for i, e := range x {
			if i > 0 {
				sb.WriteByte(',')
			}

			writeCanon(sb, e)
		}

		sb.WriteByte(']')
	case json.Number:
		f, _ := x.Float64()
		sb.WriteString(strconv.FormatFloat(f+0, 'g', -1, 64))
	case float64:
		sb.WriteString(strconv.FormatFloat(x+0, 'g', -1, 64))
	default:
		b, _ := json.Marshal(x)
		sb.Write(b)
	}
}
