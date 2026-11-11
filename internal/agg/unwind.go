package agg

import (
	"encoding/json"
	"maps"
	"strconv"
	"strings"

	"github.com/thadeu/ndb/internal/jsondoc"
	"github.com/thadeu/ndb/internal/query"
)

// $unwind turns one document into one row per element of an array, before
// $group. In each row, the path of the array holds that element.
//
//	{"$unwind": "$items"}
//	{"$unwind": {"path": "$items", "preserveNullAndEmptyArrays": true, "includeArrayIndex": "i"}}
//
// As in MongoDB: a document where the path is missing, null or an empty
// array gives no row (with preserveNullAndEmptyArrays: one row, where an
// empty array is left out); a value that is not an array gives one row
// with it. includeArrayIndex names a field that gets the place of the
// element (null when the value is not an array).
//
// A $match after $unwind filters the rows; it is tested on each row.
type unwind struct {
	path     []string
	preserve bool
	index    string

	// match is the $match stages after the $unwind, or nil.
	match *query.Query
}

func compileUnwind(arg json.RawMessage) (*unwind, error) {
	var long struct {
		Path     *string `json:"path"`
		Preserve bool    `json:"preserveNullAndEmptyArrays"`
		Index    string  `json:"includeArrayIndex"`
	}

	var path string

	if json.Unmarshal(arg, &path) != nil {
		var keys map[string]json.RawMessage

		if json.Unmarshal(arg, &keys) != nil {
			return nil, invalid(`$unwind takes "$path" or {"path": "$path", ...}`)
		}

		for k := range keys {
			if k != "path" && k != "preserveNullAndEmptyArrays" && k != "includeArrayIndex" {
				return nil, invalid("$unwind: unknown key %q", k)
			}
		}

		if json.Unmarshal(arg, &long) != nil || long.Path == nil {
			return nil, invalid(`$unwind needs "path": "$path"`)
		}

		path = *long.Path
	}

	name, ok := strings.CutPrefix(path, "$")
	if !ok || name == "" || strings.HasPrefix(name, "$") {
		return nil, invalid(`$unwind: the path must be "$" and a field, such as "$items"`)
	}

	if strings.HasPrefix(long.Index, "$") || strings.Contains(long.Index, ".") || long.Index == "_id" {
		return nil, invalid("$unwind: includeArrayIndex is the name of a top-level field, with no $, and not _id")
	}

	return &unwind{path: strings.Split(name, "."), preserve: long.Preserve, index: long.Index}, nil
}

// unwindMatch compiles the $match stages that come after the $unwind.
func (p *Pipeline) unwindMatch(elems []json.RawMessage) error {
	if len(elems) == 0 {
		return nil
	}

	body := map[string]any{"query": elems[0]}

	if len(elems) > 1 {
		body["query"] = map[string]any{"$and": elems}
	}

	q, err := p.parseQuery(body)
	p.unwind.match = q

	return err
}

// Unwound tells if the pipeline has an $unwind: then the group reads rows
// of Expand, not documents.
func (p *Pipeline) Unwound() bool {
	return p.unwind != nil
}

// UnwindPaths are the document paths that Expand reads besides Paths: the
// array, and the fields of the $match after the $unwind.
func (p *Pipeline) UnwindPaths() []string {
	if p.unwind == nil {
		return nil
	}

	out := []string{strings.Join(p.unwind.path, ".")}

	if p.unwind.match != nil {
		out = append(out, p.unwind.match.Paths()...)
	}

	return out
}

// Expand calls add with the group values of each row that the $unwind makes
// of the decoded document m, and the place of the row in the document. m is
// not changed.
func (p *Pipeline) Expand(m map[string]any, add func(vals []query.Value, elem int) error) error {
	u := p.unwind
	val, ok := jsondoc.Lookup(m, u.path)
	arr, isArr := val.([]any)

	emit := func(row map[string]any, elem int) error {
		if u.match != nil && !u.match.Match(row) {
			return nil
		}

		return add(p.Values(row), elem)
	}

	switch {
	case !ok || val == nil || isArr && len(arr) == 0:
		if !u.preserve {
			return nil
		}

		row := m

		// As MongoDB does, the kept row of an empty array has no field: a
		// group by it puts the row with the missing and null ones.
		if isArr {
			var leaf map[string]any

			row, leaf = unwindCopy(m, u.path)

			delete(leaf, u.path[len(u.path)-1])
		}

		return emit(u.withIndex(row, nil), 0)
	case !isArr:
		return emit(u.withIndex(m, nil), 0)
	}

	// One copy of the maps on the path serves every element: each row is
	// read by add before the next one is set.
	row, leaf := unwindCopy(m, u.path)

	for i, elem := range arr {
		leaf[u.path[len(u.path)-1]] = elem

		if u.index != "" {
			row[u.index] = json.Number(strconv.Itoa(i))
		}

		if err := emit(row, i); err != nil {
			return err
		}
	}

	return nil
}

// withIndex is m with the includeArrayIndex field set to v, when the stage
// has one.
func (u *unwind) withIndex(m map[string]any, v any) map[string]any {
	if u.index == "" {
		return m
	}

	row := maps.Clone(m)
	row[u.index] = v

	return row
}

// unwindCopy copies m and the objects on path, so the last key can be set
// with no change to m. It gives the copy and the object that holds the
// last key.
func unwindCopy(m map[string]any, path []string) (root, leaf map[string]any) {
	root = maps.Clone(m)
	leaf = root

	for _, key := range path[:len(path)-1] {
		next := maps.Clone(leaf[key].(map[string]any))
		leaf[key] = next
		leaf = next
	}

	return root, leaf
}
