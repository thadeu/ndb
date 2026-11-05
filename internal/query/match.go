package query

import "slices"

// Node is a compiled where clause.
type Node interface {
	match(m map[string]any) bool
}

type andNode []Node

func (n andNode) match(m map[string]any) bool {
	for _, c := range n {
		if !c.match(m) {
			return false
		}
	}

	return true
}

type orNode []Node

func (n orNode) match(m map[string]any) bool {
	for _, c := range n {
		if c.match(m) {
			return true
		}
	}

	return false
}

type notNode struct{ inner Node }

func (n notNode) match(m map[string]any) bool {
	return !n.inner.match(m)
}

// op is one operator: name has no "$". sub is used by "not".
type op struct {
	name string
	vals []Value
	flag bool
	sub  []op
}

// fieldNode is the operators of one field, joined with AND.
type fieldNode struct {
	path   string
	parts  []string
	isTime bool
	ops    []op
}

func (f *fieldNode) match(m map[string]any) bool {
	var leaves []any

	found := resolve(m, f.parts, &leaves)
	cands := candidates(leaves, f.isTime)

	return matchAll(f.ops, cands, found)
}

func matchAll(ops []op, cands []Value, found bool) bool {
	for _, o := range ops {
		if !matchOp(o, cands, found) {
			return false
		}
	}

	return true
}

func matchOp(o op, cands []Value, found bool) bool {
	switch o.name {
	case "eq":
		return anyEq(cands, o.vals[0])
	case "ne":
		return !anyEq(cands, o.vals[0])
	case "in":
		return slices.ContainsFunc(o.vals, func(v Value) bool { return anyEq(cands, v) })
	case "nin":
		return !slices.ContainsFunc(o.vals, func(v Value) bool { return anyEq(cands, v) })
	case "exists":
		return found == o.flag
	case "not":
		return !matchAll(o.sub, cands, found)
	}

	return slices.ContainsFunc(cands, func(c Value) bool {
		if c.Kind != o.vals[0].Kind {
			return false
		}

		d := Compare(c, o.vals[0])

		switch o.name {
		case "gt":
			return d > 0
		case "gte":
			return d >= 0
		case "lt":
			return d < 0
		}

		return d <= 0
	})
}

func anyEq(cands []Value, v Value) bool {
	return slices.ContainsFunc(cands, func(c Value) bool {
		return c.Kind == v.Kind && Compare(c, v) == 0
	})
}

// resolve collects the values at parts, walking through arrays of objects.
// It reports whether the path exists.
func resolve(cur any, parts []string, out *[]any) bool {
	if len(parts) == 0 {
		*out = append(*out, cur)

		return true
	}

	switch c := cur.(type) {
	case map[string]any:
		next, ok := c[parts[0]]
		if !ok {
			return false
		}

		return resolve(next, parts[1:], out)
	case []any:
		found := false

		for _, el := range c {
			if resolve(el, parts, out) {
				found = true
			}
		}

		return found
	}

	return false
}

// Elements are the values of a field for sort and indexes: array leaves
// are replaced by their elements.
func elements(leaves []any, isTime bool) []Value {
	out := make([]Value, 0, len(leaves))

	for _, l := range leaves {
		if arr, ok := l.([]any); ok {
			for _, e := range arr {
				out = append(out, FromJSON(e, isTime))
			}

			continue
		}

		out = append(out, FromJSON(l, isTime))
	}

	return out
}

// candidates are the elements plus every whole array, so {tags: ["a"]}
// can match the array itself.
func candidates(leaves []any, isTime bool) []Value {
	out := elements(leaves, isTime)

	for _, l := range leaves {
		if _, ok := l.([]any); ok {
			out = append(out, FromJSON(l, isTime))
		}
	}

	return out
}

// Elements returns the index keys of doc at path: one per array element.
func Elements(m map[string]any, path string, isTime bool) []Value {
	var leaves []any

	if !resolve(m, splitPath(path), &leaves) {
		return nil
	}

	return elements(leaves, isTime)
}
