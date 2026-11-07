package collection_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

// smallLine makes one WAL record hold about 100 tombstones, so a many write
// of a few thousand documents needs many records.
const smallLine = 4 << 10

func openBatched(t *testing.T, root string, seal bool) *collection.Collection {
	t.Helper()

	def := catalog.Def{Name: "calls", Indexes: []string{"s", "day"}, MaxLineSize: smallLine}
	cfg := collection.Config{Dir: filepath.Join(root, "wal"), FS: fs.OS{}, Def: def, SealAge: time.Hour}

	if seal {
		cfg.Data = root
	}

	c, err := collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	waitIndexes(t, c)

	return c
}

func waitIndexes(t *testing.T, c *collection.Collection) {
	t.Helper()

	for range 500 {
		ready := true

		for _, ix := range c.Indexes() {
			ready = ready && ix.State == "ready"
		}

		if ready {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("indexes not ready")
}

// fillBatched inserts n documents: s is "x" for even i and "y" for odd i,
// day is i % 10, and city is "c" + i % 3.
func fillBatched(t *testing.T, c *collection.Collection, n int) {
	t.Helper()

	for start := 0; start < n; start += 20 {
		var docs [][]byte

		for i := start; i < min(start+20, n); i++ {
			docs = append(docs, fmt.Appendf(nil, `{"_id":"d%05d","s":"%s","day":%d,"city":"c%d","n":0}`, i, map[bool]string{true: "x", false: "y"}[i%2 == 0], i%10, i%3))
		}

		if _, err := c.Insert(ctx, docs...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeleteManyOverTheLineLimit(t *testing.T) {
	for _, seal := range []bool{false, true} {
		t.Run(fmt.Sprintf("seal=%v", seal), func(t *testing.T) {
			root := t.TempDir()
			c := openBatched(t, root, seal)

			fillBatched(t, c, 4000)

			if seal {
				mustSeal(t, c)
			}

			n, err := c.Delete(ctx, []byte(`{"s":"x"}`), true)
			if err != nil || n != 2000 || c.Len() != 2000 {
				t.Fatalf("index only: n=%d err=%v len=%d", n, err, c.Len())
			}

			n, err = c.Delete(ctx, []byte(`{"day":{"$in":[1,3]},"city":"c1"}`), true)
			if err != nil || n != 267 || c.Len() != 1733 {
				t.Fatalf("with a read: n=%d err=%v len=%d", n, err, c.Len())
			}

			closeOnce(c)

			c = openBatched(t, root, seal)

			if c.Len() != 1733 {
				t.Fatalf("after reopen: len=%d, want 1733", c.Len())
			}

			if _, ok := c.Find("d00000"); ok {
				t.Fatal("d00000 is back after reopen")
			}

			if _, ok := c.Find("d00001"); ok {
				t.Fatal("d00001 (day 1, city c1) is back after reopen")
			}

			if _, ok := c.Find("d00005"); !ok {
				t.Fatal("d00005 (day 5) is gone")
			}
		})
	}
}

func TestUpdateManyOverTheLineLimitWritesEachDocumentOnce(t *testing.T) {
	for _, seal := range []bool{false, true} {
		t.Run(fmt.Sprintf("seal=%v", seal), func(t *testing.T) {
			c := openBatched(t, t.TempDir(), seal)

			fillBatched(t, c, 3000)

			if seal {
				mustSeal(t, c)
			}

			res := upd(t, c, `{"s":"y","$inc":{"n":1}}`, collection.UpdateOpts{Many: true})

			if res.Matched != 1500 || res.Modified != 1500 {
				t.Fatalf("%+v", res)
			}

			for i := range 3000 {
				want := float64(i % 2)

				if got := docOf(t, c, fmt.Sprintf("d%05d", i))["n"]; got != want {
					t.Fatalf("d%05d: n=%v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestUpdateManyMovesDocumentsOutOfTheIndexKey(t *testing.T) {
	c := openBatched(t, t.TempDir(), false)

	fillBatched(t, c, 3000)

	res := upd(t, c, `{"s":"x","$set":{"s":"z"},"$inc":{"n":1}}`, collection.UpdateOpts{Many: true})

	if res.Matched != 1500 || res.Modified != 1500 {
		t.Fatalf("%+v", res)
	}

	n, err := c.Delete(ctx, []byte(`{"s":"z"}`), true)
	if err != nil || n != 1500 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestUpsertManyOverTheLineLimit(t *testing.T) {
	c := openBatched(t, t.TempDir(), false)

	fillBatched(t, c, 1000)

	res := upd(t, c, `{"s":"none","$set":{"k":1}}`, collection.UpdateOpts{Many: true, Upsert: true})

	if res.Matched != 0 || res.Modified != 1 || c.Len() != 1001 {
		t.Fatalf("%+v len=%d", res, c.Len())
	}
}

// A document that changed after the delete listed its candidates must be
// read again: here a write in the same writer batch moves it out of the
// filter before the delete runs.
func TestDeleteManySeesAPendingChange(t *testing.T) {
	c := openBatched(t, t.TempDir(), false)

	fillBatched(t, c, 200)

	for range 20 {
		done := make(chan error, 1)

		go func() {
			_, err := c.Update(ctx, []byte(`{"_id":"d00000","$set":{"s":"keep"}}`), collection.UpdateOpts{})
			done <- err
		}()

		if _, err := c.Delete(ctx, []byte(`{"s":"x"}`), true); err != nil {
			t.Fatal(err)
		}

		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if _, ok := c.Find("d00000"); ok {
			if docOf(t, c, "d00000")["s"] != "keep" {
				t.Fatal("d00000 survived with s=x")
			}
		}

		c.Insert(ctx, doc(`{"_id":"d00000x","s":"x","day":0}`))
		c.Delete(ctx, []byte(`{"_id":"d00000x"}`), false)
	}
}
