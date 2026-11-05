package server

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/thadeu/ndb/internal/engine"
	ndbmetrics "github.com/thadeu/ndb/internal/metrics"
)

// metrics are plain counters, exported in the Prometheus text format.
type metrics struct {
	requests atomic.Int64
	errors   atomic.Int64
	canceled atomic.Int64
	rows     atomic.Int64
	writes   atomic.Int64

	// find and aggregate are the query latency by type.
	find      ndbmetrics.Histogram
	aggregate ndbmetrics.Histogram
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

	db := s.db.Load()

	if db != nil {
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

	fmt.Fprint(w, "# TYPE ndb_query_seconds histogram\n")
	s.metrics.find.Write(w, "ndb_query_seconds", `type="find"`)
	s.metrics.aggregate.Write(w, "ndb_query_seconds", `type="aggregate"`)

	if db != nil {
		writeEngineMetrics(w, db)
	}
}

// writeEngineMetrics prints the per-collection series and the blob store. They
// are read at scrape time, so the hot paths only pay for atomics.
func writeEngineMetrics(w io.Writer, db *engine.DB) {
	stats := db.AllStats()

	series := []struct {
		name, kind string
		value      func(engine.Stats) int64
	}{
		{"ndb_memtable_bytes", "gauge", func(s engine.Stats) int64 { return s.MemBytes }},
		{"ndb_segments", "gauge", func(s engine.Stats) int64 { return int64(s.Segments) }},
		{"ndb_tailer_lag_bytes", "gauge", func(s engine.Stats) int64 { return s.TailerLag }},
		{"ndb_quarantined_total", "counter", func(s engine.Stats) int64 { return s.Quarantined }},
		{"ndb_watch_subscribers", "gauge", func(s engine.Stats) int64 { return int64(s.Subscribers) }},
	}

	for _, m := range series {
		fmt.Fprintf(w, "# TYPE %s %s\n", m.name, m.kind)

		for _, st := range stats {
			fmt.Fprintf(w, "%s{collection=%q} %d\n", m.name, st.Name, m.value(st))
		}
	}

	fmt.Fprint(w, "# TYPE ndb_index_build_progress gauge\n")

	for _, st := range stats {
		for _, ix := range st.Indexes {
			fmt.Fprintf(w, "ndb_index_build_progress{collection=%q,field=%q} %g\n", st.Name, ix.Field, indexProgress(ix.State, ix.Done, ix.Total))
		}
	}

	writeWriterMetrics(w, stats)

	count, bytes := db.Blobs().Stats()

	fmt.Fprintf(w, "# TYPE ndb_blob_store_blobs gauge\nndb_blob_store_blobs %d\n", count)
	fmt.Fprintf(w, "# TYPE ndb_blob_store_bytes gauge\nndb_blob_store_bytes %d\n", bytes)
}

func writeWriterMetrics(w io.Writer, stats []engine.Stats) {
	fmt.Fprint(w, "# TYPE ndb_wal_records_total counter\n")

	for _, st := range stats {
		if st.Writer != nil {
			fmt.Fprintf(w, "ndb_wal_records_total{collection=%q} %d\n", st.Name, st.Writer.Records.Load())
		}
	}

	fmt.Fprint(w, "# TYPE ndb_commit_seconds histogram\n")

	for _, st := range stats {
		if st.Writer != nil {
			st.Writer.Commit.Write(w, "ndb_commit_seconds", fmt.Sprintf("collection=%q", st.Name))
		}
	}

	fmt.Fprint(w, "# TYPE ndb_fsync_seconds histogram\n")

	for _, st := range stats {
		if st.Writer != nil {
			st.Writer.Fsync.Write(w, "ndb_fsync_seconds", fmt.Sprintf("collection=%q", st.Name))
		}
	}
}

// indexProgress is 0..1: a ready index is 1.
func indexProgress(state string, done, total int) float64 {
	if state == "ready" || total == 0 {
		return 1
	}

	return float64(done) / float64(total)
}
