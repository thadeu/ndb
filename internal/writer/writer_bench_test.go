package writer_test

import (
	"context"
	"testing"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/writer"
)

// BenchmarkStrictCommitParallel measures strict commits from concurrent
// callers (group commit). Run light: -cpu=2 -benchtime=2000x.
func BenchmarkStrictCommitParallel(b *testing.B) {
	w, err := writer.Open(writer.Config{Dir: b.TempDir(), FS: fs.OS{}})
	if err != nil {
		b.Fatal(err)
	}

	defer w.Close()

	b.SetParallelism(32)
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, _, err := w.Commit(context.Background(), put("id")); err != nil {
				b.Error(err)

				return
			}
		}
	})

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "commits/s")
}
