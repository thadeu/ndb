package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/diskusage"
	"github.com/thadeu/ndb/internal/engine"
)

// Routes of a partitioned collection (partition_by). The logical name has no
// data: reads go over its live partitions, writes go to the partition of the
// time field, and a route by id goes to the partition that holds the id.
// Most routes reuse the handler of one collection: they set the path value
// coll to the partition name and call it.

type partitionHandler func(http.ResponseWriter, *http.Request, *engine.DB, catalog.Def)

func (s *Server) partitionRoutes() map[string]partitionHandler {
	return map[string]partitionHandler{
		"PUT ":                  s.partEnsure,
		"GET ":                  s.partGet,
		"DELETE ":               s.partDrop,
		"POST _truncate":        s.partTruncate,
		"POST _rename":          func(w http.ResponseWriter, r *http.Request, db *engine.DB, _ catalog.Def) { s.rename(w, r, db) },
		"POST _restore":         func(w http.ResponseWriter, r *http.Request, db *engine.DB, _ catalog.Def) { s.restore(w, r, db) },
		"GET _pitr":             func(w http.ResponseWriter, r *http.Request, db *engine.DB, _ catalog.Def) { s.pitr(w, r, db) },
		"POST _search":          s.partSearch,
		"POST _count":           s.partCount,
		"POST _aggregate":       s.partAggregate,
		"POST _doc":             s.partInsertOne,
		"POST _bulk":            s.partBulk,
		"GET _doc/{}":           s.byID(s.get, false),
		"DELETE _doc/{}":        s.byID(s.removeID, false),
		"POST _update/{}":       s.byID(s.updateID, true),
		"POST _expire/{}":       s.byID(s.expire, false),
		"POST _persist/{}":      s.byID(s.persist, false),
		"GET _ttl/{}":           s.byID(s.ttl, false),
		"POST _update_by_query": s.partUpdateBy,
		"POST _delete_by_query": s.partDeleteBy,
		"PUT _indexes/{}":       s.partIndex,
		"DELETE _indexes/{}":    s.partIndex,
		"GET _stats":            s.partStats,
	}
}

// partitioned answers a route on a logical collection, or reports false
// when name is not one.
func (s *Server) partitioned(w http.ResponseWriter, r *http.Request, db *engine.DB) bool {
	def, ok := db.Partitioned(r.PathValue("coll"))
	if !ok {
		return false
	}

	h := s.partitionRoutes()[r.Method+" "+r.PathValue("action")]

	if h == nil {
		writeError(w, &apiError{status: http.StatusBadRequest, code: "not_supported", msg: "not on a partitioned collection: call one partition, such as " + def.Name + "-" + engine.PeriodOf(def.PartitionBy, time.Now())})

		return true
	}

	h(w, r, db, def)

	return true
}

