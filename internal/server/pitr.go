package server

import (
	"net/http"
	"time"

	"github.com/thadeu/ndb/internal/engine"
)

// Point in time recovery routes (M17):
//
//	POST /{coll}/_restore {"until", "into"}    a copy of coll as it was at until
//	POST /_restore {"collection", "until", "into"}   the same, by name in the body
//	GET  /{coll}/_pitr                          the window of coll
//	GET  /_pitr                                 the window of every collection

type restoreReq struct {
	Collection string `json:"collection,omitempty"`
	Until      string `json:"until"`
	Into       string `json:"into"`
}

// restore is POST /{coll}/_restore. coll may be dropped, while it is in the
// window.
func (s *Server) restore(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	s.runRestore(w, r, db, r.PathValue("coll"))
}

// restoreAny is POST /_restore, for a client that keeps the name in the
// body.
func (s *Server) restoreAny(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	s.runRestore(w, r, db, "")
}

func (s *Server) runRestore(w http.ResponseWriter, r *http.Request, db *engine.DB, name string) {
	var req restoreReq

	body, err := readBody(w, r, engine.DefaultMaxLine)
	if err == nil {
		err = decode(body, &req)
	}

	if name == "" {
		name = req.Collection
	}

	var until time.Time

	switch {
	case err != nil:
	case name == "":
		err = badRequest("collection is required")
	case req.Into == "":
		err = badRequest("into is required")
	default:
		until, err = time.Parse(time.RFC3339Nano, req.Until)
		if err != nil {
			err = badRequest("until must be an RFC 3339 time, such as 2026-11-12T14:04:00Z")
		}
	}

	for _, coll := range []string{name, req.Into} {
		if err == nil {
			err = s.mayAlso(r, coll, accessAdmin)
		}
	}

	start := time.Now()

	if err == nil {
		err = db.Restore(name, req.Into, until, start)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"collection":  req.Into,
		"restored":    name,
		"until":       until.UTC(),
		"duration_ms": time.Since(start).Milliseconds(),
	})
}

// pitr is GET /{coll}/_pitr: the window, the oldest and newest instant a
// restore reaches, and the disk it holds.
func (s *Server) pitr(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	info, err := db.PITR(r.PathValue("coll"), time.Now())
	if err != nil {
		writeError(w, err)

		return
	}

	writeJSON(w, http.StatusOK, info)
}

// pitrAll is GET /_pitr: the daemon window and every collection with one,
// dropped ones included.
func (s *Server) pitrAll(w http.ResponseWriter, _ *http.Request, db *engine.DB) {
	writeJSON(w, http.StatusOK, map[string]any{
		"default":     db.PITRDefault(),
		"collections": append([]engine.PITRInfo{}, db.AllPITR(time.Now())...),
	})
}
