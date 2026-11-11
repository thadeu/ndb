package server

import (
	"net/http"

	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
)

type txReq struct {
	Ops    []collection.TxOp `json:"ops"`
	Locks  []string          `json:"locks"`
	Unlock bool              `json:"unlock"`
}

// tx is POST /{coll}/_tx: {"ops", "locks", "unlock"}. The ops are written
// in one step, all or none. See docs/dev/http/documents.md.
func (s *Server) tx(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	name := r.PathValue("coll")

	var req txReq

	body, err := readBody(w, r, db.MaxLine(name))
	if err == nil {
		err = decode(body, &req)
	}

	if err == nil && len(req.Ops) == 0 {
		err = badRequest("a transaction needs ops")
	}

	if err != nil {
		writeError(w, err)

		return
	}

	col, err := db.Get(name)
	if err != nil {
		writeError(w, err)

		return
	}

	results, err := col.Tx(r.Context(), req.Ops, req.Locks, req.Unlock)
	if err != nil {
		writeError(w, err)

		return
	}

	s.metrics.writes.Add(int64(len(results)))

	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
