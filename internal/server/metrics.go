package server

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

// metrics are plain counters, exported in the Prometheus text format.
type metrics struct {
	requests atomic.Int64
	errors   atomic.Int64
	canceled atomic.Int64
	rows     atomic.Int64
	writes   atomic.Int64
}

// count wraps every route: it counts requests, errors and canceled queries.
func (s *Server) count(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(sw, r)

		s.metrics.requests.Add(1)

		switch {
		case sw.status == StatusClientClosed:
			s.metrics.canceled.Add(1)
		case sw.status >= 400:
			s.metrics.errors.Add(1)
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the real writer (flush, deadlines).
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) metricsPage(w http.ResponseWriter, _ *http.Request) {
	var ready, collections int

	if db := s.db.Load(); db != nil {
		ready = 1
		collections = len(db.Definitions())
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	fmt.Fprintf(w, "# TYPE ndb_requests_total counter\nndb_requests_total %d\n", s.metrics.requests.Load())
	fmt.Fprintf(w, "# TYPE ndb_request_errors_total counter\nndb_request_errors_total %d\n", s.metrics.errors.Load())
	fmt.Fprintf(w, "# TYPE ndb_requests_canceled_total counter\nndb_requests_canceled_total %d\n", s.metrics.canceled.Load())
	fmt.Fprintf(w, "# TYPE ndb_rows_returned_total counter\nndb_rows_returned_total %d\n", s.metrics.rows.Load())
	fmt.Fprintf(w, "# TYPE ndb_writes_total counter\nndb_writes_total %d\n", s.metrics.writes.Load())
	fmt.Fprintf(w, "# TYPE ndb_collections gauge\nndb_collections %d\n", collections)
	fmt.Fprintf(w, "# TYPE ndb_ready gauge\nndb_ready %d\n", ready)
}
