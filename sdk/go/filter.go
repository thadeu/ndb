package ndb

import "maps"

// Filter is one condition of a Where. The filters of one Where are ANDed.
// A Filter only holds JSON for the daemon; it does no matching itself.
type Filter struct {
	doc map[string]any
}

// Eq matches a field equal to v.
func Eq(field string, v any) Filter {
	return Filter{doc: map[string]any{field: v}}
}

// Ne matches a field not equal to v.
func Ne(field string, v any) Filter {
	return operator(field, "$ne", v)
}

// Gt matches a field greater than v.
func Gt(field string, v any) Filter {
	return operator(field, "$gt", v)
}

// Gte matches a field greater than or equal to v.
func Gte(field string, v any) Filter {
	return operator(field, "$gte", v)
}

// Lt matches a field less than v.
func Lt(field string, v any) Filter {
	return operator(field, "$lt", v)
}

// Lte matches a field less than or equal to v.
func Lte(field string, v any) Filter {
	return operator(field, "$lte", v)
}

// In matches a field equal to one of values.
func In(field string, values ...any) Filter {
	list := append([]any{}, values...)

	return operator(field, "$in", list)
}

// Exists matches documents that have the field (exists true) or do not
// have it (exists false).
func Exists(field string, exists bool) Filter {
	return operator(field, "$exists", exists)
}

// RegexOption is a flag of Regex. The values are the $options letters.
type RegexOption string

const (
	// IgnoreCase makes the pattern case insensitive (i).
	IgnoreCase RegexOption = "i"

	// Multiline makes ^ and $ match at each line (m).
	Multiline RegexOption = "m"

	// DotAll makes . match a newline (s).
	DotAll RegexOption = "s"

	// Extended ignores white space and # comments in the pattern (x).
	Extended RegexOption = "x"
)

// Regex matches a string field that matches pattern. The syntax is Go RE2:
// no backreferences and no look-around. Only strings match; on an array
// field, any string element may match. A bad pattern makes the query fail
// with ErrInvalidQuery.
func Regex(field, pattern string, opts ...RegexOption) Filter {
	cond := map[string]any{"$regex": pattern}

	if len(opts) > 0 {
		var flags string

		for _, o := range opts {
			flags += string(o)
		}

		cond["$options"] = flags
	}

	return Filter{doc: map[string]any{field: cond}}
}

// And matches when every filter matches. A Filter argument of Update or
// Delete is one filter; use And to give it several conditions.
func And(filters ...Filter) Filter {
	return Filter{doc: whereOf(filters)}
}

// Or matches when at least one of the filters matches.
func Or(filters ...Filter) Filter {
	return Filter{doc: map[string]any{"$or": docsOf(filters)}}
}

// Not matches when f does not match.
func Not(f Filter) Filter {
	return Filter{doc: map[string]any{"$not": f.doc}}
}

// ElemMatch matches when one element of the array field matches every
// filter: ElemMatch("items", Eq("sku", "a"), Gt("qty", 2)). The filters name
// fields of the element. For an array of scalars, use an empty field name
// for the element itself: ElemMatch("scores", Gte("", 10), Lt("", 20)).
func ElemMatch(field string, filters ...Filter) Filter {
	inner := map[string]any{}

	var fields []Filter

	for _, f := range filters {
		if ops, ok := f.doc[""].(map[string]any); ok && len(f.doc) == 1 {
			maps.Copy(inner, ops)

			continue
		}

		fields = append(fields, f)
	}

	maps.Copy(inner, whereOf(fields))

	return operator(field, "$elemMatch", inner)
}

func operator(field, name string, v any) Filter {
	return Filter{doc: map[string]any{field: map[string]any{name: v}}}
}

// whereOf joins the filters into one where object. One JSON object cannot
// hold two values for one key, so two filters on the same key use $and.
func whereOf(filters []Filter) map[string]any {
	out := map[string]any{}

	for _, f := range filters {
		for k, v := range f.doc {
			if _, dup := out[k]; dup {
				return map[string]any{"$and": docsOf(filters)}
			}

			out[k] = v
		}
	}

	return out
}

func docsOf(filters []Filter) []map[string]any {
	out := make([]map[string]any, len(filters))

	for i, f := range filters {
		out[i] = f.doc
	}

	return out
}
