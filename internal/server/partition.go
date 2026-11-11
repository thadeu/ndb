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
		"POST _lock/{}":         s.byID(s.lock, false),
		"POST _unlock/{}":       s.byID(s.unlock, false),
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

// partInsertOne sends the document to the partition of its time field. An
// _id that another partition holds is 409 duplicate_id, as in one
// collection. The insert is checked before a new partition is made.
func (s *Server) partInsertOne(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
	body, err := readBody(w, r, db.MaxLine(def.Name))

	if err != nil {
		writeError(w, err)

		return
	}

	plan, err := s.planWrite(r.Context(), db, def, [][]byte{body}, 0, false)

	if err == nil {
		_, err = db.Partition(def, plan.order[0])
	}

	if err != nil {
		writeError(w, err)

		return
	}

	r.SetPathValue("coll", plan.order[0])
	r.Body = io.NopCloser(bytes.NewReader(body))

	s.insertOne(w, r, db)
}

// partBulk groups the documents by partition and writes each group in one
// transaction. The results keep the order of docs. It is all or nothing:
// every group is checked first (time field, ids across partitions, unique
// values, documents), and nothing is written when one check fails. Only a
// write that fails after the checks (a disk error, or a write of another
// call between the check and the write that takes the same id or unique
// value) keeps the groups written before it.
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

	docs := make([][]byte, len(req.Docs))

	for i, doc := range req.Docs {
		docs[i] = doc
	}

	plan, err := s.planWrite(r.Context(), db, def, docs, expireIn, req.Upsert)

	if err != nil {
		writeError(w, err)

		return
	}

	out := make([]writeResult, len(docs))

	for _, name := range plan.order {
		if _, err := db.Partition(def, name); err != nil {
			writeError(w, err)

			return
		}

		res, ok := s.insertDocs(w, r, db, name, plan.docs(name, docs), expireIn, req.Upsert)

		if !ok {
			return
		}

		for j, i := range plan.groups[name] {
			out[i] = res[j]
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// writePlan is the partition of each document of a write: groups holds
// the indexes of the documents of each partition, order the partitions in
// the order of their first document.
type writePlan struct {
	groups map[string][]int
	order  []string
}

func (p writePlan) docs(name string, docs [][]byte) [][]byte {
	out := make([][]byte, len(p.groups[name]))

	for j, i := range p.groups[name] {
		out[j] = docs[i]
	}

	return out
}

// planWrite picks the partition of each document and checks the whole
// write, before it makes any partition or writes any document. A document
// goes to the partition of its time field. An _id that a partition holds
// is duplicate_id, or with upsert goes to that partition, where it merges
// into the stored document (also when its time field is of another
// period).
func (s *Server) planWrite(ctx context.Context, db *engine.DB, def catalog.Def, docs [][]byte, expireIn time.Duration, upsert bool) (writePlan, error) {
	plan := writePlan{groups: map[string][]int{}}
	held := map[string]string{}

	for i, doc := range docs {
		name, err := engine.PartitionName(def, doc)

		if err != nil {
			return writePlan{}, err
		}

		if id, ok := docIDOf(doc); ok {
			name, err = s.targetOf(db, def, id, name, held, upsert)

			if err != nil {
				return writePlan{}, err
			}
		}

		if _, seen := plan.groups[name]; !seen {
			plan.order = append(plan.order, name)
		}

		plan.groups[name] = append(plan.groups[name], i)
	}

	for _, name := range plan.order {
		// One group into a partition that exists is one transaction, which
		// checks itself.
		if _, err := db.Get(name); err == nil && len(plan.order) == 1 {
			continue
		}

		if err := db.CheckPartitionInsert(ctx, def, name, expireIn, upsert, plan.docs(name, docs)); err != nil {
			return writePlan{}, err
		}
	}

	return plan, nil
}

// targetOf is the partition of a document with this _id: the partition
// that holds the id, or name. held keeps the ids seen in this write.
func (s *Server) targetOf(db *engine.DB, def catalog.Def, id, name string, held map[string]string, upsert bool) (string, error) {
	if prev, ok := held[id]; ok {
		if !upsert {
			return "", &apiError{status: http.StatusConflict, code: "duplicate_id", msg: id + " is in docs twice"}
		}

		return prev, nil
	}

	holder := s.holderOf(db, def, id)

	switch {
	case holder == "" || holder == name:
	case upsert:
		name = holder
	default:
		return "", &apiError{status: http.StatusConflict, code: "duplicate_id", msg: id + " already exists in " + holder}
	}

	held[id] = name

	return name, nil
}

// holderOf is the live partition that holds id, newest first, or "".
func (s *Server) holderOf(db *engine.DB, def catalog.Def, id string) string {
	for _, name := range slices.Backward(db.Partitions(def, true, time.Now())) {
		col, err := db.Get(name)

		if err != nil {
			continue
		}

		if _, ok := col.Get(id); ok {
			return name
		}
	}

	return ""
}

// docIDOf is the _id of a document, when it is a string. A document with
// no _id gets a new one, which no partition holds.
func docIDOf(doc []byte) (string, bool) {
	var head struct {
		ID any `json:"_id"`
	}

	if json.Unmarshal(doc, &head) != nil {
		return "", false
	}

	id, ok := head.ID.(string)

	return id, ok && id != ""
}

// byID runs a route by id on the partition that holds the id, newest
// partition first. With create (an upsert by id), a missing id goes to the
// partition of the time field of the operators, or of now.
func (s *Server) byID(h handler, create bool) partitionHandler {
	return func(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def) {
		if name := s.holderOf(db, def, r.PathValue("id")); name != "" {
			r.SetPathValue("coll", name)
			h(w, r, db)

			return
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

// createByID is an _update/{id} of an id that no partition holds. The
// body is checked first, as on one collection. With upsert it creates the
// document in the partition of its time. Without it nothing matches: the
// update runs on the newest partition, which checks the whole body and
// matches nothing.
func (s *Server) createByID(w http.ResponseWriter, r *http.Request, db *engine.DB, def catalog.Def, h handler) {
	var req updateReq

	body, err := readBody(w, r, db.MaxLine(def.Name))

	if err == nil {
		err = decode(body, &req)
	}

	if err == nil {
		err = checkUpdateByID(req)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	r.Body = io.NopCloser(bytes.NewReader(body))

	if !req.Upsert {
		names := db.Partitions(def, true, time.Now())

		if len(names) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"matched": 0, "modified": 0})

			return
		}

		r.SetPathValue("coll", names[len(names)-1])
		h(w, r, db)

		return
	}

	col, err := db.PartitionAt(def, engine.TimeOfUpsert(def, req.Update))

	if err != nil {
		writeError(w, err)

		return
	}

	r.SetPathValue("coll", col.Name())

	h(w, r, db)
}

// checkUpdateByID runs the checks of an _update/{id} body that need no
// collection.
func checkUpdateByID(req updateReq) error {
	if _, err := parseExpireIn(req.ExpireIn); err != nil {
		return err
	}

	if req.Many {
		return badRequest("many does not apply to one _id")
	}

	_, err := collection.JoinUpdate(req.Query, req.Update)

	return err
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
			out["doc"] = openDoc(r, db, r.PathValue("coll"), res.Doc)
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

	writeJSON(w, http.StatusOK, map[string]any{"matched": res.Matched, "modified": res.Modified, "doc": openDoc(r, db, r.PathValue("coll"), res.Doc)})
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

	// A body with no query is the empty filter, as on a plain collection.
	if req.Query == nil {
		req.Query = map[string]any{}
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
