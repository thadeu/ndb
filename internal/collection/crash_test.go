package collection_test

import (
	"flag"
	"os"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/crashtest"
	"github.com/thadeu/ndb/internal/fs"
)

var iterations = flag.Int("crash.iterations", 200, "kills per crash test")

func TestMain(m *testing.M) {
	crashtest.Children(map[string]func(){"counter": counterChild, "sealer": sealerChild})
	os.Exit(m.Run())
}

var counterDef = catalog.Def{Name: "counters"}

// counterChild bumps one doc forever and acks each version it got back.
func counterChild() {
	c, err := collection.Open(collection.Config{Dir: os.Getenv("COL_DIR"), FS: fs.OS{}, Def: counterDef})
	if err != nil {
		panic(err)
	}

	if _, ok := c.Find("c"); !ok {
		if _, err := c.Insert(ctx, doc(`{"_id":"c"}`)); err != nil {
			panic(err)
		}
	}

	for {
		v, err := c.UpdateByID(ctx, "c", collection.Change{Set: map[string]any{"t": time.Now().UnixNano()}}, 0)
		if err != nil {
			panic(err)
		}

		crashtest.Ack(int64(v))
	}
}

func TestAckedUpdatesSurviveKills(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	dir := t.TempDir()

	crashtest.Run(t, crashtest.Config{
		Child:      "counter",
		Env:        []string{"COL_DIR=" + dir},
		Iterations: *iterations,
		MaxDelay:   20 * time.Millisecond,
		Check: func(t *testing.T, acked int64) {
			c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: counterDef})
			if err != nil {
				t.Errorf("reopen: %v", err)

				return
			}

			defer c.Close()

			got, ok := c.Find("c")

			if acked > 0 && (!ok || int64(got.V) < acked) {
				t.Errorf("acked version %d, found %d (ok=%v)", acked, got.V, ok)
			}
		},
	})
}
