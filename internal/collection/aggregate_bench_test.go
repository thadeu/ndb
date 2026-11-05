package collection_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/gen"
)

const groupByStatusHour = `{"pipeline":[{"$group":{"_id":{"status":"$status","hour":{"$dateTrunc":{"date":"$ts","unit":"hour"}}},"n":{"$count":{}},"avg":{"$avg":"$duration"}}}]}`

const groupByCity = `{"pipeline":[{"$group":{"_id":"$address.city","n":{"$count":{}},"top":{"$max":"$duration"}}}]}`

// BenchmarkAggregateColumnEngine groups 8 MB in four segments: the group
// paths come from columns, with _raw only for rows whose cell cannot answer.
func BenchmarkAggregateColumnEngine(b *testing.B) {
	c, _ := benchScanCollection(b)

	benchAggregate(b, c, groupByStatusHour)
}

func BenchmarkAggregateColumnEngineCity(b *testing.B) {
	c, _ := benchScanCollection(b)

	benchAggregate(b, c, groupByCity)
}

// BenchmarkAggregateRowEngine groups the same data from the memtable.
func BenchmarkAggregateRowEngine(b *testing.B) {
	var buf bytes.Buffer

	if err := gen.Write(&buf, gen.Options{Seed: 1, Bytes: benchScanSize, TypeConflicts: 0.05}); err != nil {
		b.Fatal(err)
	}

	c, err := collection.Open(collection.Config{
		Dir: b.TempDir(),
		FS:  fs.OS{},
		Def: catalog.Def{Name: "calls", ID: "id", Durability: catalog.Relaxed},
	})
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { c.Close() })

	insertChunks(b, c, bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")))

	benchAggregate(b, c, groupByStatusHour)
}

func benchAggregate(b *testing.B, c *collection.Collection, body string) {
	b.Helper()

	p, err := c.ParseAggregate([]byte(body))
	if err != nil {
		b.Fatal(err)
	}

	p.Explain = true

	first, err := c.Aggregate(context.Background(), p)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()

	for range b.N {
		page, err := c.Aggregate(context.Background(), p)
		if err != nil {
			b.Fatal(err)
		}

		if len(page.Rows) != len(first.Rows) {
			b.Fatalf("groups %d, want %d", len(page.Rows), len(first.Rows))
		}
	}

	b.ReportMetric(float64(len(first.Rows)), "groups")
	b.ReportMetric(float64(first.Explain.RowsRead), "rows_read")
	b.ReportMetric(float64(first.Explain.RawFetched), "raw_fetched")
}
