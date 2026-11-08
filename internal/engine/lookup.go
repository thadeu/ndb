package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/lookup"
)

// Lookup (M18) attaches to rows the documents of other collections that they
// point to. A belongs_to reads every key of the rows in one query ($in on
// the primary key); a has_many or has_one reads one query per row, with its
// own query, sort and limit, 4 at a time. The nested lookups run once on all
// the documents found. Each collection reads its own snapshot: there is no
// snapshot across collections.

// lookupWorkers is how many per-row queries of one relation run at once.
const lookupWorkers = 4

// lookupBudget is how many documents one answer may attach (tests lower it).
var lookupBudget = lookup.MaxAttached

// Lookup gives rows with the relations of specs set on them.
func (db *DB) Lookup(ctx context.Context, rows []json.RawMessage, specs []lookup.Spec) ([]json.RawMessage, error) {
	budget := lookupBudget

	return db.attachLookups(ctx, rows, specs, &budget)
}

func (db *DB) attachLookups(ctx context.Context, rows []json.RawMessage, specs []lookup.Spec, budget *int) ([]json.RawMessage, error) {
	for _, s := range specs {
		if err := db.checkIndexed(s); err != nil {
			return nil, err
		}

		values, err := db.relate(ctx, rows, s, budget)
		if err != nil {
			return nil, fmt.Errorf("lookup %s: %w", s.Name, err)
		}

		for i := range rows {
			rows[i] = lookup.Set(rows[i], s.Name, values[i])
		}
	}

	return rows, nil
}

// checkIndexed refuses a relation whose other side would be read whole: the
// foreign field must be _id or have an index.
func (db *DB) checkIndexed(s lookup.Spec) error {
	def, ok := db.cat.Get(s.From)
	if !ok {
		return fmt.Errorf("%w: %s (lookup %s)", catalog.ErrUnknown, s.From, s.Name)
	}

	if s.Foreign == "_id" || slices.Contains(def.Indexes, s.Foreign) {
		return nil
	}

	return lookup.NewError("lookup_unindexed", "lookup %s: %s.%s has no index; add it (PUT /%s/_indexes/%s) or declare it in indexes", s.Name, s.From, s.Foreign, s.From, s.Foreign)
}

// relate gives the value of the relation for each row.
func (db *DB) relate(ctx context.Context, rows []json.RawMessage, s lookup.Spec, budget *int) ([]json.RawMessage, error) {
	if s.Batched {
		return db.relateBatched(ctx, rows, s, budget)
	}

	return db.relatePerRow(ctx, rows, s, budget)
}

// relateBatched reads the documents of every key of the rows in one query.
func (db *DB) relateBatched(ctx context.Context, rows []json.RawMessage, s lookup.Spec, budget *int) ([]json.RawMessage, error) {
	seen := map[string]bool{}

	var in []json.RawMessage

	for _, row := range rows {
		for _, kv := range lookup.Keys(row, s.Local) {
			if !seen[kv.Key] {
				seen[kv.Key] = true
				in = append(in, kv.Raw)
			}
		}
	}

	byKey := map[string][]json.RawMessage{}

	if len(in) > 0 {
		filter := map[string]any{s.Foreign: map[string]any{"$in": in}}

		docs, added, err := db.lookupFind(ctx, s, filter, *budget+1, s.Foreign)
		if err != nil {
			return nil, err
		}

		if err := spend(budget, len(docs)); err != nil {
			return nil, err
		}

		if len(s.Nested) > 0 {
			if docs, err = db.attachLookups(ctx, docs, s.Nested, budget); err != nil {
				return nil, err
			}
		}

		for _, doc := range docs {
			for _, kv := range lookup.Keys(doc, s.Foreign) {
				byKey[kv.Key] = append(byKey[kv.Key], lookup.Without(doc, added))
			}
		}
	}

	out := make([]json.RawMessage, len(rows))

	for i, row := range rows {
		keys, array := lookup.KeysOf(row, s.Local)

		var found []json.RawMessage

		for _, kv := range keys {
			docs := byKey[kv.Key]

			if s.Many {
				found = append(found, docs...)
			} else if len(docs) > 0 {
				found = append(found, docs[0])
			}
		}

		out[i] = value(found, s.Many || array)
	}

	return out, nil
}

