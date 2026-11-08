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

	// version is what /_stats reports; started is when New ran.
	version string
	started time.Time

	// tokens are the bearer tokens of the non-unix requests; none = open.
	tokens []Token
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
// "Authorization: Bearer <token>", with every right. Call it before Serve.
// Empty = no token.
func (s *Server) SetToken(token string) {
	if token != "" {
		s.tokens = append(s.tokens, Token{Name: "token-file", Secret: token, Full: true})
	}
}

// SetTokens adds the scoped tokens of ndb.yml. With any token, a non-unix
// request must carry one. Call it before Serve.
func (s *Server) SetTokens(tokens []Token) {
	s.tokens = append(s.tokens, tokens...)
}

// SetVersion is the daemon version that GET /_stats reports.
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
	rt := &router{
		system: map[string][]route{
			"_health":      {{"GET", s.health}},
			"_metrics":     {{"GET", s.metricsPage}},
			"_collections": {{"GET", s.ready(s.list)}},
			"_stats":       {{"GET", s.ready(s.globalStats)}},
			"_size":        {{"GET", s.ready(s.size)}},
			"_backup":      {{"POST", s.ready(s.backup)}},
			"_restore":     {{"POST", s.ready(s.restoreAny)}},
			"_pitr":        {{"GET", s.ready(s.pitrAll)}},
			"_blobs":       {{"POST", s.ready(s.putBlob)}},
			"_blobs/{}":    {{"GET", s.ready(s.getBlob)}},
		},
		coll: map[string][]route{
			"": {
				{"PUT", s.ready(s.ensure)},
				{"GET", s.readyOr(s.getCollection, s.listPattern)},
				{"DELETE", s.ready(s.drop)},
			},
			"_truncate":        {{"POST", s.ready(s.truncate)}},
			"_indexes/{}":      {{"PUT", s.ready(s.ensureIndex)}, {"DELETE", s.ready(s.dropIndex)}},
			"_stats":           {{"GET", s.ready(s.stats)}},
			"_compact":         {{"POST", s.ready(s.compact)}},
			"_rename":          {{"POST", s.ready(s.rename)}},
			"_restore":         {{"POST", s.ready(s.restore)}},
			"_pitr":            {{"GET", s.ready(s.pitr)}},
			"_quarantine":      {{"GET", s.ready(s.quarantine)}},
			"_doc":             {{"POST", s.ready(s.insertOne)}},
			"_bulk":            {{"POST", s.ready(s.bulk)}},
			"_doc/{}":          {{"GET", s.ready(s.get)}, {"DELETE", s.ready(s.removeID)}},
			"_update/{}":       {{"POST", s.ready(s.updateID)}},
			"_expire/{}":       {{"POST", s.ready(s.expire)}},
			"_persist/{}":      {{"POST", s.ready(s.persist)}},
			"_ttl/{}":          {{"GET", s.ready(s.ttl)}},
			"_search":          {{"POST", s.readyOr(s.find, s.findPattern)}},
			"_count":           {{"POST", s.readyOr(s.countDocs, s.countPattern)}},
			"_aggregate":       {{"POST", s.readyOr(s.aggregate, s.aggregatePattern)}},
			"_update_by_query": {{"POST", s.ready(s.update)}},
			"_delete_by_query": {{"POST", s.ready(s.remove)}},
			"_watch":           {{"POST", s.ready(s.watch)}},
			"_ack":             {{"POST", s.ready(s.ack)}},
		},
	}

	return s.count(rt)
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
		} else if s.partitioned(w, r, db) {
			return
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
