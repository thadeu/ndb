package collection

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/wal"
)

// Atomic update and delete by filter.
//
// The filter is matched and the write is made inside the writer goroutine,
// against head (the newest state, with commits that are not durable yet), so
// two updates never interleave: a guarded decrement or an $inc is safe
// without a retry. The WAL gets the resulting document of each match (a put
// with the next version), never the operators, so replay is a plain apply.
//
// Candidates. A filter with the id field uses the id lookup in head. Without
// it, a ready secondary index gives the candidate ids; the index follows the
// durable state, so a document that became a match in a commit that is not
// durable yet can be missed by many. Without an index, one update scans head
// until the first match; many is unindexed_many.

// UpdateOpts are the query-string options of an update.
type UpdateOpts struct {
	Many   bool
	Upsert bool

	// ExpireIn, when positive, sets the expires_field to now + ExpireIn on
	// every written document. It wins over the field in the operators.
	ExpireIn time.Duration
}

// UpdateResult is the answer of Update. Doc is the new document with its
// "_v", and is set for a single update that matched or upserted.
type UpdateResult struct {
	Matched  int
	Modified int
	Doc      json.RawMessage
}

// errNoWrite ends a request that has nothing to write. It is not an error
// for the caller: the request leaves no WAL record.
var errNoWrite = errors.New("nothing to write")

type updateReq struct {
	filterMap map[string]any
	filter    *query.Query
	ops       []fieldOp
	opts      UpdateOpts
	at        time.Time
	out       *UpdateResult

	// cur is the candidate list of a many update, across its records.
	cur *manyCursor
}

type deleteReq struct {
	filter *query.Query
	many   bool
	cur    *manyCursor
}

// Update applies the write operators of body to the documents that its
// filter matches. See splitBody for how the body is read.
func (c *Collection) Update(ctx context.Context, body []byte, opts UpdateOpts) (UpdateResult, error) {
	top, err := decodeBody(body)
	if err != nil {
		return UpdateResult{}, err
	}

	return c.update(ctx, top, opts)
}

// UpdateID is Update on one document: the filter is the _id plus the guard
// keys of the body. The body must not name _id, the key comes from the path.
func (c *Collection) UpdateID(ctx context.Context, id string, body []byte, opts UpdateOpts) (UpdateResult, error) {
	top, err := decodeBody(body)
	if err != nil {
		return UpdateResult{}, err
	}

	if _, ok := top[idField]; ok {
		return UpdateResult{}, errf("invalid_request", "%s comes from the path, not from the body", idField)
	}

	top[idField] = id
	opts.Many = false

	return c.update(ctx, top, opts)
}

func (c *Collection) update(ctx context.Context, top map[string]any, opts UpdateOpts) (UpdateResult, error) {
	if err := c.waitRoom(ctx); err != nil {
		return UpdateResult{}, err
	}

	filterMap, ops, err := splitBody(top)
	if err != nil {
		return UpdateResult{}, err
	}

	filter, err := c.parseFilter(filterMap)
	if err != nil {
		return UpdateResult{}, err
	}

	req := updateReq{filterMap: filterMap, filter: filter, ops: ops, opts: opts, out: &UpdateResult{}}

	if opts.ExpireIn > 0 {
		if _, err := c.expiryField(); err != nil {
			return UpdateResult{}, err
		}

		req.at = c.clock().Add(opts.ExpireIn)
	}

	if opts.Many {
		req.cur = &manyCursor{}

		if err := c.list(req.cur, filter); err != nil {
			return UpdateResult{}, err
		}
	}

	for {
		_, _, err = c.commit(ctx, req)

		if errors.Is(err, errNoWrite) {
			break
		}

		if err != nil {
			return UpdateResult{}, manyLocked(err, opts.Many)
		}

		if !opts.Many || req.cur.done() {
			break
		}
	}

	if opts.Many {
		req.out.Doc = nil
	}

	return *req.out, nil
}

// Delete removes the documents that the filter matches, and returns how many.
func (c *Collection) Delete(ctx context.Context, body []byte, many bool) (int, error) {
	top, err := decodeBody(body)
	if err != nil {
		return 0, err
	}

	return c.deleteTop(ctx, top, many)
}

