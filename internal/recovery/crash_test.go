package recovery_test

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/crashtest"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/recovery"
	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

var iterations = flag.Int("crash.iterations", 200, "kills per crash test")

func TestMain(m *testing.M) {
	crashtest.Children(map[string]func(){"wal-writer": walWriterChild})
	os.Exit(m.Run())
}

// walWriterChild recovers the WAL, then commits from 8 goroutines forever,
// acking each tx after Commit returns (strict = durable).
func walWriterChild() {
	dir := os.Getenv("WAL_DIR")

	res, err := recovery.Replay(recovery.Config{FS: fs.OS{}, Dir: dir, MaxLine: 1 << 20}, func(wal.Record) {})
	if err != nil {
		panic(err)
	}

	w, err := writer.Open(writer.Config{Dir: dir, FS: fs.OS{}, StartTx: res.LastTx, SegmentSize: 4096})
	if err != nil {
		panic(err)
	}

	doc := json.RawMessage(`{"payload":"some bytes to fill the segment"}`)

	for range 8 {
		go func() {
			for {
				tx, _, err := w.Commit(context.Background(), []wal.Op{{Kind: wal.Put, ID: "k", V: 1, Doc: doc}})
				if err != nil {
					panic(err)
				}

				crashtest.Ack(int64(tx))
			}
		}()
	}

	select {}
}

func TestWriterSurvivesKills(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	dir := t.TempDir()

	crashtest.Run(t, crashtest.Config{
		Child:      "wal-writer",
		Env:        []string{"WAL_DIR=" + dir},
		Iterations: *iterations,
		MaxDelay:   30 * time.Millisecond,
		Check: func(t *testing.T, acked int64) {
			res, err := recovery.Replay(recovery.Config{FS: fs.OS{}, Dir: dir, MaxLine: 1 << 20}, func(wal.Record) {})
			if err != nil {
				t.Errorf("replay: %v", err)

				return
			}

			if int64(res.LastTx) < acked {
				t.Errorf("acked tx %d but WAL ends at tx %d", acked, res.LastTx)
			}
		},
	})

	segs, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))

	t.Logf("%d segments after the run", len(segs))
}
