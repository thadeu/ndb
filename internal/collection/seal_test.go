package collection_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

var sealDef = catalog.Def{Name: "calls", ID: "id", Indexes: []string{"status"}}

// openSealed opens an engine-mode collection whose data (segments and meta)
// lives under root. The WAL is root/wal.
func openSealed(t *testing.T, root string, cfg collection.Config) *collection.Collection {
	t.Helper()

	cfg.Dir = filepath.Join(root, "wal")
	cfg.Data = root
	cfg.FS = fsOf(cfg.FS)
	cfg.Def = sealDef

	if cfg.CompactMin == 0 {
		cfg.CompactMin = 1 << 20
	}

	c, err := collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	return c
}

func fsOf(f fs.FS) fs.FS {
	if f == nil {
		return fs.OS{}
	}

	return f
}

func openMem(t *testing.T) *collection.Collection {
	t.Helper()

	c, err := collection.Open(collection.Config{Dir: t.TempDir(), FS: fs.OS{}, Def: sealDef})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	return c
}

var closed sync.Map

func closeOnce(c *collection.Collection) {
	if _, done := closed.LoadOrStore(c, true); !done {
		c.Close()
	}
}

func reopen(t *testing.T, c *collection.Collection, root string, cfg collection.Config) *collection.Collection {
	t.Helper()

	closeOnce(c)

	return openSealed(t, root, cfg)
}

func insertN(t *testing.T, c *collection.Collection, from, to int) {
	t.Helper()

	for i := from; i < to; i++ {
		d := fmt.Sprintf(`{"id":"c%04d","status":"s%d","n":%d}`, i, i%4, i)

		if _, err := c.Insert(context.Background(), doc(d)); err != nil {
			t.Fatal(err)
		}
	}
}

func update(t *testing.T, c *collection.Collection, id string, status string) {
	t.Helper()

	ch := collection.Change{Set: map[string]any{"status": status}}

	if _, err := c.Update(context.Background(), id, ch, 0); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, c *collection.Collection, id string) {
	t.Helper()

	if err := c.Delete(context.Background(), id, 0); err != nil {
		t.Fatal(err)
	}
}

var probes = []string{
	`{}`,
	`{"where":{"status":"s1"}}`,
	`{"where":{"n":{"$gte":10,"$lt":30}},"sort":{"n":-1},"limit":7,"skip":2}`,
	`{"where":{"id":{"$in":["c0001","c0100","c9999"]}}}`,
	`{"where":{"status":"s2"},"fields":["id"],"count":true}`,
}

// rowsOf returns the rows of every probe query, in order, as JSON text.
func rowsOf(t *testing.T, c *collection.Collection) []string {
	t.Helper()

	var out []string

	for _, body := range probes {
		q, err := c.ParseQuery([]byte(body))
		if err != nil {
			t.Fatal(err)
		}

		page, err := c.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}

		b, _ := json.Marshal(page.Rows)
		out = append(out, string(b))

		if page.Total != nil {
			out = append(out, fmt.Sprint(*page.Total))
		}
	}

	return out
}

func sameRows(t *testing.T, want, got []string) {
	t.Helper()

	if len(want) != len(got) {
		t.Fatalf("probe count %d, want %d", len(got), len(want))
	}

	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("probe %d differs:\nwant %s\n got %s", i, want[i], got[i])
		}
	}
}

// TestSealedRowsReadLikeMemtable runs the same writes on a memory-only
// collection and on a sealing one, with seals in between, then compares
// every probe and every id.
func TestSealedRowsReadLikeMemtable(t *testing.T) {
	mem := openMem(t)
	root := t.TempDir()
	seg := openSealed(t, root, collection.Config{})

	both := func(f func(c *collection.Collection)) {
		f(mem)
		f(seg)
	}

	both(func(c *collection.Collection) { insertN(t, c, 0, 300) })
	both(func(c *collection.Collection) {
		for i := 0; i < 300; i += 5 {
			update(t, c, fmt.Sprintf("c%04d", i), "done")
		}
	})
	both(func(c *collection.Collection) {
		for i := 0; i < 300; i += 7 {
			remove(t, c, fmt.Sprintf("c%04d", i))
		}
	})

	if err := seg.Seal(); err != nil {
		t.Fatal(err)
	}

	if seg.Segments() != 1 {
		t.Fatalf("segments = %d, want 1", seg.Segments())
	}

	sameRows(t, rowsOf(t, mem), rowsOf(t, seg))

	both(func(c *collection.Collection) { insertN(t, c, 300, 340) })
	both(func(c *collection.Collection) {
		update(t, c, "c0010", "late")
		remove(t, c, "c0011")
		remove(t, c, "c0013")
	})

	if err := seg.Seal(); err != nil {
		t.Fatal(err)
	}

	sameRows(t, rowsOf(t, mem), rowsOf(t, seg))

	for i := range 340 {
		id := fmt.Sprintf("c%04d", i)

		mdoc, mok := mem.Find(id)
		sdoc, sok := seg.Find(id)

		if mok != sok || string(mdoc.Doc) != string(sdoc.Doc) || mdoc.V != sdoc.V {
			t.Fatalf("find %s: memtable %v %s, segments %v %s", id, mok, mdoc.Doc, sok, sdoc.Doc)
		}
	}
}

