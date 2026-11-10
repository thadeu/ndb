package collection_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/crashtest"
	"github.com/thadeu/ndb/internal/fs"
)

var sealCrashDef = sealDef

// sealerChild inserts ids in order from the first missing one, seals every
// 8 inserts, and acks each insert once it is durable. It runs with a small
// size limit, so the background sealer also works while it is killed.
func sealerChild() {
	dir := os.Getenv("SEAL_DIR")

	c, err := collection.Open(collection.Config{
		Dir:       filepath.Join(dir, "wal"),
		Data:      dir,
		FS:        fs.OS{},
		Def:       sealCrashDef,
		SealBytes: 1 << 10,
		SealTick:  2 * time.Millisecond,
	})
	if err != nil {
		panic(err)
	}

	n := 0

	for {
		if _, ok := c.Find(sealID(n)); !ok {
			break
		}

		n++
	}

	for ; ; n++ {
		if _, err := c.Insert(context.Background(), doc(fmt.Sprintf(`{"_id":"s%06d","n":%d}`, n, n))); err != nil {
			panic(err)
		}

		crashtest.Ack(int64(n + 1))

		if n%8 == 7 {
			if err := c.Seal(); err != nil {
				panic(err)
			}
		}
	}
}

func sealID(n int) string {
	return fmt.Sprintf("s%06d", n)
}

// TestSealingKillsLoseNoAckedRow kills a writer that seals while it writes.
// After each kill, every acked insert must be there, and the rows must be a
// prefix of the insert order with no duplicate.
func TestSealingKillsLoseNoAckedRow(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	dir := t.TempDir()
	segmentsSeen := 0

	crashtest.Run(t, crashtest.Config{
		Child:      "sealer",
		Env:        []string{"SEAL_DIR=" + dir},
		Iterations: *iterations,
		MaxDelay:   100 * time.Millisecond,
		Check: func(t *testing.T, acked int64) {
			c, err := collection.Open(collection.Config{Dir: filepath.Join(dir, "wal"), Data: dir, FS: fs.OS{}, Def: sealCrashDef})
			if err != nil {
				t.Errorf("reopen: %v", err)

				return
			}

			defer c.Close()

			segmentsSeen = max(segmentsSeen, c.Segments())

			checkPrefix(t, c, acked)
		},
	})

	if segmentsSeen == 0 {
		t.Fatal("no segment was ever sealed; the test did not cover sealing")
	}
}

func checkPrefix(t *testing.T, c *collection.Collection, acked int64) {
	t.Helper()

	q, _ := c.ParseQuery([]byte(`{}`))

	page, err := c.Query(context.Background(), q)
	if err != nil {
		t.Errorf("query: %v", err)

		return
	}

	if int64(len(page.Rows)) < acked {
		t.Errorf("rows = %d, acked = %d", len(page.Rows), acked)
	}

	ids := map[string]bool{}

	for _, r := range page.Rows {
		var m struct {
			ID string `json:"_id"`
		}

		if err := json.Unmarshal(r, &m); err != nil {
			t.Errorf("row: %v", err)

			return
		}

		if ids[m.ID] {
			t.Errorf("duplicate row %s", m.ID)

			return
		}

		ids[m.ID] = true
	}

	for i := range len(page.Rows) {
		if id := sealID(i); !ids[id] {
			t.Errorf("row %s missing: rows are not a prefix of the inserts", id)

			return
		}
	}
}
