package metrics_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/metrics"
)

func TestHistogramText(t *testing.T) {
	var h metrics.Histogram

	h.Observe(200 * time.Microsecond)
	h.Observe(3 * time.Millisecond)
	h.Observe(20 * time.Second)

	var sb strings.Builder

	h.Write(&sb, "ndb_x_seconds", `collection="a"`)

	out := sb.String()

	for _, want := range []string{
		`ndb_x_seconds_bucket{collection="a",le="0.0005"} 1`,
		`ndb_x_seconds_bucket{collection="a",le="0.005"} 2`,
		`ndb_x_seconds_bucket{collection="a",le="10"} 2`,
		`ndb_x_seconds_bucket{collection="a",le="+Inf"} 3`,
		`ndb_x_seconds_count{collection="a"} 3`,
		`ndb_x_seconds_sum{collection="a"} 20.0032`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestHistogramWithoutLabels(t *testing.T) {
	var h metrics.Histogram

	h.Observe(time.Millisecond)

	var sb strings.Builder

	h.Write(&sb, "ndb_y_seconds", "")

	if !strings.Contains(sb.String(), "ndb_y_seconds_bucket{le=\"+Inf\"} 1\n") || !strings.Contains(sb.String(), "ndb_y_seconds_count 1\n") {
		t.Fatalf("out:\n%s", sb.String())
	}
}

func TestHistogramConcurrent(t *testing.T) {
	var (
		h  metrics.Histogram
		wg sync.WaitGroup
	)

	for range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 1000 {
				h.Observe(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	var sb strings.Builder

	h.Write(&sb, "z", "")

	if !strings.Contains(sb.String(), "z_count 4000\n") {
		t.Fatalf("out:\n%s", sb.String())
	}
}

func TestWriterObserver(t *testing.T) {
	var w metrics.Writer

	w.Committed(3, time.Millisecond)
	w.Fsynced(2 * time.Millisecond)

	if w.Records.Load() != 3 {
		t.Fatalf("records = %d", w.Records.Load())
	}

	var sb strings.Builder

	w.Commit.Write(&sb, "c", "")
	w.Fsync.Write(&sb, "f", "")

	if !strings.Contains(sb.String(), "c_count 1\n") || !strings.Contains(sb.String(), "f_count 1\n") {
		t.Fatalf("out:\n%s", sb.String())
	}
}