// partEnsure is PUT on the logical name: it merges the definition and gives
// the indexes to every partition.
func (s *Server) partEnsure(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	var decl catalog.Def

	body, err := readBody(w, r, engine.DefaultMaxLine)
	if err == nil {
		err = decode(body, &decl)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	decl.Name = def.Name

	final, err := db.EnsurePartitioned(decl)
	if err != nil {
		writeError(w, err)

		return
	}

	s.writePartitioned(w, db, final)
}

func (s *Server) partGet(w http.ResponseWriter, _ *http.Request, db *engine.DB, def catalog.Def) {
	s.writePartitioned(w, db, def)
}

func (s *Server) writePartitioned(w http.ResponseWriter, db *engine.DB, def catalog.Def) {
	writeJSON(w, http.StatusOK, map[string]any{
		"definition": def,
		"partitions": append([]string{}, db.Partitions(def, false, time.Now())...),
	})
}

func (s *Server) partDrop(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	s.partSwap(w, r, db, def, true)
}

func (s *Server) partTruncate(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	s.partSwap(w, r, db, def, false)
}

// partSwap drops every partition, and the definition with drop.
func (s *Server) partSwap(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def, drop bool) {
	if r.URL.Query().Get("confirm") != def.Name {
		writeError(w, fmt.Errorf("%w: confirm must be %q", engine.ErrConfirm, def.Name))

		return
	}

	if err := db.DropPartitioned(def, drop); err != nil {
		writeError(w, err)

		return
	}

	key := "truncated"

	if drop {
		key = "dropped"
	}

	writeJSON(w, http.StatusOK, map[string]any{"collection": def.Name, key: true})
}

func (s *Server) partSearch(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	s.read(w, r, db, &s.metrics.find, s.withLookup(r, db, func(ctx context.Context, body []byte) (collection.Page, error) {
		return db.FindPartitioned(ctx, def, body)
	}))
}

func (s *Server) partCount(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	s.runCount(w, r, db, func(ctx context.Context, body []byte) (collection.Page, error) {
		return db.FindPartitioned(ctx, def, body)
	})
}

func (s *Server) partAggregate(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	s.read(w, r, db, &s.metrics.aggregate, s.withLookupStages(r, db, func(ctx context.Context, body []byte) (collection.Page, error) {
		return db.AggregatePartitioned(ctx, def, body)
	}))
}

// partInsertOne sends the document to the partition of its time field.
func (s *Server) partInsertOne(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	body, err := readBody(w, r, db.MaxLine(def.Name))
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.PartitionFor(def, body)
	if err != nil {
		writeError(w, err)

		return
	}

	r.SetPathValue("coll", col.Name())
	r.Body = io.NopCloser(bytes.NewReader(body))

	s.insertOne(w, r, db)
}

// partBulk groups the documents by partition and writes each group in one
// transaction. The results keep the order of docs. A failure stops the
// call; the groups written before it stay.
func (s *Server) partBulk(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	var req bulkReq

	body, err := readBody(w, r, db.MaxLine(def.Name))
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

	groups := map[string][]int{}
	order := []string{}

	for i, doc := range req.Docs {
		col, err := db.PartitionFor(def, doc)
		if err != nil {
			writeError(w, err)

			return
		}

		if _, seen := groups[col.Name()]; !seen {
			order = append(order, col.Name())
		}

		groups[col.Name()] = append(groups[col.Name()], i)
	}

	out := make([]writeResult, len(req.Docs))

	for _, name := range order {
		docs := make([][]byte, len(groups[name]))

		for j, i := range groups[name] {
			docs[j] = req.Docs[i]
		}

		res, ok := s.insertDocs(w, r, db, name, docs, expireIn, req.Upsert)
		if !ok {
			return
		}

		for j, i := range groups[name] {
			out[i] = res[j]
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// byID runs a route by id on the partition that holds the id, newest
// partition first. With create (an upsert by id), a missing id goes to the
// partition of the time field of the operators, or of now.
func (s *Server) byID(h handler, create bool) partitionHandler {
	return func(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
		id := r.PathValue("id")
		names := db.Partitions(def, true, time.Now())

		for _, name := range slices.Backward(names) {
			col, err := db.Get(name)
			if err != nil {
				continue
			}

			if _, ok := col.Get(id); ok {
				r.SetPathValue("coll", name)
				h(w, r, db)

				return
			}
		}

		if create {
			s.createByID(w, r, db, def, h)

			return
		}

		switch r.Method {
		case http.MethodDelete:
			writeJSON(w, http.StatusOK, map[string]int{"deleted": 0})
		default:
			writeError(w, &apiError{status: http.StatusNotFound, code: "not_found", msg: "no such document"})
		}
	}
}

// createByID is an _update/{id} of an id that no partition holds: with
// upsert it creates the document in the partition of its time; without it
// nothing matches.
func (s *Server) createByID(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def, h handler) {
	var req updateReq

	body, err := readBody(w, r, db.MaxLine(def.Name))
	if err == nil {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	if !req.Upsert {
		writeJSON(w, http.StatusOK, map[string]any{"matched": 0, "modified": 0})

		return
	}

	col, err := db.PartitionAt(def, engine.TimeOfUpsert(def, req.Update))
	if err != nil {
		writeError(w, err)

		return
	}

	r.SetPathValue("coll", col.Name())
	r.Body = io.NopCloser(bytes.NewReader(body))

	h(w, r, db)
}

// partUpdateBy runs the update on every live partition, newest first.
// Without many it stops at the first partition that matched.
func (s *Server) partUpdateBy(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	var req updateReq

	body, err := readBody(w, r, db.MaxLine(def.Name))
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

	joined, err := collection.JoinUpdate(req.Query, req.Update)
	if err != nil {
		writeError(w, err)

		return
	}

	raw, _ := json.Marshal(joined)
	opts := collection.UpdateOpts{Many: req.Many, ExpireIn: expireIn}
	out := map[string]any{"matched": 0, "modified": 0}
	matched, modified := 0, 0

	err = s.eachPartition(db, def, func(col *engine.Collection) (bool, error) {
		res, err := col.Update(r.Context(), raw, opts)
		if err != nil {
			return false, err
		}

		matched += res.Matched
		modified += res.Modified

		if res.Doc != nil && !req.Many {
			out["doc"] = res.Doc
		}

		return !req.Many && res.Matched > 0, nil
	})
	if err != nil {
		writeError(w, err)

		return
	}

	if matched == 0 && req.Upsert {
		s.upsertByQuery(w, r, db, def, req, raw, expireIn)

		return
	}

	out["matched"], out["modified"] = matched, modified
	s.metrics.writes.Add(int64(modified))

	writeJSON(w, http.StatusOK, out)
}

// upsertByQuery creates the document of an upsert that matched nothing, in
// the partition of the time field of its operators (or of now).
func (s *Server) upsertByQuery(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def, req updateReq, raw []byte, expireIn time.Duration) {
	col, err := db.PartitionAt(def, engine.TimeOfUpsert(def, req.Update))
	if err != nil {
		writeError(w, err)

		return
	}

	res, err := col.Update(r.Context(), raw, collection.UpdateOpts{Upsert: true, ExpireIn: expireIn})
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(res.Modified))

	writeJSON(w, http.StatusOK, map[string]any{"matched": res.Matched, "modified": res.Modified, "doc": res.Doc})
}

// partDeleteBy deletes on every live partition, newest first. Without many
// it stops at the first partition that deleted.
func (s *Server) partDeleteBy(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	var req deleteReq

	body, err := readBody(w, r, db.MaxLine(def.Name))
	if err == nil {
		err = decode(body, &req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	raw, _ := json.Marshal(req.Query)
	deleted := 0

	err = s.eachPartition(db, def, func(col *engine.Collection) (bool, error) {
		n, err := col.Delete(r.Context(), raw, req.Many)
		deleted += n

		return !req.Many && n > 0, err
	})
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(deleted))

	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}

// eachPartition calls fn on the live partitions, newest first, until fn
// returns stop or an error.
func (s *Server) eachPartition(db *engine.DB, def catalog.Def, fn func(*engine.Collection) (bool, error)) error {
	for _, name := range slices.Backward(db.Partitions(def, true, time.Now())) {
		col, err := db.Get(name)
		if err != nil {
			continue
		}

		stop, err := fn(col)
		if err != nil || stop {
			return err
		}
	}

	return nil
}

// partIndex adds or drops an index on the logical collection and on every
// partition.
func (s *Server) partIndex(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	field := r.PathValue("field")

	var (
		final catalog.Def
		err   error
	)

	if r.Method == http.MethodPut {
		final, err = db.EnsurePartitioned(catalog.Def{Name: def.Name, Indexes: []string{field}})
	} else {
		final, err = db.DropIndex(def.Name, field)

		for _, name := range db.Partitions(def, false, time.Now()) {
			if err == nil {
				_, err = db.DropIndex(name, field)
			}
		}
	}

	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"collection": def.Name, "indexes": final.Indexes})
}

// partStats is the stats of every partition.
func (s *Server) partStats(w http.ResponseWriter, _ *http.Request, db *engine.DB, def catalog.Def) {
	parts := []statsBody{}
	rows := 0

	for _, name := range db.Partitions(def, false, time.Now()) {
		col, err := db.Get(name)
		if err != nil {
			continue
		}

		st := col.Stats()
		parts = append(parts, collectionStats(st, diskusage.ScanCollection(col.Dir(), st.Name)))
		rows += st.Rows
	}

	writeJSON(w, http.StatusOK, map[string]any{"name": def.Name, "partition_by": def.PartitionBy, "rows": rows, "partitions": parts})
}
