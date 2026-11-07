package collection_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/gen"
)

// benchScanSize is the generated data. The light benchmarks use 8 MB, in
// four segments; the 10 GB target is not run here.
const benchScanSize = 8 << 20

// benchScanCollection seals generated documents into four segments, with no
// index, so every find runs the scan.
func benchScanCollection(b *testing.B) (*collection.Collection, time.Time) {
	b.Helper()

	var buf bytes.Buffer

	if err := gen.Write(&buf, gen.Options{Seed: 1, Bytes: benchScanSize, TypeConflicts: 0.05}); err != nil {
		b.Fatal(err)
	}

	lines := bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n"))
	root := b.TempDir()

	c, err := collection.Open(collection.Config{
		Dir:  filepath.Join(root, "wal"),
		Data: root,
		FS:   fs.OS{},
		Def:  catalog.Def{Name: "calls", Durability: catalog.Relaxed},
	})
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { c.Close() })

	per := len(lines)/4 + 1

	for start := 0; start < len(lines); start += per {
		insertChunks(b, c, lines[start:min(start+per, len(lines))])

		if err := c.Seal(); err != nil {
			b.Fatal(err)
		}
	}

	// ts grows with the document number, so each segment has its own range.
	cut := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(len(lines)*3/4) * time.Second)

	return c, cut
}

// insertChunks inserts lines 500 at a time: one transaction is one WAL line,
// and it must stay under max_line_size.
func insertChunks(b *testing.B, c *collection.Collection, lines [][]byte) {
	b.Helper()

	for start := 0; start < len(lines); start += 500 {
		if _, err := c.Insert(context.Background(), lines[start:min(start+500, len(lines))]...); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkScanPruned finds ts >= cut, a range that zone maps prune for the
// first three segments and most of the fourth.
func BenchmarkScanPruned(b *testing.B) {
	c, cut := benchScanCollection(b)
	body := fmt.Sprintf(`{"query":{"ts":{"$gte":%q}}}`, cut.Format(time.RFC3339))

	benchFind(b, c, body)
}

// BenchmarkScanUnpruned finds the same rows through $or, which the planner
// does not prune, so every row group is read.
func BenchmarkScanUnpruned(b *testing.B) {
	c, cut := benchScanCollection(b)
	body := fmt.Sprintf(`{"query":{"$or":[{"ts":{"$gte":%q}},{"_id":"none"}]}}`, cut.Format(time.RFC3339))

	benchFind(b, c, body)
}

func benchFind(b *testing.B, c *collection.Collection, body string) {
	b.Helper()

	q, err := c.ParseQuery([]byte(body))
	if err != nil {
		b.Fatal(err)
	}

	first, err := c.Query(context.Background(), q)
	if err != nil {
		b.Fatal(err)
	}

	q.Explain = true

	page, err := c.Query(context.Background(), q)
	if err != nil {
		b.Fatal(err)
	}

	ex := page.Explain

	b.ReportMetric(float64(ex.RowsRead), "rows_read")
	b.ReportMetric(float64(ex.RowsMatched), "rows_matched")
	b.ReportMetric(float64(ex.SegmentsSkipped), "segments_skipped")
	b.ReportMetric(float64(ex.RowGroupsSkipped), "row_groups_skipped")
	b.ReportMetric(float64(ex.RawFetched), "raw_fetched")

	b.ResetTimer()

	for range b.N {
		p, err := c.Query(context.Background(), q)
		if err != nil {
			b.Fatal(err)
		}

		if len(p.Rows) != len(first.Rows) {
			b.Fatalf("rows %d, want %d", len(p.Rows), len(first.Rows))
		}
	}
}