func TestSealReleasesDocumentBytes(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	insertN(t, c, 0, 200)

	if got := collection.MemDocs(c); got != 200 {
		t.Fatalf("memtable docs = %d, want 200", got)
	}

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	if got := collection.MemDocs(c); got != 0 {
		t.Fatalf("memtable docs after seal = %d, want 0", got)
	}

	if _, ok := c.Find("c0042"); !ok {
		t.Fatal("sealed doc must still be found")
	}
}

func TestSealTriggeredBySize(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{SealBytes: 4 << 10, SealTick: 20 * time.Millisecond})

	insertN(t, c, 0, 200)

	waitFor(t, func() bool { return c.Segments() >= 1 })
}

func TestSealTriggeredByAge(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{SealAge: 100 * time.Millisecond, SealTick: 20 * time.Millisecond})

	insertN(t, c, 0, 3)

	waitFor(t, func() bool { return c.Segments() >= 1 })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in 5 s")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// TestCrashBeforeRenameKeepsOldState fails the rename of a seal. The data
// must read the same, no segment may be in the checkpoint, and the next
// start must remove the tmp file.
func TestCrashBeforeRenameKeepsOldState(t *testing.T) {
	root := t.TempDir()
	faulty := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpRename, At: 1})
	c := openSealed(t, root, collection.Config{FS: faulty})

	insertN(t, c, 0, 100)

	want := rowsOf(t, c)

	if err := c.Seal(); err == nil {
		t.Fatal("seal must fail on the injected rename")
	}

	sameRows(t, want, rowsOf(t, c))

	closeOnce(c)

	c2 := openSealed(t, root, collection.Config{})

	if c2.Segments() != 0 {
		t.Fatalf("segments = %d after crash, want 0", c2.Segments())
	}

	sameRows(t, want, rowsOf(t, c2))

	if left, _ := filepath.Glob(filepath.Join(root, "segments", "*.tmp")); len(left) != 0 {
		t.Fatalf("tmp files left: %v", left)
	}
}

// TestQueriesDuringSealSeeEachRowOnce seals while writes and queries run.
func TestQueriesDuringSealSeeEachRowOnce(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := range 3 {
			insertN(t, c, i*100, i*100+100)

			if err := c.Seal(); err != nil {
				t.Error(err)
			}
		}
	}()

	for range 30 {
		q, _ := c.ParseQuery([]byte(`{"count":true}`))

		page, err := c.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}

		seen := map[string]bool{}

		for _, r := range page.Rows {
			var m struct {
				ID string `json:"id"`
			}

			if err := json.Unmarshal(r, &m); err != nil {
				t.Fatal(err)
			}

			if seen[m.ID] {
				t.Fatalf("row %s returned twice", m.ID)
			}

			seen[m.ID] = true
		}
	}

	wg.Wait()

	if got := len(rowsOfAll(t, c)); got != 300 {
		t.Fatalf("rows = %d, want 300", got)
	}
}

func rowsOfAll(t *testing.T, c *collection.Collection) []json.RawMessage {
	t.Helper()

	q, _ := c.ParseQuery([]byte(`{}`))

	page, err := c.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}

	return page.Rows
}

func TestRestartAfterSealReplaysOnlyTheTail(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{})

	insertN(t, c, 0, 150)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	insertN(t, c, 150, 170)

	want := rowsOf(t, c)

	c = reopen(t, c, root, collection.Config{})

	if c.Segments() != 1 {
		t.Fatalf("segments after restart = %d, want 1", c.Segments())
	}

	sameRows(t, want, rowsOf(t, c))
}

