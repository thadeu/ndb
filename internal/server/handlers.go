package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
)

type writeResult struct {
	ID string `json:"_id"`
	V  uint64 `json:"v"`
}

type indexBody struct {
	Field string `json:"field"`
	State string `json:"state"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

type insertReq struct {
	Insert   []json.RawMessage `json:"insert"`
	ExpireIn json.RawMessage   `json:"expire_in"`
}

// find streams the rows as NDJSON. The page metadata goes in headers, which
// are sent before the first row: X-Ndb-Next (absent on the last page),
// X-Ndb-Plan, X-Ndb-Examined and, with count: true, X-Ndb-Total. With
// explain: true, X-Ndb-Explain is the JSON of the plan counters.
func (s *Server) find(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	began := time.Now()

	page, err := col.Find(r.Context(), body)

	s.metrics.find.Observe(time.Since(began))

	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.rows.Add(int64(len(page.Rows)))

	writePage(w, page)
}

// aggregate streams the output rows of a pipeline as NDJSON, with the same
// headers as find (plan §10.2, §10.6).
func (s *Server) aggregate(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	began := time.Now()

	page, err := col.Aggregate(r.Context(), body)

	s.metrics.aggregate.Observe(time.Since(began))

	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.rows.Add(int64(len(page.Rows)))

	writePage(w, page)
}

func (s *Server) insert(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req insertReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	if len(req.Insert) == 0 {
		writeError(w, badRequest("insert must be a non-empty array"))

		return
	}

	docs := make([][]byte, len(req.Insert))

	for i, d := range req.Insert {
		docs[i] = d
	}

	expireIn, err := parseExpireIn(req.ExpireIn)
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.GetOrCreate(name)
	if err != nil {
		writeError(w, err)

		return
	}

	var res []collection.Result

	if expireIn > 0 {
		res, err = col.InsertIn(r.Context(), expireIn, docs...)
	} else {
		res, err = col.Insert(r.Context(), docs...)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(len(res)))

	out := make([]writeResult, len(res))

	for i, x := range res {
		out[i] = writeResult{ID: x.ID, V: x.V}
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": out})
}

// update is the atomic update by filter: the body mixes the filter and the
// write operators, the options are in the query string.
func (s *Server) update(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	s.runUpdate(w, r, db, "")
}

// updateID is POST /v1/c/{coll}/{id}/update: the same update, on the
// document with this _id. The body filter keys are extra guards.
func (s *Server) updateID(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	s.runUpdate(w, r, db, r.PathValue("id"))
}

func (s *Server) runUpdate(w http.ResponseWriter, r *http.Request, db *engine.DB, id string) {
	name := r.PathValue("coll")

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	opts, err := updateOpts(r)
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	var res collection.UpdateResult

	if id == "" {
		res, err = col.Update(r.Context(), body, opts)
	} else {
		opts.Many = false

		res, err = col.UpdateID(r.Context(), id, body, opts)
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

// get is GET /v1/c/{coll}/{id}: the document with its "_v".
func (s *Server) get(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	doc, ok := col.Get(r.PathValue("id"))
	if !ok {
		writeError(w, &apiError{status: http.StatusNotFound, code: "not_found", msg: "no such document"})

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(append(doc.Doc, '\n'))
}

// removeID is DELETE /v1/c/{coll}/{id}.
func (s *Server) removeID(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	n, err := col.DeleteID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(n))

	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

func updateOpts(r *http.Request) (collection.UpdateOpts, error) {
	var opts collection.UpdateOpts

	many, err := boolParam(r, "many")
	if err != nil {
		return opts, err
	}

	upsert, err := boolParam(r, "upsert")
	if err != nil {
		return opts, err
	}

	expireIn, err := parseExpireParam(r.URL.Query().Get("expire_in"))
	if err != nil {
		return opts, err
	}

	return collection.UpdateOpts{Many: many, Upsert: upsert, ExpireIn: expireIn}, nil
}

// boolParam reads an optional true/false query parameter.
func boolParam(r *http.Request, name string) (bool, error) {
	raw := r.URL.Query().Get(name)

	if raw == "" {
		return false, nil
	}

	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, badRequest("%s must be true or false", name)
	}

	return b, nil
}

// parseExpireParam reads expire_in from the query string: a duration
// string or a number of seconds, like the field of the insert body.
func parseExpireParam(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}

	if _, err := strconv.ParseFloat(raw, 64); err != nil {
		raw = strconv.Quote(raw)
	}

	return parseExpireIn(json.RawMessage(raw))
}

// remove is delete by filter: the body is the filter.
func (s *Server) remove(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	many, err := boolParam(r, "many")
	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	n, err := col.Delete(r.Context(), body, many)
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(n))

	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}

// ensure is ensure_collection (PUT /v1/c/{coll}). The answer has the final
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

func writePage(w http.ResponseWriter, page collection.Page) {
	h := w.Header()

	h.Set("Content-Type", "application/x-ndjson")
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

	w.WriteHeader(http.StatusOK)

	for _, row := range page.Rows {
		if _, err := w.Write(row); err != nil {
			return
		}

		if _, err := w.Write([]byte{'\n'}); err != nil {
			return
		}
	}
}