func (c *Collection) deleteTop(ctx context.Context, top map[string]any, many bool) (int, error) {
	filterMap, err := splitFilter(top)
	if err != nil {
		return 0, err
	}

	filter, err := c.parseFilter(filterMap)
	if err != nil {
		return 0, err
	}

	req := deleteReq{filter: filter, many: many, cur: &manyCursor{}}

	if many {
		if err := c.list(req.cur, filter); err != nil {
			return 0, err
		}
	}

	deleted := 0

	for {
		_, ops, err := c.commit(ctx, req)

		if errors.Is(err, errNoWrite) {
			return deleted, nil
		}

		if err != nil {
			return deleted, manyLocked(err, many)
		}

		deleted += len(ops)

		if !many || req.cur.done() {
			return deleted, nil
		}
	}
}

// DeleteID removes the document with this _id, and returns how many (0 or 1).
func (c *Collection) DeleteID(ctx context.Context, id string) (int, error) {
	return c.deleteTop(ctx, map[string]any{idField: id}, false)
}

func decodeBody(body []byte) (map[string]any, error) {
	m, err := decodeObject(body)
	if err != nil {
		return nil, errf("invalid_request", "the body must be a JSON object")
	}

	return m, nil
}

func (c *Collection) parseFilter(m map[string]any) (*query.Query, error) {
	if len(m) == 0 {
		return nil, errf("empty_filter", "the filter is empty")
	}

	return query.ParseWhere(m, c.queryOptions())
}

// prepareUpdate runs in the writer goroutine.
func (c *Collection) prepareUpdate(v *view, r updateReq) ([]wal.Op, error) {
	vq, err := c.visibility(c.clock())
	if err != nil {
		return nil, err
	}

	var ops []wal.Op

	if r.opts.Many {
		ops, err = c.updateMany(v, r, vq)
	} else {
		ops, err = c.updateOne(v, r, vq)
	}

	if err != nil {
		return nil, err
	}

	last := !r.opts.Many || r.cur.done()

	if r.out.Matched == 0 && r.opts.Upsert && last {
		return c.prepareUpsert(v, r)
	}

	if len(ops) == 0 {
		return nil, errNoWrite
	}

	return ops, nil
}

// updateOne writes the first match: the id, an index, or a scan of head.
func (c *Collection) updateOne(v *view, r updateReq, vq *query.Query) ([]wal.Op, error) {
	ids, err := c.candidates(v, r.filter)
	if err != nil {
		return nil, err
	}

	for id := range ids {
		op, matched, err := c.updateMatch(v, id, r, vq)
		if err != nil {
			return nil, err
		}

		if !matched {
			continue
		}

		if op == nil {
			return nil, nil
		}

		return []wal.Op{*op}, nil
	}

	return nil, nil
}

// updateMany writes the next candidates of the list, up to one record.
func (c *Collection) updateMany(v *view, r updateReq, vq *query.Query) ([]wal.Op, error) {
	var ops []wal.Op

	size := 0

	for ; !r.cur.done() && !c.full(ops, size); r.cur.pos++ {
		op, _, err := c.updateMatch(v, r.cur.cands[r.cur.pos].id, r, vq)
		if err != nil {
			return nil, err
		}

		if op != nil {
			ops = append(ops, *op)
			size += opCost(*op)
		}
	}

	return ops, nil
}

// updateMatch tests one candidate and writes it when it matches. The op is
// nil when it does not match, or when the operators change nothing.
func (c *Collection) updateMatch(v *view, id string, r updateReq, vq *query.Query) (*wal.Op, bool, error) {
	cur, ok := v.get(id)
	if !ok {
		return nil, false, nil
	}

	m, raw, ok, err := matchEntry(cur, r.filter, vq)
	if err != nil || !ok {
		return nil, false, err
	}

	r.out.Matched++

	op, err := c.writeMatch(v, id, cur, m, raw, r)

	return op, true, err
}

