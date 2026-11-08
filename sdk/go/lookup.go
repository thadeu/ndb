package ndb

import "encoding/json"

// Relation is one lookup of a search: the documents of another collection
// that a row points to (POST /{coll}/_search with "lookup"). Build it with
// BelongsTo, HasMany or HasOne; each method returns a new Relation.
//
//	orders.Query(ndb.Eq("status", "paid")).
//		Lookup("customer", ndb.BelongsTo("customers", "customer_id")).
//		Lookup("items", ndb.HasMany("order_items", "order_id").Sort(ndb.Asc("position")).Limit(50)).
//		All(ctx)
//
// The field of T that gets it carries the lookup tag option, so a write
// leaves it out: `ndb:"customer,lookup"`. Its type decodes with
// encoding/json (json tags).
type Relation struct {
	kind    string
	coll    string
	fk      string
	pk      string
	filters []Filter
	sort    sortOrder
	limit   int
	fields  []string
	nested  map[string]Relation
}

// BelongsTo: the foreign key is in this collection (row.fk == other._id).
// The row gets an object, or null.
func BelongsTo(coll, fk string) Relation {
	return Relation{kind: "belongs_to", coll: coll, fk: fk}
}

// HasMany: the foreign key is in the other collection (other.fk ==
// row._id). The row gets an array (100 documents unless Limit says).
func HasMany(coll, fk string) Relation {
	return Relation{kind: "has_many", coll: coll, fk: fk}
}

// HasOne is HasMany with one document: an object, or null.
func HasOne(coll, fk string) Relation {
	return Relation{kind: "has_one", coll: coll, fk: fk}
}

// PK sets the primary key the foreign key points to (default _id).
func (r Relation) PK(field string) Relation {
	r.pk = field

	return r
}

// Query filters the documents of a HasMany or HasOne.
func (r Relation) Query(filters ...Filter) Relation {
	r.filters = append(append([]Filter{}, r.filters...), filters...)

	return r
}

// Sort orders the documents of a HasMany or HasOne.
func (r Relation) Sort(keys ...SortKey) Relation {
	r.sort = append(sortOrder{}, keys...)

	return r
}

// Limit caps the documents of a HasMany (1 to 1000).
func (r Relation) Limit(n int) Relation {
	r.limit = n

	return r
}

// Fields keeps these fields of the related documents.
func (r Relation) Fields(names ...string) Relation {
	r.fields = append([]string{}, names...)

	return r
}

// Lookup nests a relation of the related documents (two levels at most).
func (r Relation) Lookup(name string, rel Relation) Relation {
	nested := make(map[string]Relation, len(r.nested)+1)

	for k, v := range r.nested {
		nested[k] = v
	}

	nested[name] = rel
	r.nested = nested

	return r
}

func (r Relation) MarshalJSON() ([]byte, error) {
	out := map[string]any{r.kind: r.coll, "fk": r.fk}

	if r.pk != "" {
		out["pk"] = r.pk
	}

	if len(r.filters) > 0 {
		out["query"] = queryOf(r.filters)
	}

	if len(r.sort) > 0 {
		out["sort"] = r.sort
	}

	if r.limit > 0 {
		out["limit"] = r.limit
	}

	if len(r.fields) > 0 {
		out["fields"] = r.fields
	}

	if len(r.nested) > 0 {
		out["lookup"] = r.nested
	}

	return json.Marshal(out)
}

// Lookup adds a relation to the search: the rows get it under name.
func (q *Query[T]) Lookup(name string, rel Relation) *Query[T] {
	if q.lookup == nil {
		q.lookup = map[string]Relation{}
	}

	q.lookup[name] = rel

	return q
}
