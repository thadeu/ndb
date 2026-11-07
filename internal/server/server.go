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

	// heartbeat is the idle time of a watch stream before a heartbeat line.
	// stopCtx ends when Shutdown starts, so open watch streams do not hold it.
	heartbeat time.Duration
	stopCtx   context.Context

	// blobLimit is the largest blob in bytes (0 = DefaultBlobLimit).
	blobLimit int64

	// version is what /v1/stats reports; started is when New ran.
	version string
	started time.Time

	// token, when set, is the bearer token every non-unix request must carry.
	token []byte
}

func New() *Server {
	stopCtx, stop := context.WithCancel(context.Background())
	s := &Server{heartbeat: defaultHeartbeat, stopCtx: stopCtx, started: time.Now()}
	s.handler = s.authenticate(s.routes())
	s.http = &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	s.http.RegisterOnShutdown(stop)

	return s
}

// SetToken makes every request that does not come in on the unix socket carry
// "Authorization: Bearer <token>". Call it before Serve. Empty = no token.
func (s *Server) SetToken(token string) {
	s.token = []byte(token)
}

// SetVersion is the daemon version that GET /v1/stats reports.
func (s *Server) SetVersion(v string) {
	s.version = v
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

	mux.HandleFunc("POST /v1/c/{coll}/find", s.readyOr(s.find, s.findPattern))
	mux.HandleFunc("POST /v1/c/{coll}/aggregate", s.readyOr(s.aggregate, s.aggregatePattern))
	mux.HandleFunc("POST /v1/c/{coll}/insert", s.ready(s.insert))
	mux.HandleFunc("POST /v1/c/{coll}/update", s.ready(s.update))
	mux.HandleFunc("POST /v1/c/{coll}/delete", s.ready(s.remove))
	mux.HandleFunc("POST /v1/c/{coll}/expire", s.ready(s.expire))
	mux.HandleFunc("POST /v1/c/{coll}/persist", s.ready(s.persist))
	mux.HandleFunc("POST /v1/c/{coll}/watch", s.ready(s.watch))
	mux.HandleFunc("POST /v1/c/{coll}/ack", s.ready(s.ack))
	mux.HandleFunc("PUT /v1/c/{coll}", s.ready(s.ensure))
	mux.HandleFunc("GET /v1/c/{coll}/{id}", s.ready(s.get))
	mux.HandleFunc("POST /v1/c/{coll}/{id}/update", s.ready(s.updateID))
	mux.HandleFunc("DELETE /v1/c/{coll}/{id}", s.ready(s.removeID))
	mux.HandleFunc("GET /v1/c/{coll}/{id}/ttl", s.ready(s.ttl))
	mux.HandleFunc("GET /v1/c", s.ready(s.list))
	mux.HandleFunc("DELETE /v1/c/{coll}/indexes/{field}", s.ready(s.dropIndex))
	mux.HandleFunc("GET /v1/c/{coll}", s.readyOr(s.getCollection, s.listPattern))
	mux.HandleFunc("GET /v1/c/{coll}/stats", s.ready(s.stats))
	mux.HandleFunc("POST /v1/c/{coll}/compact", s.ready(s.compact))
	mux.HandleFunc("POST /v1/c/{coll}/truncate", s.ready(s.truncate))
	mux.HandleFunc("DELETE /v1/c/{coll}", s.ready(s.drop))
	mux.HandleFunc("GET /v1/c/{coll}/quarantine", s.ready(s.quarantine))
	mux.HandleFunc("GET /v1/stats", s.ready(s.globalStats))
	mux.HandleFunc("GET /v1/size", s.ready(s.size))
	mux.HandleFunc("PUT /v1/blobs", s.ready(s.putBlob))
	mux.HandleFunc("GET /v1/blobs/{ref}", s.ready(s.getBlob))
	mux.HandleFunc("POST /v1/backup", s.ready(s.backup))
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /metrics", s.metricsPage)

	return s.count(mux)
}

type handler func(http.ResponseWriter, *http.Request, *engine.DB)

// ready gives fn the engine, or answers 503 while recovery runs. A
// collection pattern (logs-*) in the path is pattern_read_only.
func (s *Server) ready(fn handler) http.HandlerFunc {
	return s.readyOr(fn, nil)
}

// readyOr is ready, but a collection pattern in the path goes to many.
func (s *Server) readyOr(one, many handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		db := s.db.Load()

		if db == nil {
			writeError(w, &apiError{status: http.StatusServiceUnavailable, code: "not_ready", msg: "ndb is recovering"})

			return
		}

		fn := one

		if engine.IsPattern(r.PathValue("coll")) {
			fn = many
		}

		if fn == nil {
			writeError(w, &apiError{status: http.StatusBadRequest, code: "pattern_read_only", msg: "a collection pattern only reads: find, aggregate, GET"})

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
