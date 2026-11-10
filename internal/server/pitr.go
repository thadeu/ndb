package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/thadeu/ndb/internal/engine"
)

// Point in time recovery routes (M17):
//
//	POST /{coll}/_restore {"at", "to"}    a copy of coll, as it was at at, in to
//	POST /_restore {"collection", "at", "to"}   the same, by name in the body
//	GET  /{coll}/_pitr                          the window of coll
//	GET  /_pitr                                 the window of every collection

type restoreReq struct {
	Collection string `json:"collection,omitempty"`
	At         string `json:"at"`
	To         string `json:"to"`
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

	var at time.Time

	switch {
	case err != nil:
	case name == "":
		err = badRequest("collection is required")
	case req.To == "":
		err = badRequest("to is required")
	default:
		at, err = time.Parse(time.RFC3339Nano, req.At)
		if err != nil {
			err = badRequest("at must be an RFC 3339 time, such as 2026-11-12T14:04:00Z")
		}
	}

	for _, coll := range []string{name, req.To} {
		if err == nil {
			err = s.mayAlso(r, coll, accessAdmin)
		}
	}

	longPoll(w)

	start := time.Now()

	if err == nil {
		err = db.Restore(name, req.To, at, start)
	}

	if err != nil {
		writeError(w, err)

		return
	}

	s.logInfo(w, r, "collection restored", slog.String("from", name), slog.String("to", req.To), slog.Time("at", at.UTC()), slog.Int64("ms", time.Since(start).Milliseconds()))

	writeJSON(w, http.StatusOK, map[string]any{
		"collection":  req.To,
		"restored":    name,
		"at":          at.UTC(),
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
