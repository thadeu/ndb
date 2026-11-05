package server

import (
	"net"
	"net/http"

	"github.com/thadeu/ndb/internal/engine"
)

// backup runs engine.DB.Backup into a dir of the server host. It writes to
// the server disk, so it answers only on the unix socket.
func (s *Server) backup(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	if addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr); addr == nil || addr.Network() != "unix" {
		writeError(w, &apiError{status: http.StatusForbidden, code: "forbidden", msg: "backup is allowed on the unix socket only"})

		return
	}

	body, err := readBody(w, r, 1<<16)
	if err != nil {
		writeError(w, err)

		return
	}

	var req struct {
		Dest string `json:"dest"`
	}

	if err := decode(body, &req); err != nil {
		writeError(w, err)

		return
	}

	if req.Dest == "" {
		writeError(w, badRequest("dest is required"))

		return
	}

	last, err := db.Backup(r.Context(), req.Dest)
	if err != nil {
		writeError(w, badRequest("%v", err))

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"dest": req.Dest, "collections": last})
}
