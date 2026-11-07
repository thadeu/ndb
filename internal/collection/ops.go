package collection

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/thadeu/ndb/internal/query"
)

// Write operators of an update body. The other top-level keys are the
// filter. See update.go for how the two parts are split.
//
// $setOnInsert works like $set, but only when an upsert creates the
// document. On a match it is ignored, so a racing writer's data is never
// overwritten. It counts in the conflict checks like any other operator.

var (
	writeOperators  = []string{"$set", "$setOnInsert", "$unset", "$inc", "$push", "$pull", "$min", "$max"}
	filterOperators = []string{"$and", "$or", "$nor", "$not"}
)

// fieldOp is one operator applied to one field path.
type fieldOp struct {
	op   string
	path string
	val  any

	// pull is the element test of $pull.
	pull func(elem any) bool
}

// JoinUpdate checks the two parts of an update body of the API, {"query":
// {...}, "update": {...}}, and joins them into the one object that Update
// reads: the write operators belong in update only, the filter in query
// only.
func JoinUpdate(q, update map[string]any) (map[string]any, error) {
	if len(update) == 0 {
		return nil, errf("no_update", "update needs a write operator (%s)", strings.Join(writeOperators, ", "))
	}

	out := make(map[string]any, len(q)+len(update))

	for k, v := range update {
		if !slices.Contains(writeOperators, k) {
			return nil, errf("unknown_operator", "%s is not a write operator; a filter goes in query", k)
		}

		out[k] = v
	}

	for k, v := range q {
		if slices.Contains(writeOperators, k) {
			return nil, errf("unknown_operator", "%s is a write operator; it goes in update, not in query", k)
		}

		out[k] = v
	}

	return out, nil
}

// splitBody separates the filter keys from the write operators of a decoded
// body. A top-level "$" key that is neither is unknown_operator.
func splitBody(top map[string]any) (map[string]any, []fieldOp, error) {
	filter := map[string]any{}
	groups := map[string]map[string]any{}

	for k, v := range top {
		switch {
		case slices.Contains(writeOperators, k):
			fields, ok := v.(map[string]any)
			if !ok {
				return nil, nil, errf("invalid_field", "%s needs an object of fields", k)
			}

			groups[k] = fields
		case slices.Contains(filterOperators, k) || !strings.HasPrefix(k, "$"):
			filter[k] = v
		default:
			return nil, nil, errf("unknown_operator", "%s is not a filter or write operator", k)
		}
	}

	if len(groups) == 0 {
		return nil, nil, errf("no_update", "the body has no write operator (%s)", strings.Join(writeOperators, ", "))
	}

	ops, err := buildOps(groups)
	if err != nil {
		return nil, nil, err
	}

	return filter, ops, checkConflicts(ops)
}

// splitFilter checks that a delete body is a filter only.
func splitFilter(top map[string]any) (map[string]any, error) {
	for k := range top {
		if strings.HasPrefix(k, "$") && !slices.Contains(filterOperators, k) {
			return nil, errf("unknown_operator", "%s is not a filter operator", k)
		}
	}

	return top, nil
}

func buildOps(groups map[string]map[string]any) ([]fieldOp, error) {
	var out []fieldOp

	for _, name := range writeOperators {
		fields := groups[name]
		paths := make([]string, 0, len(fields))

		for p := range fields {
			paths = append(paths, p)
		}

		sort.Strings(paths)

		for _, p := range paths {
			if err := checkOpPath(name, p); err != nil {
				return nil, err
			}

			fo, err := newFieldOp(name, p, fields[p])
			if err != nil {
				return nil, err
			}

			out = append(out, fo)
		}
	}

	return out, nil
}

