package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/settings"
)

// rotateKey is POST /_cluster/key: a key rotation (ndb key rotate). Only
// the unix socket may: who rotates the key has a shell on the host, and no
// token of the TCP listener does it, not even the full one. With
// {"env":true} the answer has the new key, once, for NDB_ENCRYPTION_KEY.
func (s *Server) rotateKey(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	if r.PathValue("id") != "key" {
		unknownRoute(w)

		return
	}

	if !isUnix(r) {
		writeError(w, &apiError{status: http.StatusForbidden, code: "forbidden", msg: "the key rotates on the unix socket only (ndb key rotate on the host)"})

		return
	}

	var req struct {
		Env bool `json:"env"`
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<10))

	if err == nil && len(body) > 0 {
		err = json.Unmarshal(body, &req)
	}

	if err != nil {
		writeError(w, badRequest("the body must be {} or {\"env\":true}"))

		return
	}

	cur := db.Key()

	key, err := db.RotateKey(req.Env)

	switch {
	case errors.Is(err, settings.ErrRotating):
		writeError(w, &apiError{status: http.StatusConflict, code: "key_rotating", msg: "a key rotation is not finished yet: see GET /_cluster/settings"})

		return
	case errors.Is(err, engine.ErrEnvKey):
		writeError(w, badRequest("%v", err))

		return
	case err != nil:
		writeError(w, err)

		return
	}

	s.logInfo(w, r, "key rotation", slog.Bool("env", req.Env))

	if req.Env {
		writeJSON(w, http.StatusOK, map[string]any{"key": key, "key_id": settings.NextID(cur.ID), "from": cur.ID})

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"key_id": db.Key().ID, "from": cur.ID})
}
