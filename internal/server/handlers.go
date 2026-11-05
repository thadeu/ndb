package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
)

type writeResult struct {
	ID string `json:"id"`
	V  uint64 `json:"v"`
}

type indexBody struct {
	Field string `json:"field"`
	State string `json:"state"`
	Done  int    `json:"done"`
	Total int    `json:"total"`
}

type statsBody struct {
	Name        string            `json:"name"`
	Write       catalog.WriteMode `json:"write"`
	Rows        int               `json:"rows"`
	Quarantined int64             `json:"quarantined"`
	Indexes     []indexBody       `json:"indexes"`
}

type insertReq struct {
	Insert []json.RawMessage `json:"insert"`
}

type updateReq struct {
	Update struct {
		ID        string         `json:"id"`
		Set       map[string]any `json:"set"`
		Unset     []string       `json:"unset"`
		IfVersion uint64         `json:"if_version"`
	} `json:"update"`
}

type deleteReq struct {
	Delete struct {
		ID        string `json:"id"`
		IfVersion uint64 `json:"if_version"`
	} `json:"delete"`
}

// find streams the rows as NDJSON. The page metadata goes in headers, which
// are sent before the first row: X-Ndb-Next (absent on the last page),
// X-Ndb-Plan, X-Ndb-Examined and, with count: true, X-Ndb-Total.
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

	page, err := col.Find(r.Context(), body)
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

	col, err := db.GetOrCreate(name)
	if err != nil {
		writeError(w, err)

		return
	}

	res, err := col.Insert(r.Context(), docs...)
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

func (s *Server) update(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req updateReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	u := req.Update
	ch := collection.Change{Set: u.Set, Unset: u.Unset}

	v, err := col.Update(r.Context(), u.ID, ch, u.IfVersion)
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(1)

	writeJSON(w, http.StatusOK, writeResult{ID: u.ID, V: v})
}

func (s *Server) remove(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req deleteReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err != nil {
		writeError(w, err)

		return
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	d := req.Delete

	if err := col.Delete(r.Context(), d.ID, d.IfVersion); err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(1)

	writeJSON(w, http.StatusOK, map[string]string{"id": d.ID})
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

func (s *Server) stats(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	col, err := db.Get(r.PathValue("coll"))
	if err != nil {
		writeError(w, err)

		return
	}

	st := col.Stats()

	writeJSON(w, http.StatusOK, statsBody{
		Name:        st.Name,
		Write:       st.Write,
		Rows:        st.Rows,
		Quarantined: st.Quarantined,
		Indexes:     indexBodies(st),
	})
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
