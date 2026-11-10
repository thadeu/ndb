package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/query"
)

// A unique field that a later PUT adds to an engine collection is enforced
// at once. First the data must hold no duplicate value of the new field:
// else the PUT is 409 unique_violation and the definition does not change.
// Then the collection closes and opens again with the new definition, as at
// a restart, so the next write checks the new field. Its open watch streams
// end, and their consumers connect again from their stored position.

// uniquePage is how many documents one read of the duplicate check takes.
const uniquePage = 1000

// checkDecl refuses the keys that only ndb sets.
func checkDecl(decl catalog.Def) error {
	if decl.PartitionOf != "" {
		return query.NewError("invalid_request", "partition_of is set by ndb on each partition: it is not a key of a declaration")
	}

	return nil
}

// newUnique is the unique fields of decl that cur does not have.
func newUnique(cur, decl catalog.Def) []string {
	var out []string

	for _, f := range decl.Unique {
		if f != "" && !slices.Contains(cur.Unique, f) && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}

	return out
}

// ensureUnique is ensure for a declaration that adds unique fields to the
// open collection col.
func (db *DB) ensureUnique(col *Collection, decl catalog.Def) (catalog.Def, error) {
	for {
		// A sleeping collection wakes first: the check reads its data.
		if err := col.enter(); err != nil {
			return catalog.Def{}, err
		}

		col.leave()

		def, done, err := db.ensureUniqueOn(col, decl)

		if done {
			return def, err
		}
	}
}

// ensureUniqueOn checks the data and opens col again with the new
// definition, while no call runs on col. It reports false when col went to
// sleep meanwhile: the caller wakes it and tries again.
func (db *DB) ensureUniqueOn(col *Collection, decl catalog.Def) (catalog.Def, bool, error) {
	db.swap.Lock()
	defer db.swap.Unlock()

	col.live.Lock()
	defer col.live.Unlock()

	switch {
	case col.closed:
		return catalog.Def{}, true, ErrClosed
	case col.dropped:
		return catalog.Def{}, true, fmt.Errorf("%w: %s", catalog.ErrUnknown, col.name)
	case col.asleep:
		return catalog.Def{}, false, nil
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return catalog.Def{}, true, err
	}

	cur, _ := db.cat.Get(decl.Name)

	if err := uniqueData(db.ctx, col.rd, newUnique(cur, decl)); err != nil {
		return catalog.Def{}, true, err
	}

	def, err := db.cat.Ensure(decl, catalog.FromCode)

	if err != nil {
		return catalog.Def{}, true, err
	}

	if err := col.eng.Close(); err != nil {
		return def, true, err
	}

	fresh, err := db.openEngine(def, col.dir, col.wm)

	if err != nil {
		// The collection stays closed, as if it slept: the next call opens
		// it again.
		col.asleep, col.rest = true, resting{}

		return def, true, fmt.Errorf("open %s again: %w", def.Name, err)
	}

	col.eng, col.rd = fresh.eng, fresh.rd

	return def, true, nil
}

// checkUniqueData runs the duplicate check of fields on the open
// collection name, while calls go on. ensureUniqueOn checks again under the
// lock.
func (db *DB) checkUniqueData(name string, fields []string) error {
	col, err := db.Get(name)

	if err != nil || !col.engine() {
		return nil
	}

	if err := col.enter(); err != nil {
		return err
	}

	defer col.leave()

	return uniqueData(db.ctx, col.rd, fields)
}

// uniqueData fails with unique_violation when two visible documents hold
// the same value of one of fields. It reads every document, one page at a
// time, and keeps one id per value.
func uniqueData(ctx context.Context, rd reader, fields []string) error {
	for _, f := range fields {
		if err := uniqueField(ctx, rd, f); err != nil {
			return err
		}
	}

	return nil
}

func uniqueField(ctx context.Context, rd reader, field string) error {
	owners := map[string]string{}
	after := ""

	for {
		req := map[string]any{"limit": uniquePage}

		if after != "" {
			req["after"] = after
		}

		body, _ := json.Marshal(req)

		q, err := rd.ParseQuery(body)

		if err != nil {
			return err
		}

		page, err := rd.Query(ctx, q)

		if err != nil {
			return err
		}

		for _, row := range page.Rows {
			key, ok := collection.UniqueKey(row, field)

			if !ok {
				continue
			}

			id := docID(row)

			if other, taken := owners[key]; taken && other != id {
				return query.NewError("unique_violation", "%s=%s is held by %s and %s: make the values unique, then add %s to unique", field, key, other, id, field)
			}

			owners[key] = id
		}

		if page.Next == "" {
			return nil
		}

		after = page.Next
	}
}

// docID is the _id of a document.
func docID(doc json.RawMessage) string {
	var head struct {
		ID string `json:"_id"`
	}

	_ = json.Unmarshal(doc, &head)

	return head.ID
}