func checkOpPath(op, path string) error {
	parts := strings.Split(path, ".")

	for _, p := range parts {
		if p == "" {
			return errf("invalid_field", "%s: bad field path %q", op, path)
		}

		if strings.HasPrefix(p, "$") {
			return errf("invalid_field", "%s: field %q starts with $", op, path)
		}
	}

	if parts[0] == "_v" {
		return errf("invalid_field", "%s: _v is the document version and cannot be written", op)
	}

	if parts[0] == idField {
		return errf("immutable_id", "%s cannot change", idField)
	}

	return nil
}

func newFieldOp(op, path string, val any) (fieldOp, error) {
	fo := fieldOp{op: op, path: path, val: val}

	switch op {
	case "$inc":
		if _, ok := val.(json.Number); !ok {
			return fo, errf("invalid_field", "$inc.%s needs a number", path)
		}
	case "$push":
		if err := checkPush(path, val); err != nil {
			return fo, err
		}
	case "$pull":
		test, err := pullTest(val)
		if err != nil {
			return fo, errf("invalid_field", "$pull.%s: %v", path, err)
		}

		fo.pull = test
	case "$min", "$max":
		k := query.FromJSON(val, false).Kind

		if k != query.Number && k != query.String {
			return fo, errf("invalid_field", "%s.%s needs a number or a string", op, path)
		}
	}

	return fo, nil
}

func checkPush(path string, val any) error {
	m, ok := val.(map[string]any)
	if !ok {
		return nil
	}

	each, has := m["$each"]
	if !has {
		return nil
	}

	if _, ok := each.([]any); !ok || len(m) != 1 {
		return errf("invalid_field", "$push.%s: $each needs an array and nothing else", path)
	}

	return nil
}

// pullTest compiles the element test of $pull. A scalar or an array is an
// equality; an object with operator keys is a condition on the element
// ({"$gte": 5}); any other object is a where on object elements.
func pullTest(val any) (func(any) bool, error) {
	m, isObj := val.(map[string]any)

	if isObj && isWhere(m) {
		q, err := query.ParseWhere(m, query.Options{})
		if err != nil {
			return nil, err
		}

		return func(elem any) bool {
			em, ok := elem.(map[string]any)

			return ok && q.Match(em)
		}, nil
	}

	q, err := query.ParseWhere(map[string]any{"v": val}, query.Options{})
	if err != nil {
		return nil, err
	}

	return func(elem any) bool {
		return q.Match(map[string]any{"v": elem})
	}, nil
}

// isWhere tells a where object from an operator object: it has no "$" key,
// or it has a boolean filter operator.
func isWhere(m map[string]any) bool {
	dollar := 0

	for k := range m {
		if strings.HasPrefix(k, "$") {
			dollar++
		}

		if slices.Contains(filterOperators, k) {
			return true
		}
	}

	return dollar == 0
}

// checkConflicts rejects two ops on the same field, or on a field and one of
// its parents. The order of a map would decide the result otherwise.
func checkConflicts(ops []fieldOp) error {
	for i, a := range ops {
		for _, b := range ops[i+1:] {
			if a.path == b.path || strings.HasPrefix(a.path, b.path+".") || strings.HasPrefix(b.path, a.path+".") {
				return errf("conflicting_update", "%s and %s both write %s", a.op, b.op, a.path)
			}
		}
	}

	return nil
}

// applyOps changes m in place and tells whether anything changed.
func applyOps(m map[string]any, ops []fieldOp) (bool, error) {
	changed := false

	for _, o := range ops {
		c, err := applyOp(m, o)
		if err != nil {
			return false, err
		}

		changed = changed || c
	}

	return changed, nil
}

func applyOp(m map[string]any, o fieldOp) (bool, error) {
	cur, exists := getPath(m, o.path)

	switch o.op {
	case "$set":
		if exists && sameJSON(cur, o.val) {
			return false, nil
		}

		return true, setPath(m, o.path, o.val)
	case "$setOnInsert":
		return false, nil
	case "$unset":
		if !exists {
			return false, nil
		}

		unsetPath(m, o.path)

		return true, nil
	case "$inc":
		return incField(m, o, cur, exists)
	case "$push":
		return pushField(m, o, cur, exists)
	case "$pull":
		return pullField(m, o, cur, exists)
	}

	return boundField(m, o, cur, exists)
}