func TestDeleteOfSealedRowSurvivesSealAndRestart(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{})

	insertN(t, c, 0, 10)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	remove(t, c, "c0003")

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	c = reopen(t, c, root, collection.Config{})

	if _, ok := c.Find("c0003"); ok {
		t.Fatal("deleted row came back after seal and restart")
	}

	if len(rowsOfAll(t, c)) != 9 {
		t.Fatalf("rows = %d, want 9", len(rowsOfAll(t, c)))
	}
}

func TestDeleteAfterSealSurvivesRestartFromWAL(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{})

	insertN(t, c, 0, 10)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	remove(t, c, "c0004")

	c = reopen(t, c, root, collection.Config{})

	if _, ok := c.Find("c0004"); ok {
		t.Fatal("delete in the WAL tail was lost")
	}
}

func TestRebuildFromWALWhenSegmentsAndMetaAreDeleted(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{})

	insertN(t, c, 0, 120)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	insertN(t, c, 120, 140)
	remove(t, c, "c0007")
	update(t, c, "c0008", "x")

	want := rowsOf(t, c)

	closeOnce(c)

	if err := os.RemoveAll(filepath.Join(root, "segments")); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(root, "meta.bolt")); err != nil {
		t.Fatal(err)
	}

	c = openSealed(t, root, collection.Config{})

	sameRows(t, want, rowsOf(t, c))
}

func TestOrphanFilesAreRemovedOnStart(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	want := rowsOf(t, c)

	closeOnce(c)

	seg := filepath.Join(root, "segments")

	orphans := []string{filepath.Join(seg, "000002.parquet.tmp"), filepath.Join(seg, "000099.parquet")}

	for _, p := range orphans {
		if err := os.WriteFile(p, []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	c = openSealed(t, root, collection.Config{})

	for _, p := range orphans {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("orphan %s not removed: %v", p, err)
		}
	}

	sameRows(t, want, rowsOf(t, c))
}

func TestIndexAfterSealAndRestartUsesSealedRows(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{})

	insertN(t, c, 0, 120)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	insertN(t, c, 120, 140)
	remove(t, c, "c0012")

	c = reopen(t, c, root, collection.Config{})

	waitFor(t, func() bool { return c.Indexes()[0].State == "ready" })

	q, _ := c.ParseQuery([]byte(`{"where":{"status":"s1"},"count":true}`))

	page, err := c.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}

	if page.Plan != "index:status" {
		t.Fatalf("plan = %q, want index:status", page.Plan)
	}

	if *page.Total != 35 {
		t.Fatalf("status s1 rows = %d, want 35", *page.Total)
	}
}

func TestUniqueKeysOfSealedRows(t *testing.T) {
	root := t.TempDir()
	def := catalog.Def{Name: "users", ID: "id", Unique: []string{"email"}}

	c, err := collection.Open(collection.Config{Dir: filepath.Join(root, "wal"), Data: root, FS: fs.OS{}, Def: def})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := c.Insert(ctx, doc(`{"id":"u1","email":"a@x"}`)); err != nil {
		t.Fatal(err)
	}

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	c.Close()

	c, err = collection.Open(collection.Config{Dir: filepath.Join(root, "wal"), Data: root, FS: fs.OS{}, Def: def})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	if _, err := c.Insert(ctx, doc(`{"id":"u2","email":"a@x"}`)); code(err) != "unique_violation" {
		t.Fatalf("duplicate of a sealed unique value: %v", err)
	}

	if _, err := c.Update(ctx, "u1", collection.Change{Set: map[string]any{"email": "b@x"}}, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Insert(ctx, doc(`{"id":"u2","email":"a@x"}`)); err != nil {
		t.Fatalf("value freed by an update of a sealed row: %v", err)
	}
}

func TestReinsertAfterSealedDelete(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{})

	insertN(t, c, 0, 5)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	remove(t, c, "c0002")

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Insert(ctx, doc(`{"id":"c0002","status":"new","n":99}`)); err != nil {
		t.Fatal(err)
	}

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	c = reopen(t, c, root, collection.Config{})

	got, ok := c.Find("c0002")

	if !ok || string(got.Doc) != `{"id":"c0002","status":"new","n":99}` {
		t.Fatalf("reinserted row = %v %s", ok, got.Doc)
	}

	if len(rowsOfAll(t, c)) != 5 {
		t.Fatalf("rows = %d, want 5", len(rowsOfAll(t, c)))
	}
}
