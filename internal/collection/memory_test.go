package collection_test

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/collection"
)

// TestHeapDoesNotGrowWithSealedDocs is the M16 rule: sealed documents live on
// disk, so the heap does not grow with their number. Only small per-row bits
// (live bitmaps, bloom filters) may stay in memory.
func TestHeapDoesNotGrowWithSealedDocs(t *testing.T) {
	if testing.Short() {
		t.Skip("loads 120k documents")
	}

	c := openSealed(t, t.TempDir(), collection.Config{SealBytes: 1 << 40})

	load := func(from, to int) {
		pad := strings.Repeat("x", 900)

		for start := from; start < to; start += 1000 {
			docs := make([][]byte, 0, 1000)

			for i := start; i < min(start+1000, to); i++ {
				docs = append(docs, fmt.Appendf(nil, `{"_id":"call-%07d","status":"s%d","n":%d,"pad":%q}`, i, i%4, i, pad))
			}

			if _, err := c.Insert(context.Background(), docs...); err != nil {
				t.Fatal(err)
			}
		}

		if err := c.Seal(); err != nil {
			t.Fatal(err)
		}
	}

	load(0, 20_000)
	small := heap()

	load(20_000, 120_000)
	big := heap()

	perDoc := float64(big-small) / 100_000

	t.Logf("heap %d KB → %d KB for 100k more sealed docs (%.1f B per doc)", small>>10, big>>10, perDoc)

	if perDoc > 32 {
		t.Fatalf("the heap grows %.1f B per sealed document, want ≤ 32 B", perDoc)
	}
}

func heap() int64 {
	var m runtime.MemStats

	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&m)

	return int64(m.HeapAlloc)
}

// The write buffer is one budget for every collection of a daemon: over it,
// the largest memtable is sealed, even when each one is under SealBytes.
func TestSharedWriteBufferSealsTheLargestMemtable(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	shared := collection.NewShared(64 << 10)
	cfg := collection.Config{Shared: shared, SealBytes: 1 << 40, SealTick: 10 * time.Millisecond}

	big := openSealed(t, t.TempDir(), cfg)
	small := openSealed(t, t.TempDir(), cfg)

	insertN(t, small, 0, 100)
	insertN(t, big, 0, 3000)

	waitFor(t, func() bool { return big.Segments() > 0 })

	if small.Segments() != 0 {
		t.Fatalf("the small memtable was sealed too: %d segments", small.Segments())
	}
}

// A direct collection with Data is disk first too: its sealed rows do not
// stay in the heap.
func TestDirectHeapDoesNotGrowWithSealedDocs(t *testing.T) {
	if testing.Short() {
		t.Skip("loads 120k lines")
	}

	d, err := collection.OpenDirect(collection.DirectConfig{Def: sealDef, Data: t.TempDir(), SealBytes: 1 << 40, SealAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { d.Close() })

	pad := strings.Repeat("x", 900)

	load := func(from, to int) {
		for i := from; i < to; i++ {
			line := fmt.Appendf(nil, `{"_id":"call-%07d","status":"s%d","n":%d,"pad":%q}`, i, i%4, i, pad)

			if err := d.Apply("a.ndjson", int64(i)*1000, line); err != nil {
				t.Fatal(err)
			}
		}

		if err := d.Seal(); err != nil {
			t.Fatal(err)
		}
	}

	load(0, 20_000)
	small := heap()

	load(20_000, 120_000)
	big := heap()

	perDoc := float64(big-small) / 100_000

	t.Logf("heap %d KB → %d KB for 100k more sealed lines (%.1f B per doc)", small>>10, big>>10, perDoc)

	if perDoc > 32 || d.Len() != 120_000 {
		t.Fatalf("the heap grows %.1f B per sealed document (want ≤ 32), len %d", perDoc, d.Len())
	}
}
