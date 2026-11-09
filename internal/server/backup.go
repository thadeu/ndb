package server

import (
	"net"
	"net/http"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/engine"
)

// backup streams a consistent backup file (see engine.DB.Backup) as the
// response body. It copies everything the daemon holds, so it answers only on
// the unix socket. The client compresses and stores the stream. If the copy
// fails after the first byte, the connection is aborted: the file then has no
// end line and a restore refuses it.
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
		NoBlobs bool `json:"no_blobs"`
	}

	if len(body) > 0 {
		if err := decode(body, &req); err != nil {
			writeError(w, err)

			return
		}
	}

	out := &firstWrite{w: w}

	err = db.Backup(r.Context(), out, backup.Options{Version: s.version, NoBlobs: req.NoBlobs, Streams: s.streams.Snapshot})
	if err == nil {
		return
	}

	if !out.started {
		writeError(w, badRequest("%v", err))

		return
	}

	panic(http.ErrAbortHandler)
}

// firstWrite sets the content type at the first byte, so an error before any
// byte can still be a JSON error answer.
type firstWrite struct {
	w       http.ResponseWriter
	started bool
}

func (f *firstWrite) Write(p []byte) (int, error) {
	if !f.started {
		f.started = true

		f.w.Header().Set("Content-Type", "application/x-ndjson")
	}

	return f.w.Write(p)
}
