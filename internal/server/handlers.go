package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
	ndbmetrics "github.com/thadeu/ndb/internal/metrics"
)

type writeResult struct {
	ID string `json:"_id"`
	V  uint64 `json:"_v"`
}

type indexBody struct {
	Field string `json:"field"`
	State string `json:"state"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

// bulkReq is the body of POST /{coll}/_bulk. With upsert, a document whose
// _id exists is merged into the stored one instead of being duplicate_id.
type bulkReq struct {
	Docs     []json.RawMessage `json:"docs"`
	ExpireIn json.RawMessage   `json:"expire_in"`
	Upsert   bool              `json:"upsert"`
}

// updateReq is the body of _update/{id} and _update_by_query. query is the
// filter (or, for one id, extra guards such as "_v"); update holds the write
// operators.
type updateReq struct {
	Query    map[string]any  `json:"query"`
	Update   map[string]any  `json:"update"`
	Many     bool            `json:"many"`
	Upsert   bool            `json:"upsert"`
	ExpireIn json.RawMessage `json:"expire_in"`
}

// deleteReq is the body of _delete_by_query.
type deleteReq struct {
	Query map[string]any `json:"query"`
	Many  bool           `json:"many"`
}

// countReq is the body of _count.
type countReq struct {
	Query json.RawMessage `json:"query"`
}

// find is POST /{coll}/_search. The body holds the rows (see writePage);
// the page data is in X-Ndb-* headers.
func (s *Server) find(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	run := col.Find

	if isStream(db, r.PathValue("coll")) {
		run = streamFind(run)
	}

	s.read(w, r, db, &s.metrics.find, s.withLookup(r, db, run))
}

// findPattern is _search over every collection of a pattern (logs-2026-*).
func (s *Server) findPattern(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	if err := noStreamIn(db, r.PathValue("coll")); err != nil {
		writeError(w, err)

		return
	}

	s.read(w, r, db, &s.metrics.find, s.withLookup(r, db, func(ctx context.Context, body []byte) (collection.Page, error) {
		return db.FindPattern(ctx, r.PathValue("coll"), body)
	}))
}

// aggregate is POST /{coll}/_aggregate: the output rows of a pipeline, with
// the same answer and headers as _search.
func (s *Server) aggregate(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	run := col.Aggregate

	if isStream(db, r.PathValue("coll")) {
		run = streamAggregate(run)
	}

	s.read(w, r, db, &s.metrics.aggregate, s.withLookupStages(r, db, run))
}

// aggregatePattern is aggregate over every collection of a pattern.
func (s *Server) aggregatePattern(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	if err := noStreamIn(db, r.PathValue("coll")); err != nil {
		writeError(w, err)

		return
	}

	s.read(w, r, db, &s.metrics.aggregate, s.withLookupStages(r, db, func(ctx context.Context, body []byte) (collection.Page, error) {
		return db.AggregatePattern(ctx, r.PathValue("coll"), body)
	}))
}

// read runs a find or an aggregate body and writes the page.
func (s *Server) read(w http.ResponseWriter, r *http.Request, db *engine.DB, took *ndbmetrics.Histogram, run func(context.Context, []byte) (collection.Page, error)) {
	body, err := readBody(w, r, db.MaxLine(r.PathValue("coll")))
	if err != nil {
		writeError(w, err)

		return
	}

	began := time.Now()

	page, err := run(r.Context(), body)

	elapsed := time.Since(began)
	took.Observe(elapsed)

	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.rows.Add(int64(len(page.Rows)))

	w.Header().Set("X-Ndb-Took-Ms", strconv.FormatInt(elapsed.Milliseconds(), 10))

	writePage(w, r, page)
}

// countDocs is POST /{coll}/_count: {"count": n} for {"query": {...}}.
func (s *Server) countDocs(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	run := col.Find

	if isStream(db, r.PathValue("coll")) {
		run = streamFind(run)
	}

	s.runCount(w, r, db, run)
}

// countPattern is _count over every collection of a pattern.
func (s *Server) countPattern(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	if err := noStreamIn(db, r.PathValue("coll")); err != nil {
		writeError(w, err)

		return
	}

	s.runCount(w, r, db, func(ctx context.Context, body []byte) (collection.Page, error) {
		return db.FindPattern(ctx, r.PathValue("coll"), body)
	})
}

// runCount runs a find with count: true and limit 1, and answers its total.
func (s *Server) runCount(w http.ResponseWriter, r *http.Request, db *engine.DB, run func(context.Context, []byte) (collection.Page, error)) {
	var req countReq

	body, err := readBody(w, r, db.MaxLine(r.PathValue("coll")))
	if err == nil {
		err = decodeOptional(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	find := map[string]any{"count": true, "limit": 1}

	if req.Query != nil {
		find["query"] = req.Query
	}

	raw, err := json.Marshal(find)
	if err != nil {
		writeError(w, err)

		return
	}

	page, err := run(r.Context(), raw)
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]int{"count": *page.Total})
}

// insertOne is POST /{coll}/_doc: the body is the document. An unknown
// collection is created (unless allow_implicit is false).
func (s *Server) insertOne(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if trimmed := bytes.TrimSpace(body); len(trimmed) == 0 || trimmed[0] != '{' {
		writeError(w, badRequest("the body must be a JSON object: the document"))

		return
	}

	res, ok := s.insertDocs(w, r, db, name, [][]byte{body}, 0, false)
	if ok {
		writeJSON(w, http.StatusOK, res[0])
	}
}

// bulk is POST /{coll}/_bulk: {"docs": [...], "expire_in", "upsert"}, all
// or nothing.
func (s *Server) bulk(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req bulkReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err == nil {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	if len(req.Docs) == 0 {
		writeError(w, badRequest("docs must be a non-empty array"))

		return
	}

	expireIn, err := parseExpireIn(req.ExpireIn)
	if err != nil {
		writeError(w, err)

		return
	}

	docs := make([][]byte, len(req.Docs))

	for i, d := range req.Docs {
		docs[i] = d
	}

	res, ok := s.insertDocs(w, r, db, name, docs, expireIn, req.Upsert)
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{"results": res})
	}
}

// insertDocs writes docs in one transaction. On an error it answers it and
// returns false.
func (s *Server) insertDocs(w http.ResponseWriter, r *http.Request, db *engine.DB, name string, docs [][]byte, expireIn time.Duration, upsert bool) ([]writeResult, bool) {
	col, err := db.GetOrCreate(name)
	if err != nil {
		writeError(w, err)

		return nil, false
	}

	var res []collection.Result

	switch {
	case upsert:
		res, err = col.Upsert(r.Context(), expireIn, docs...)
	case expireIn > 0:
		res, err = col.InsertIn(r.Context(), expireIn, docs...)
	default:
		res, err = col.Insert(r.Context(), docs...)
	}

	if err != nil {
		writeError(w, err)

		return nil, false
	}

	s.metrics.writes.Add(int64(len(res)))

	out := make([]writeResult, len(res))

	for i, x := range res {
		out[i] = writeResult{ID: x.ID, V: x.V}
	}

	return out, true
}

// update is POST /{coll}/_update_by_query: {"query", "update", "many",
// "upsert", "expire_in"}. The match and the write are one atomic step.
func (s *Server) update(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	s.runUpdate(w, r, db, "")
}

// updateID is POST /{coll}/_update/{id}: the same update on the document
// with this _id. The keys of query are extra guards (the _v of optimistic
// concurrency).
func (s *Server) updateID(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	s.runUpdate(w, r, db, r.PathValue("id"))
}

func (s *Server) runUpdate(w http.ResponseWriter, r *http.Request, db *engine.DB, id string) {
	name := r.PathValue("coll")

	var req updateReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err == nil {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	expireIn, err := parseExpireIn(req.ExpireIn)
	if err != nil {
		writeError(w, err)

		return
	}

	if id != "" && req.Many {
		writeError(w, badRequest("many does not apply to one _id"))

		return
	}

	joined, err := collection.JoinUpdate(req.Query, req.Update)
	if err != nil {
		writeError(w, err)

		return
	}

	raw, err := json.Marshal(joined)
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	opts := collection.UpdateOpts{Many: req.Many, Upsert: req.Upsert, ExpireIn: expireIn}

	var res collection.UpdateResult

	if id == "" {
		res, err = col.Update(r.Context(), raw, opts)
	} else {
		res, err = col.UpdateID(r.Context(), id, raw, opts)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(res.Modified))

	out := map[string]any{"matched": res.Matched, "modified": res.Modified}

	if res.Doc != nil && !opts.Many {
		out["doc"] = res.Doc
	}

	writeJSON(w, http.StatusOK, out)
}

// get is GET /{coll}/_doc/{id}: the document with its "_v".
func (s *Server) get(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	id := r.PathValue("id")
	stream := isStream(db, r.PathValue("coll"))

	if stream {
		id = streamDocID(id)
	}

	doc, ok := col.Get(id)
	if !ok {
		writeError(w, &apiError{status: http.StatusNotFound, code: "not_found", msg: "no such document"})

		return
	}

	raw := doc.Doc

	if stream {
		raw = unpadRows([]json.RawMessage{raw})[0]
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(append(raw, '\n'))
}

// removeID is DELETE /{coll}/_doc/{id}.
func (s *Server) removeID(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	id := r.PathValue("id")
	stream := isStream(db, r.PathValue("coll"))

	if stream {
		id = streamDocID(id)
	}

	n, err := col.DeleteID(r.Context(), id)
	if err != nil {
		writeError(w, err)

		return
	}

	if stream && n > 0 {
		s.streams.Deleted(r.Context(), r.PathValue("coll"), id)
	}

	s.metrics.writes.Add(int64(n))

	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// remove is POST /{coll}/_delete_by_query: {"query", "many"}.
func (s *Server) remove(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req deleteReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err == nil {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	raw, err := json.Marshal(req.Query)
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	n, err := col.Delete(r.Context(), raw, req.Many)
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(n))

	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// ensure is ensure_collection (PUT /{coll}). The answer has the final
// definition and the state of each index.
func (s *Server) ensure(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var def catalog.Def

	body, err := readBody(w, r, engine.DefaultMaxLine)
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &def); err != nil {
		writeError(w, err)

		return
	}

	def.Name = name
	def.OpsFromYAML = false

	final, err := db.Ensure(def)
	if err != nil {
		writeError(w, err)

		return
	}

	if final.PartitionBy != "" {
		s.writePartitioned(w, db, final)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"definition": final,
		"indexes":    indexBodies(col.Stats()),
	})
}

func (s *Server) list(w http.ResponseWriter, _ *http.Request, db *engine.DB) {
	writeJSON(w, http.StatusOK, map[string]any{"collections": db.Definitions()})
}

// listPattern is GET /{pattern}: the names a pattern matches.
func (s *Server) listPattern(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	names, err := db.Match(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"collections": append([]string{}, names...)})
}

// ensureIndex is PUT /{coll}/_indexes/{field}: it adds a secondary index to
// an existing collection and builds it in the background.
func (s *Server) ensureIndex(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	if _, ok := db.Definition(name); !ok {
		writeError(w, fmt.Errorf("%w: %s", catalog.ErrUnknown, name))

		return
	}

	def, err := db.Ensure(catalog.Def{Name: name, Indexes: []string{r.PathValue("field")}})
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"collection": name, "indexes": def.Indexes})
}

func (s *Server) dropIndex(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	def, err := db.DropIndex(name, r.PathValue("field"))
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"collection": name, "indexes": def.Indexes})
}

func indexBodies(st engine.Stats) []indexBody {
	out := make([]indexBody, len(st.Indexes))

	for i, ix := range st.Indexes {
		out[i] = indexBody{Field: ix.Field, State: ix.State, Done: ix.Done, Total: ix.Total}
	}

	return out
}

// writePage answers the rows of a page: a JSON array, or NDJSON when the
// client asks for application/x-ndjson. The rows are the same in both; the
// page data is in headers, sent before the body.
func writePage(w http.ResponseWriter, r *http.Request, page collection.Page) {
	h := w.Header()

	h.Set("X-Ndb-Plan", page.Plan)
	h.Set("X-Ndb-Examined", strconv.Itoa(page.Examined))

	if page.Next != "" {
		h.Set("X-Ndb-Next", page.Next)
	}

	if page.Total != nil {
		h.Set("X-Ndb-Total", strconv.Itoa(*page.Total))
	}

	if page.Explain != nil {
		if b, err := json.Marshal(page.Explain); err == nil {
			h.Set("X-Ndb-Explain", string(b))
		}
	}

	if wantsNDJSON(r) {
		h.Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)

		for _, row := range page.Rows {
			if _, err := w.Write(append(row, '\n')); err != nil {
				return
			}
		}

		return
	}

	h.Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(jsonArray(page.Rows))
}

// wantsNDJSON tells if the Accept header asks for NDJSON.
func wantsNDJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/x-ndjson")
}

// jsonArray joins rows into one JSON array.
func jsonArray(rows []json.RawMessage) []byte {
	n := 2

	for _, row := range rows {
		n += len(row) + 1
	}

	out := make([]byte, 0, n)
	out = append(out, '[')

	for i, row := range rows {
		if i > 0 {
			out = append(out, ',')
		}

		out = append(out, row...)
	}

	return append(out, ']', '\n')
}
