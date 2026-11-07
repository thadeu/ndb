package segment

import (
	"encoding/json"
	"fmt"
	"github.com/thadeu/ndb/internal/jsondoc"
	"sort"
	"strconv"
	"strings"
)

// Schema rules (plan §7.4). Objects flatten to dot paths up to depth 4;
// deeper objects are JSON. Arrays of one scalar kind are lists; other arrays
// are JSON. A path with more than one kind keeps its dominant kind; the other
// values go to _rest. Strings over 4 KiB are not columns, and neither is any
// path that repeats inside one document.

const (
	maxDepth      = 4
	maxValueBytes = 4 << 10
)

var reserved = map[string]bool{
	"_id": true, "_v": true, "_seq": true, "_tx": true, "_del": true, "_raw": true, "_rest": true,
}

type kind uint8

const (
	kBool kind = iota + 1
	kInt
	kFloat
	kString
	kJSON
	kListBool
	kListInt
	kListFloat
	kListString
)

var kindNames = map[kind]string{
	kBool:       "bool",
	kInt:        "int",
	kFloat:      "float",
	kString:     "string",
	kJSON:       "json",
	kListBool:   "list<bool>",
	kListInt:    "list<int>",
	kListFloat:  "list<float>",
	kListString: "list<string>",
}

// cell is one value of one path. v is bool, int64, float64, string,
// []any of one scalar type (lists) or json.RawMessage (json).
type cell struct {
	k kind
	v any
}

type column struct {
	name  string
	kind  kind
	mixed bool
}

// infer returns the columns of a segment and its number of rows. It keeps
// only counts, not the cells: encodeTo flattens each row again.
func infer(rows Rows) ([]column, int, error) {
	bad := map[string]bool{}
	counts := map[string]map[kind]int{}
	n := 0

	err := rows(func(r Row) error {
		n++

		if r.Del {
			return nil
		}

		cells, paths, err := flatten(r.Raw)
		if err != nil {
			return fmt.Errorf("segment: row %s: %w", r.ID, err)
		}

		for _, p := range paths {
			bad[p] = true
		}

		for p, cl := range cells {
			if counts[p] == nil {
				counts[p] = map[kind]int{}
			}

			counts[p][cl.k]++
		}

		return nil
	})
	if err != nil {
		return nil, 0, err
	}

	columns := make([]column, 0, len(counts))

	for p, byKind := range counts {
		if bad[p] || reserved[p] {
			continue
		}

		columns = append(columns, column{name: p, kind: dominant(byKind), mixed: len(byKind) > 1})
	}

	sort.Slice(columns, func(a, b int) bool { return columns[a].name < columns[b].name })

	return columns, n, nil
}

// cellsOf returns the cells of one row; a tombstone has none.
func cellsOf(r Row) (map[string]cell, error) {
	if r.Del {
		return nil, nil
	}

	cells, _, err := flatten(r.Raw)
	if err != nil {
		return nil, fmt.Errorf("segment: row %s: %w", r.ID, err)
	}

	return cells, nil
}

// dominant picks the kind seen most often; on a tie, the lower kind wins.
func dominant(byKind map[kind]int) kind {
	var best kind

	for k := kBool; k <= kListString; k++ {
		if byKind[k] > byKind[best] {
			best = k
		}
	}

	return best
}

// flatten returns the cells of one document. The paths that cannot be
// columns (too large, or repeated) come back in bad.
func flatten(doc []byte) (map[string]cell, []string, error) {
	m, err := jsondoc.Decode(doc)
	if err != nil {
		return nil, nil, err
	}

	f := &flattener{cells: map[string]cell{}}

	f.object("", m)

	return f.cells, f.bad, nil
}

type flattener struct {
	cells map[string]cell
	bad   []string
}

func (f *flattener) object(prefix string, m map[string]any) {
	for k, v := range m {
		path := k

		if prefix != "" {
			path = prefix + "." + k
		}

		f.value(path, v)
	}
}

// value adds one leaf. Null and an empty array have no type, so they make no cell.
func (f *flattener) value(path string, v any) {
	if arr, ok := v.([]any); v == nil || (ok && len(arr) == 0) {
		return
	}

	if obj, ok := v.(map[string]any); ok && strings.Count(path, ".") < maxDepth-1 {
		f.object(path, obj)

		return
	}

	c, ok := typed(v)

	_, dup := f.cells[path]

	if !ok || dup || tooBig(c) {
		f.bad = append(f.bad, path)

		return
	}

	f.cells[path] = c
}

func typed(v any) (cell, bool) {
	switch x := v.(type) {
	case bool:
		return cell{kBool, x}, true
	case string:
		return cell{kString, x}, true
	case json.Number:
		return number(x)
	case []any:
		return list(x)
	}

	return jsonCell(v)
}

func number(n json.Number) (cell, bool) {
	if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return cell{kInt, i}, true
	}

	if f, err := strconv.ParseFloat(string(n), 64); err == nil {
		return cell{kFloat, f}, true
	}

	return jsonCell(n)
}

// list returns a list cell when every element is one scalar kind.
func list(xs []any) (cell, bool) {
	if len(xs) == 0 {
		return jsonCell(xs)
	}

	first, ok := scalar(xs[0])

	if !ok {
		return jsonCell(xs)
	}

	out := make([]any, len(xs))

	for i, x := range xs {
		c, ok := scalar(x)

		if !ok || c.k != first.k {
			return jsonCell(xs)
		}

		out[i] = c.v
	}

	return cell{listKind(first.k), out}, true
}

func scalar(v any) (cell, bool) {
	switch x := v.(type) {
	case bool:
		return cell{kBool, x}, true
	case string:
		return cell{kString, x}, true
	case json.Number:
		c, ok := number(x)

		return c, ok && c.k != kJSON
	}

	return cell{}, false
}

func listKind(k kind) kind {
	switch k {
	case kBool:
		return kListBool
	case kInt:
		return kListInt
	case kFloat:
		return kListFloat
	}

	return kListString
}

func jsonCell(v any) (cell, bool) {
	b, err := json.Marshal(v)
	if err != nil {
		return cell{}, false
	}

	return cell{kJSON, json.RawMessage(b)}, true
}

func tooBig(c cell) bool {
	switch v := c.v.(type) {
	case string:
		return len(v) > maxValueBytes
	case json.RawMessage:
		return len(v) > maxValueBytes
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && len(s) > maxValueBytes {
				return true
			}
		}
	}

	return false
}
