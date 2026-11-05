package collection_test

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/query"
)

// benchRows is the collection size. The exit criteria say 1M; set
// NDB_BENCH_ROWS=1000000 on a quiet machine.
func benchRows() int {
	if n, err := strconv.Atoi(os.Getenv("NDB_BENCH_ROWS")); err == nil {
		return n
	}

	return 100_000
}

func benchCollection(b *testing.B) *collection.Collection {
	b.Helper()

	n := benchRows()
	def := catalog.Def{Name: "posts", ID: "post_id", Indexes: []string{"ref"}, Durability: catalog.Relaxed}

	c, err := collection.Open(collection.Config{Dir: b.TempDir(), FS: fs.OS{}, Def: def})
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { c.Close() })

	for start := 0; start < n; start += 2000 {
		docs := make([][]byte, 0, 2000)

		for i := start; i < min(start+2000, n); i++ {
			docs = append(docs, fmt.Appendf(nil, `{"post_id":"c%d","ref":"r%d","n":%d}`, i, i/100, i))
		}

		if _, err := c.Insert(ctx, docs...); err != nil {
			b.Fatal(err)
		}
	}

	for {
		ready := true

		for _, ix := range c.Indexes() {
			ready = ready && ix.State == "ready"
		}

		if ready {
			return c
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func p99(b *testing.B, lat []time.Duration) {
	slices.Sort(lat)
	b.ReportMetric(float64(lat[len(lat)*99/100].Microseconds()), "p99-µs")
}

func BenchmarkFindByID(b *testing.B) {
	c := benchCollection(b)
	n := benchRows()
	lat := make([]time.Duration, b.N)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		start := time.Now()
		c.Find("c" + strconv.Itoa(i*7919%n))
		lat[i] = time.Since(start)
	}

	p99(b, lat)
}

func BenchmarkQueryByIDWhere(b *testing.B) {
	c := benchCollection(b)
	n := benchRows()
	lat := make([]time.Duration, b.N)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		q, _ := query.Parse(fmt.Appendf(nil, `{"where":{"post_id":"c%d"}}`, i*7919%n), query.Options{})
		start := time.Now()

		if _, err := c.Query(ctx, q); err != nil {
			b.Fatal(err)
		}

		lat[i] = time.Since(start)
	}

	p99(b, lat)
}

func BenchmarkQueryIndexed100(b *testing.B) {
	c := benchCollection(b)
	groups := benchRows() / 100
	lat := make([]time.Duration, b.N)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		q, _ := query.Parse(fmt.Appendf(nil, `{"where":{"ref":"r%d"}}`, i*31%groups), query.Options{})
		start := time.Now()

		p, err := c.Query(ctx, q)
		if err != nil || len(p.Rows) != 100 {
			b.Fatal(err, len(p.Rows))
		}

		lat[i] = time.Since(start)
	}

	p99(b, lat)
}
