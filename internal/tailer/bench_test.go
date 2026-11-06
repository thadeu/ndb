package tailer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/gen"
	"github.com/thadeu/ndb/internal/tailer"
)

// BenchmarkIngest reads a generated NDJSON file into the memtable. Run it
// small: -benchtime=1x -cpu=2.
func BenchmarkIngest(b *testing.B) {
	data := b.TempDir()

	f, err := os.Create(filepath.Join(data, "a.ndjson"))
	if err != nil {
		b.Fatal(err)
	}

	const size = 50 << 20

	if err := gen.Write(f, gen.Options{Seed: 1, Bytes: size}); err != nil {
		b.Fatal(err)
	}

	f.Close()

	def := catalog.Def{Name: "calls", Write: catalog.Direct, Path: filepath.Join(data, "*.ndjson")}

	b.SetBytes(size)
	b.ResetTimer()

	for range b.N {
		b.StopTimer()

		d := collection.NewDirect(collection.DirectConfig{Def: def})
		tl, err := tailer.Open(tailer.Config{Dir: b.TempDir(), Def: def, Sink: d})
		if err != nil {
			b.Fatal(err)
		}

		b.StartTimer()

		if err := tl.Sync(context.Background()); err != nil {
			b.Fatal(err)
		}

		b.StopTimer()
		b.ReportMetric(float64(d.Len()), "docs")
		b.ReportMetric(float64(tl.Quarantined()), "quarantined")
		tl.Close()
	}
}
