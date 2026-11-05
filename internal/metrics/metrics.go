// Package metrics has the small pieces of the Prometheus text format that
// ndb needs: histograms built on atomics, and the observer of a WAL writer.
// It imports nothing from ndb, so any package can use it. There is no client
// library (plan §E12.4).
package metrics

import (
	"fmt"
	"io"
	"strconv"
	"sync/atomic"
	"time"
)

// bounds are the upper edges of the histogram buckets, in seconds. They
// cover a fast fsync (100 µs) to a slow query (10 s).
var bounds = [...]float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5, 10}

// Histogram counts durations. The zero value is ready; Observe is one atomic
// add per counter and never blocks.
type Histogram struct {
	buckets [len(bounds) + 1]atomic.Uint64
	count   atomic.Uint64
	sumNs   atomic.Int64
}

func (h *Histogram) Observe(d time.Duration) {
	sec := d.Seconds()
	i := 0

	for i < len(bounds) && sec > bounds[i] {
		i++
	}

	h.buckets[i].Add(1)
	h.count.Add(1)
	h.sumNs.Add(int64(d))
}

// Write prints the histogram as name_bucket, name_sum and name_count. labels
// is the label list without braces, e.g. `collection="calls"`, or empty.
func (h *Histogram) Write(w io.Writer, name, labels string) {
	var cum uint64

	for i, b := range bounds {
		cum += h.buckets[i].Load()

		fmt.Fprintf(w, "%s_bucket{%sle=\"%s\"} %d\n", name, prefix(labels), strconv.FormatFloat(b, 'g', -1, 64), cum)
	}

	cum += h.buckets[len(bounds)].Load()

	fmt.Fprintf(w, "%s_bucket{%sle=\"+Inf\"} %d\n", name, prefix(labels), cum)
	fmt.Fprintf(w, "%s_sum%s %s\n", name, braces(labels), strconv.FormatFloat(float64(h.sumNs.Load())/1e9, 'g', -1, 64))
	fmt.Fprintf(w, "%s_count%s %d\n", name, braces(labels), h.count.Load())
}

func prefix(labels string) string {
	if labels == "" {
		return ""
	}

	return labels + ","
}

func braces(labels string) string {
	if labels == "" {
		return ""
	}

	return "{" + labels + "}"
}

// Writer is the observer of one collection WAL writer: write rate, commit
// latency and fsync latency.
type Writer struct {
	// Records counts the WAL records written (one per commit).
	Records atomic.Int64

	// Commit is the time from the start of a batch to its last ack.
	Commit Histogram

	// Fsync is the time of one WAL fsync.
	Fsync Histogram
}

// Committed is called after a batch is durable and acked.
func (w *Writer) Committed(records int, d time.Duration) {
	w.Records.Add(int64(records))
	w.Commit.Observe(d)
}

// Fsynced is called after every WAL fsync.
func (w *Writer) Fsynced(d time.Duration) {
	w.Fsync.Observe(d)
}