// writeMatch applies the operators to one matched document. It returns nil
// when nothing changed.
func (c *Collection) writeMatch(v *view, id string, cur *entry, m map[string]any, raw json.RawMessage, r updateReq) (*wal.Op, error) {
	changed, err := applyOps(m, r.ops)
	if err != nil {
		return nil, err
	}

	if !r.at.IsZero() {
		if err := setPath(m, c.def.ExpiresField, formatExpiry(r.at)); err != nil {
			return nil, err
		}

		changed = true
	}

	if !changed {
		r.out.Doc = withVersion(raw, cur.v)

		return nil, nil
	}

	// A document stored before _v was refused can hold one: drop it.
	delete(m, versionField)

	doc, err := c.finishDoc(v, id, m)

	if err != nil {
		return nil, err
	}

	op := wal.Op{Kind: wal.Put, ID: id, V: cur.v + 1, Doc: doc}

	v.apply(op)

	r.out.Modified++
	r.out.Doc = withVersion(doc, op.V)

	return &op, nil
}

// finishDoc encodes the new document, moves big strings to blobs and checks
// the unique fields.
func (c *Collection) finishDoc(v *view, id string, m map[string]any) (json.RawMessage, error) {
	doc, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}

	doc, err = c.externalize(doc)
	if err != nil {
		return nil, err
	}

	return doc, v.checkUnique(id, doc)
}

// insertOps turns $setOnInsert into $set: the document is being created.
func insertOps(ops []fieldOp) []fieldOp {
	out := make([]fieldOp, len(ops))

	for i, o := range ops {
		if o.op == "$setOnInsert" {
			o.op = "$set"
		}

		out[i] = o
	}

	return out
}

// prepareUpsert creates the document from the equality fields of the
// filter, then applies the operators to it.
func (c *Collection) prepareUpsert(v *view, r updateReq) ([]wal.Op, error) {
	base := map[string]any{}

	for k, val := range r.filterMap {
		if err := upsertField(base, k, val); err != nil {
			return nil, err
		}
	}

	if _, err := applyOps(base, insertOps(r.ops)); err != nil {
		return nil, err
	}

	if !r.at.IsZero() {
		if err := setPath(base, c.def.ExpiresField, formatExpiry(r.at)); err != nil {
			return nil, err
		}
	}

	raw, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}

	id, doc, err := c.withID(raw)
	if err != nil {
		return nil, err
	}

	prior, err := c.priorVersion(v, id)
	if err != nil {
		return nil, err
	}

	doc, err = c.externalize(doc)
	if err != nil {
		return nil, err
	}

	if err := v.checkUnique(id, doc); err != nil {
		return nil, err
	}

	op := wal.Op{Kind: wal.Put, ID: id, V: prior + 1, Doc: doc}

	v.apply(op)

	r.out.Modified = 1
	r.out.Doc = withVersion(doc, op.V)

	return []wal.Op{op}, nil
}

// upsertField copies one plain equality of the filter into the new document.
// Operators, boolean filters and the version are skipped.
func upsertField(base map[string]any, key string, val any) error {
	if strings.HasPrefix(key, "$") || key == "_v" {
		return nil
	}

	if m, ok := val.(map[string]any); ok && !isPlainObject(m) {
		return nil
	}

	return setPath(base, key, val)
}

func isPlainObject(m map[string]any) bool {
	for k := range m {
		if strings.HasPrefix(k, "$") {
			return false
		}
	}

	return true
}

// prepareDelete runs in the writer goroutine.
func (c *Collection) prepareDelete(v *view, r deleteReq) ([]wal.Op, error) {
	vq, err := c.visibility(c.clock())
	if err != nil {
		return nil, err
	}

	var ops []wal.Op

	if r.many {
		ops, err = c.deleteMany(v, r, vq)
	} else {
		ops, err = c.deleteOne(v, r, vq)
	}

	if err != nil {
		return nil, err
	}

	if len(ops) == 0 {
		return nil, errNoWrite
	}

	return ops, nil
}

