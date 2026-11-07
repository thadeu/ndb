package query

import "slices"

// IndexOnly reports the one path whose index answers the whole filter, so
// that each candidate of that index matches without reading the document.
//
// That is true when the filter is only eq, in, gt, gte, lt or lte on one
// path, with scalar values that are not null, and either one point condition
// (eq or in) or at most one lower and one upper bound of the same type. The
// index keeps one key per array element and compares inside one type, like
// Match, so its candidates are exactly the matches.
func (q *Query) IndexOnly() (string, bool) {
	var fields []*fieldNode

	if !flatFields(q.where, &fields) || len(fields) == 0 {
		return "", false
	}

	path := fields[0].path

	var ops []op

	for _, f := range fields {
		if f.path != path {
			return "", false
		}

		ops = append(ops, f.ops...)
	}

	if !exactOps(ops) {
		return "", false
	}

	return path, true
}

// flatFields collects the field nodes of an and of fields. Any other node
// (or, not) makes it false.
func flatFields(n Node, out *[]*fieldNode) bool {
	switch x := n.(type) {
	case andNode:
		for _, c := range x {
			if !flatFields(c, out) {
				return false
			}
		}

		return true
	case *fieldNode:
		*out = append(*out, x)

		return true
	}

	return false
}

func exactOps(ops []op) bool {
	var point, lower, upper int

	for _, o := range ops {
		if !indexableOp(o) || slices.ContainsFunc(o.vals, func(v Value) bool { return v.Kind == Null }) {
			return false
		}

		switch o.name {
		case "eq", "in":
			point++
		case "gt", "gte":
			lower++
		default:
			upper++
		}
	}

	if point > 0 {
		return point == 1 && lower+upper == 0
	}

	if lower > 1 || upper > 1 {
		return false
	}

	return len(ops) == 1 || ops[0].vals[0].Kind == ops[1].vals[0].Kind
}
