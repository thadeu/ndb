// Package server serves the HTTP API of plan §12.1 on one or more listeners.
// Every data endpoint answers 503 until Attach gives it an open engine, so
// /health is only ok after recovery.
package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/engine"
)

// StatusClientClosed is the status for a request whose client went away
// before the answer (the nginx convention).
const StatusClientClosed = 499

type Server struct {
	db      atomic.Pointer[engine.DB]
	metrics metrics
	handler http.Handler
	http    *http.Server
}

func New() *Server {
	s := &Server{}
	s.handler = s.routes()
	s.http = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s
}

// Attach gives the server the open engine. Until then it is not ready.
func (s *Server) Attach(db *engine.DB) {
	s.db.Store(db)
}

func (s *Server) Handler() http.Handler {
	return s.handler
}

// Serve answers on l until Shutdown. It returns nil after a Shutdown.
func (s *Server) Serve(l net.Listener) error {
	err := s.http.Serve(l)

	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

// Shutdown stops the listeners and waits for the in-flight requests, until
// ctx ends.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/c/{coll}/find", s.ready(s.find))
	mux.HandleFunc("POST /v1/c/{coll}/insert", s.ready(s.insert))
	mux.HandleFunc("POST /v1/c/{coll}/update", s.ready(s.update))
	mux.HandleFunc("POST /v1/c/{coll}/delete", s.ready(s.remove))
	mux.HandleFunc("PUT /v1/c/{coll}", s.ready(s.ensure))
	mux.HandleFunc("GET /v1/c", s.ready(s.list))
	mux.HandleFunc("DELETE /v1/c/{coll}/indexes/{field}", s.ready(s.dropIndex))
	mux.HandleFunc("GET /v1/c/{coll}/stats", s.ready(s.stats))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /metrics", s.metricsPage)

	return s.count(mux)
}

// ready gives fn the engine, or answers 503 while recovery runs.
func (s *Server) ready(fn func(http.ResponseWriter, *http.Request, *engine.DB)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		db := s.db.Load()

		if db == nil {
			writeError(w, &apiError{status: http.StatusServiceUnavailable, code: "not_ready", msg: "ndb is recovering"})

			return
		}

		fn(w, r, db)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	if s.db.Load() == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "recovering"})

		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