// deleteOne deletes the first match: the id, an index, or a scan of head.
func (c *Collection) deleteOne(v *view, r deleteReq, vq *query.Query) ([]wal.Op, error) {
	ids, err := c.candidates(v, r.filter)
	if err != nil {
		return nil, err
	}

	for id := range ids {
		cur, ok := v.get(id)
		if !ok {
			continue
		}

		_, _, ok, err := matchEntry(cur, r.filter, vq)
		if err != nil {
			return nil, err
		}

		if ok {
			return []wal.Op{deleteOp(v, id, cur)}, nil
		}
	}

	return nil, nil
}

// deleteMany deletes the next candidates of the list, up to one record. A
// candidate of an exact list that did not change is deleted without being
// read, which matters most for sealed documents.
func (c *Collection) deleteMany(v *view, r deleteReq, vq *query.Query) ([]wal.Op, error) {
	var ops []wal.Op

	size := 0

	for ; !r.cur.done() && !c.full(ops, size); r.cur.pos++ {
		cd := r.cur.cands[r.cur.pos]

		cur, ok := v.get(cd.id)
		if !ok {
			continue
		}

		if !c.skipRead(r.cur, cd, vq) {
			_, _, ok, err := matchEntry(cur, r.filter, vq)
			if err != nil {
				return nil, err
			}

			if !ok {
				continue
			}
		}

		op := deleteOp(v, cd.id, cur)

		ops = append(ops, op)
		size += opCost(op)
	}

	return ops, nil
}

func deleteOp(v *view, id string, cur *entry) wal.Op {
	op := wal.Op{Kind: wal.Del, ID: id, V: cur.v + 1}

	v.apply(op)

	return op
}

// matchEntry decodes the entry and tests the filter on it, with the version
// as the field "_v". An expired document (vq does not match) never matches.
func matchEntry(e *entry, filter, vq *query.Query) (map[string]any, json.RawMessage, bool, error) {
	raw, err := e.raw()
	if err != nil {
		return nil, nil, false, err
	}

	m, err := query.Decode(raw)
	if err != nil {
		return nil, nil, false, err
	}

	if vq != nil && !vq.Match(m) {
		return nil, nil, false, nil
	}

	probe := make(map[string]any, len(m)+1)

	for k, val := range m {
		probe[k] = val
	}

	probe["_v"] = json.Number(strconv.FormatUint(e.v, 10))

	return m, raw, filter.Match(probe), nil
}

// candidates is the ids to test, in the order of the plan. See the notes at
// the top of this file.
func (c *Collection) candidates(v *view, q *query.Query) (iter.Seq[string], error) {
	conds := q.Indexable()

	if ids, ok := c.idValues(conds); ok {
		return slices.Values(ids), nil
	}

	c.mu.RLock()
	_, cands, ok := c.indexedCands(conds)
	c.mu.RUnlock()

	if ok {
		ids := make([]string, len(cands))

		for i, cd := range cands {
			ids[i] = cd.id
		}

		return slices.Values(ids), nil
	}

	c.mu.RLock()

	src := c.scanSource()
	src.held = c.holdSegments()

	c.mu.RUnlock()

	return func(yield func(string) bool) {
		defer src.release()

		for id := range v.head.docs {
			if !yield(id) {
				return
			}
		}

		err := eachLive(src.segs, func(id string, _ *entry) error {
			if _, mem := v.head.docs[id]; mem {
				return nil
			}

			if !yield(id) {
				return errStopped
			}

			return nil
		})
		if err != nil && !errors.Is(err, errStopped) {
			c.fail(err)
		}
	}, nil
}

// idValues are the ids named by an eq or in on the id field.
func (c *Collection) idValues(conds []query.Cond) ([]string, bool) {
	for _, cd := range conds {
		if cd.Path != idField || !isPoint(cd.Op) {
			continue
		}

		if slices.ContainsFunc(cd.Vals, func(val query.Value) bool { return val.Kind != query.String }) {
			continue
		}

		var out []string

		for _, val := range cd.Vals {
			if !slices.Contains(out, val.Str) {
				out = append(out, val.Str)
			}
		}

		return out, true
	}

	return nil, false
}