// relatePerRow reads, for each row, the documents whose foreign field is the
// row's local field, with the query, sort and limit of the relation.
func (db *DB) relatePerRow(ctx context.Context, rows []json.RawMessage, s lookup.Spec, budget *int) ([]json.RawMessage, error) {
	found := make([][]json.RawMessage, len(rows))
	errs := make([]error, len(rows))
	added := addedFields(s)

	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		slot = make(chan struct{}, lookupWorkers)
	)

	for i, row := range rows {
		keys := lookup.Keys(row, s.Local)
		if len(keys) == 0 {
			continue
		}

		wg.Add(1)
		slot <- struct{}{}

		go func() {
			defer wg.Done()
			defer func() { <-slot }()

			filter := keyFilter(s, keys)

			docs, _, err := db.lookupFind(ctx, s, filter, s.Limit, "")
			if err == nil {
				mu.Lock()
				err = spend(budget, len(docs))
				mu.Unlock()
			}

			if err != nil {
				errs[i] = err

				return
			}

			found[i] = docs
		}()
	}

	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	if len(s.Nested) > 0 {
		if err := db.attachNested(ctx, found, s.Nested, budget); err != nil {
			return nil, err
		}
	}

	out := make([]json.RawMessage, len(rows))

	for i := range rows {
		for j := range found[i] {
			found[i][j] = lookup.Without(found[i][j], added)
		}

		out[i] = value(found[i], s.Many)
	}

	return out, nil
}

// keyFilter is the filter of one row: the foreign field equals the key (or
// one of the keys), and the relation query.
func keyFilter(s lookup.Spec, keys []lookup.KeyVal) map[string]any {
	var match any = keys[0].Raw

	if len(keys) > 1 {
		raws := make([]json.RawMessage, len(keys))

		for i, kv := range keys {
			raws[i] = kv.Raw
		}

		match = map[string]any{"$in": raws}
	}

	key := map[string]any{s.Foreign: match}

	if len(s.Query) == 0 {
		return key
	}

	return map[string]any{"$and": []any{key, s.Query}}
}

// attachNested runs the nested lookups once on every document found, then
// puts them back in their rows.
func (db *DB) attachNested(ctx context.Context, found [][]json.RawMessage, specs []lookup.Spec, budget *int) error {
	var flat []json.RawMessage

	for _, docs := range found {
		flat = append(flat, docs...)
	}

	flat, err := db.attachLookups(ctx, flat, specs, budget)
	if err != nil {
		return err
	}

	n := 0

	for i := range found {
		for j := range found[i] {
			found[i][j] = flat[n]
			n++
		}
	}

	return nil
}

// lookupFind runs a search on the other collection and gives the documents
// with the fields the lookup needs: keep (the foreign key of a batch) and
// the local fields of the nested lookups, even when the fields of the
// relation leave them out. added lists those, to remove when done.
func (db *DB) lookupFind(ctx context.Context, s lookup.Spec, filter map[string]any, limit int, keep string) ([]json.RawMessage, []string, error) {
	fields, added := lookup.Need(s.Fields, needed(s, keep)...)

	body := map[string]any{"query": filter, "limit": limit}

	if len(fields) > 0 {
		body["fields"] = fields
	}

	if len(s.Sort) > 0 {
		body["sort"] = s.Sort
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, nil, err
	}

	rows, err := db.findAny(ctx, s.From, raw)

	return rows, added, err
}

// needed are the fields of the other collection that the lookup reads.
func needed(s lookup.Spec, keep string) []string {
	need := lookup.Locals(s.Nested)

	if keep != "" {
		need = append(need, keep)
	}

	return need
}

// addedFields are the fields that a per-row relation reads but that its
// fields leave out.
func addedFields(s lookup.Spec) []string {
	_, added := lookup.Need(s.Fields, needed(s, "")...)

	return added
}

// findAny searches a collection by name, plain or partitioned.
func (db *DB) findAny(ctx context.Context, name string, body []byte) ([]json.RawMessage, error) {
	if def, ok := db.Partitioned(name); ok {
		page, err := db.FindPartitioned(ctx, def, body)

		return page.Rows, err
	}

	col, err := db.Get(name)
	if err != nil {
		return nil, err
	}

	page, err := col.Find(ctx, body)

	return page.Rows, err
}

// spend takes n documents from the budget of the answer.
func spend(budget *int, n int) error {
	*budget -= n

	if *budget < 0 {
		return lookup.NewError("lookup_too_large", "a lookup attaches more than %d documents; add a limit, a query or fewer rows", lookupBudget)
	}

	return nil
}

// value is the JSON a row gets: an array, or the first document or null.
func value(docs []json.RawMessage, many bool) json.RawMessage {
	if many {
		if len(docs) == 0 {
			return json.RawMessage(`[]`)
		}

		b, _ := json.Marshal(docs)

		return b
	}

	if len(docs) == 0 {
		return json.RawMessage(`null`)
	}

	return docs[0]
}
