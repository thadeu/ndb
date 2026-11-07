package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/engine"
)

// DefaultBlobLimit is the largest blob that POST /_blobs accepts.
const DefaultBlobLimit = 1 << 30

// SetBlobLimit sets the largest accepted blob, in bytes (0 = default 1 GiB).
// Call it before Serve.
func (s *Server) SetBlobLimit(n int64) {
	s.blobLimit = n
}

func (s *Server) blobMax() int64 {
	if s.blobLimit <= 0 {
		return DefaultBlobLimit
	}

	return s.blobLimit
}

// putBlob streams the raw body into the blob store and answers with the
// reference to keep in a document. The Content-Type goes into the reference.
func (s *Server) putBlob(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	limit := s.blobMax()
	typ := r.Header.Get("Content-Type")

	ref, err := db.Blobs().Put(http.MaxBytesReader(w, r.Body, limit), typ, limit)
	if err != nil {
		writeError(w, blobError(err, limit))

		return
	}

	s.metrics.writes.Add(1)

	writeJSON(w, http.StatusOK, ref)
}

// getBlob streams a blob. http.ServeContent handles Range and If-None-Match.
// The content has no stored type; a client that wants one passes ?type=.
func (s *Server) getBlob(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	rd, err := db.Blobs().Get(r.PathValue("ref"))
	if err != nil {
		writeError(w, blobError(err, 0))

		return
	}

	defer rd.Close()

	typ := r.URL.Query().Get("type")

	if typ == "" {
		typ = "application/octet-stream"
	}

	w.Header().Set("Content-Type", typ)
	w.Header().Set("Etag", `"`+strings.TrimPrefix(r.PathValue("ref"), "sha256:")+`"`)
	w.Header().Set("Cache-Control", "private, immutable")

	http.ServeContent(w, r, "", rd.ModTime(), rd)
}

func blobError(err error, limit int64) error {
	var tooBig *http.MaxBytesError

	switch {
	case errors.Is(err, blob.ErrTooLarge) || errors.As(err, &tooBig):
		return &apiError{status: http.StatusRequestEntityTooLarge, code: "request_too_large", msg: fmt.Sprintf("blob is over %d bytes", limit)}
	case errors.Is(err, blob.ErrBadRef):
		return &apiError{status: http.StatusBadRequest, code: "invalid_blob_ref", msg: "want sha256:<64 hex digits>"}
	case errors.Is(err, blob.ErrNotFound):
		return &apiError{status: http.StatusNotFound, code: "not_found", msg: "no such blob"}
	}

	return err
}
