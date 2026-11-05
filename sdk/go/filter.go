package ndb

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

// Or matches when at least one of the filters matches.
func Or(filters ...Filter) Filter {
	return Filter{doc: map[string]any{"$or": docsOf(filters)}}
}

// Not matches when f does not match.
func Not(f Filter) Filter {
	return Filter{doc: map[string]any{"$not": f.doc}}
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