func sameJSON(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)

	return err1 == nil && err2 == nil && bytes.Equal(x, y)
}

func incField(m map[string]any, o fieldOp, cur any, exists bool) (bool, error) {
	delta := o.val.(json.Number)

	var base json.Number = "0"

	if exists {
		n, ok := cur.(json.Number)
		if !ok {
			return false, errf("invalid_field", "$inc.%s: the field is not a number", o.path)
		}

		base = n
	}

	sum, err := addNumbers(base, delta)
	if err != nil {
		return false, errf("invalid_field", "$inc.%s: %v", o.path, err)
	}

	if exists && numEqual(base, sum) {
		return false, nil
	}

	return true, setPath(m, o.path, sum)
}

func numEqual(a, b json.Number) bool {
	x, err1 := a.Float64()
	y, err2 := b.Float64()

	return err1 == nil && err2 == nil && x == y
}

// addNumbers keeps integers exact and falls back to float64 on overflow or
// a fraction.
func addNumbers(a, b json.Number) (json.Number, error) {
	x, err1 := a.Int64()
	y, err2 := b.Int64()

	if err1 == nil && err2 == nil {
		overflow := (y > 0 && x > math.MaxInt64-y) || (y < 0 && x < math.MinInt64-y)

		if !overflow {
			return json.Number(strconv.FormatInt(x+y, 10)), nil
		}
	}

	f, err := a.Float64()
	if err != nil {
		return "", err
	}

	g, err := b.Float64()
	if err != nil {
		return "", err
	}

	sum := f + g

	if math.IsInf(sum, 0) || math.IsNaN(sum) {
		return "", errf("invalid_field", "the result is not a finite number")
	}

	return json.Number(strconv.FormatFloat(sum, 'g', -1, 64)), nil
}

func pushField(m map[string]any, o fieldOp, cur any, exists bool) (bool, error) {
	var arr []any

	if exists {
		a, ok := cur.([]any)
		if !ok {
			return false, errf("invalid_field", "$push.%s: the field is not an array", o.path)
		}

		arr = a
	}

	add := []any{o.val}

	if obj, ok := o.val.(map[string]any); ok {
		if each, has := obj["$each"]; has {
			add = each.([]any)
		}
	}

	if arr == nil {
		arr = []any{}
	}

	out := make([]any, 0, len(arr)+len(add))
	out = append(out, arr...)
	out = append(out, add...)

	return true, setPath(m, o.path, out)
}

func pullField(m map[string]any, o fieldOp, cur any, exists bool) (bool, error) {
	if !exists {
		return false, nil
	}

	arr, ok := cur.([]any)
	if !ok {
		return false, errf("invalid_field", "$pull.%s: the field is not an array", o.path)
	}

	out := make([]any, 0, len(arr))

	for _, e := range arr {
		if !o.pull(e) {
			out = append(out, e)
		}
	}

	if len(out) == len(arr) {
		return false, nil
	}

	return true, setPath(m, o.path, out)
}

// boundField is $min and $max: set the field when it is missing or the new
// value is lower (higher). Numbers and strings only, both of one type.
func boundField(m map[string]any, o fieldOp, cur any, exists bool) (bool, error) {
	if !exists {
		return true, setPath(m, o.path, o.val)
	}

	a := query.FromJSON(cur, false)
	b := query.FromJSON(o.val, false)

	if a.Kind != b.Kind {
		return false, errf("invalid_field", "%s.%s: the field and the value are not the same type", o.op, o.path)
	}

	d := query.Compare(b, a)

	if (o.op == "$min" && d >= 0) || (o.op == "$max" && d <= 0) {
		return false, nil
	}

	return true, setPath(m, o.path, o.val)
}
