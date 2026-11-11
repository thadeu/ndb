package collection_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

var ctx = context.Background()

// openData opens a collection that seals to segments under root, with its
// own definition (openSealed has a fixed one). The caller closes it.
func openData(t *testing.T, root string, def catalog.Def) *collection.Collection {
	t.Helper()

	if def.Name == "" {
		def.Name = "posts"
	}

	c, err := collection.Open(collection.Config{Dir: filepath.Join(root, "wal"), Data: root, FS: fs.OS{}, Def: def, CompactMin: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

func open(t *testing.T, dir string, def catalog.Def) *collection.Collection {
	t.Helper()

	if def.Name == "" {
		def.Name = "posts"
	}

	c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: def})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { c.Close() })

	return c
}

func doc(s string) json.RawMessage {
	return json.RawMessage(s)
}

func field(t *testing.T, d json.RawMessage, name string) any {
	t.Helper()

	var m map[string]any

	if err := json.Unmarshal(d, &m); err != nil {
		t.Fatal(err)
	}

	return m[name]
}

func code(err error) string {
	var ce interface{ Code() string }

	if errors.As(err, &ce) {
		return ce.Code()
	}

	return fmt.Sprint(err)
}

func TestInsertAndFind(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	res, err := c.Insert(ctx, doc(`{"_id":"p1","status":"draft"}`))
	if err != nil {
		t.Fatal(err)
	}

	if res[0].ID != "p1" || res[0].V != 1 {
		t.Fatalf("res = %+v", res)
	}

	got, ok := c.Find("p1")

	if !ok || got.V != 1 || field(t, got.Doc, "status") != "draft" {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestInsertManyIsOneTransaction(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	_, err := c.Insert(ctx, doc(`{"_id":"a"}`), doc(`{"_id":"a"}`))

	if code(err) != "duplicate_id" {
		t.Fatalf("want duplicate_id, got %v", err)
	}

	if _, ok := c.Find("a"); ok {
		t.Fatal("half of a failed transaction was applied")
	}
}

func TestInsertErrors(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1"}`))

	cases := map[string]json.RawMessage{
		"duplicate_id": doc(`{"_id":"p1"}`),
		"invalid_id":   doc(`{"_id":5}`),
		"invalid_doc":  doc(`[1,2]`),
	}

	for want, d := range cases {
		if _, err := c.Insert(ctx, d); code(err) != want {
			t.Fatalf("%s: got %v", want, err)
		}
	}
}

func TestGeneratedID(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	res, err := c.Insert(ctx, doc(`{"status":"x"}`))
	if err != nil {
		t.Fatal(err)
	}

	got, _ := c.Find(res[0].ID)

	if len(res[0].ID) != 36 || field(t, got.Doc, "_id") != res[0].ID {
		t.Fatalf("id %q, doc %s", res[0].ID, got.Doc)
	}
}

func TestUpdateSetUnset(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1","status":"draft","tmp":1,"meta":{"a":1}}`))

	v, err := c.UpdateByID(ctx, "p1", collection.Change{
		Set:   map[string]any{"status": "published", "meta.b": 2},
		Unset: []string{"tmp"},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}

	got, _ := c.Find("p1")

	if v != 2 || got.V != 2 || field(t, got.Doc, "status") != "published" || field(t, got.Doc, "tmp") != nil {
		t.Fatalf("v=%d doc=%s", v, got.Doc)
	}

	meta := field(t, got.Doc, "meta").(map[string]any)

	if meta["a"] != 1.0 || meta["b"] != 2.0 {
		t.Fatalf("meta = %v", meta)
	}
}

func TestUpdateErrors(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1"}`))

	if _, err := c.UpdateByID(ctx, "nope", collection.Change{Set: map[string]any{"a": 1}}, 0); code(err) != "not_found" {
		t.Fatalf("got %v", err)
	}

	if _, err := c.UpdateByID(ctx, "p1", collection.Change{Set: map[string]any{"a": 1}}, 9); code(err) != "not_found" {
		t.Fatalf("got %v", err)
	}

	if _, err := c.UpdateByID(ctx, "p1", collection.Change{Set: map[string]any{"_id": "p2"}}, 0); code(err) != "immutable_id" {
		t.Fatalf("got %v", err)
	}
}

func TestVersionFilterUnderConcurrency(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1","n":0}`))

	const workers = 20

	var wg sync.WaitGroup

	wins := 0
	mu := sync.Mutex{}

	for i := range workers {
		wg.Go(func() {
			_, err := c.UpdateByID(ctx, "p1", collection.Change{Set: map[string]any{"n": i + 1}}, 1)

			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		})
	}

	wg.Wait()

	got, _ := c.Find("p1")

	if wins != 1 || got.V != 2 {
		t.Fatalf("%d updates won with _v=1, final v=%d", wins, got.V)
	}
}

func TestUpdatesInOneBatchSeeEachOther(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1"}`))

	const n = 50

	var wg sync.WaitGroup

	for i := range n {
		wg.Go(func() {
			if _, err := c.UpdateByID(ctx, "p1", collection.Change{Set: map[string]any{fmt.Sprint("f", i): i}}, 0); err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	got, _ := c.Find("p1")

	var m map[string]any

	json.Unmarshal(got.Doc, &m)

	if got.V != n+1 || len(m) != n+1 {
		t.Fatalf("v=%d fields=%d: lost updates", got.V, len(m))
	}
}

func TestDelete(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1"}`))

	if n, err := c.Delete(ctx, []byte(`{"_id":"p1","_v":5}`), false); err != nil || n != 0 {
		t.Fatalf("stale version: n=%d err=%v", n, err)
	}

	if n, err := c.Delete(ctx, []byte(`{"_id":"p1","_v":1}`), false); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	if _, ok := c.Find("p1"); ok {
		t.Fatal("found after delete")
	}

	if err := c.DeleteByID(ctx, "p1"); code(err) != "not_found" {
		t.Fatalf("got %v", err)
	}

	if _, err := c.Insert(ctx, doc(`{"_id":"p1"}`)); err != nil {
		t.Fatalf("insert after delete: %v", err)
	}
}

func TestUnique(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Unique: []string{"slug"}})

	c.Insert(ctx, doc(`{"_id":"p1","slug":"hello"}`))

	if _, err := c.Insert(ctx, doc(`{"_id":"p2","slug":"hello"}`)); code(err) != "unique_violation" {
		t.Fatalf("insert: got %v", err)
	}

	c.Insert(ctx, doc(`{"_id":"p2","slug":"other"}`))

	if _, err := c.UpdateByID(ctx, "p2", collection.Change{Set: map[string]any{"slug": "hello"}}, 0); code(err) != "unique_violation" {
		t.Fatalf("update: got %v", err)
	}

	c.DeleteByID(ctx, "p1")

	if _, err := c.UpdateByID(ctx, "p2", collection.Change{Set: map[string]any{"slug": "hello"}}, 0); err != nil {
		t.Fatalf("slug free after delete: %v", err)
	}

	if _, err := c.Insert(ctx, doc(`{"_id":"p3"}`), doc(`{"_id":"p4"}`)); err != nil {
		t.Fatalf("missing unique field must not collide: %v", err)
	}
}

func TestUniqueInsideOneTransaction(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Unique: []string{"slug"}})

	_, err := c.Insert(ctx, doc(`{"_id":"a","slug":"x"}`), doc(`{"_id":"b","slug":"x"}`))

	if code(err) != "unique_violation" {
		t.Fatalf("got %v", err)
	}
}

func TestReopenRestoresState(t *testing.T) {
	dir := t.TempDir()
	def := catalog.Def{Unique: []string{"slug"}}

	c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: withName(def)})
	if err != nil {
		t.Fatal(err)
	}

	c.Insert(ctx, doc(`{"_id":"p1","slug":"a"}`), doc(`{"_id":"p2","slug":"b"}`))
	c.UpdateByID(ctx, "p1", collection.Change{Set: map[string]any{"x": 1}}, 0)
	c.DeleteByID(ctx, "p2")
	c.Close()

	again := open(t, dir, def)

	p1, ok := again.Find("p1")

	if !ok || p1.V != 2 || field(t, p1.Doc, "x") != 1.0 {
		t.Fatalf("p1 = %+v ok=%v", p1, ok)
	}

	if _, ok := again.Find("p2"); ok {
		t.Fatal("deleted doc came back")
	}

	if _, err := again.Insert(ctx, doc(`{"_id":"p3","slug":"a"}`)); code(err) != "unique_violation" {
		t.Fatalf("unique index not rebuilt: %v", err)
	}

	if _, err := again.Insert(ctx, doc(`{"_id":"p4","slug":"b"}`)); err != nil {
		t.Fatalf("deleted slug still taken: %v", err)
	}
}

func withName(d catalog.Def) catalog.Def {
	d.Name = "posts"

	return d
}

func TestInsertAcceptsAnyNonEmptyStringID(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	for _, id := range []string{"alice@x.com", "a b/c", "42"} {
		res, err := c.Insert(ctx, doc(`{"_id":`+strconv.Quote(id)+`,"n":1}`))
		if err != nil || res[0].ID != id {
			t.Fatalf("%q: %v %v", id, res, err)
		}

		got, ok := c.Find(id)
		if !ok || field(t, got.Doc, "_id") != id {
			t.Fatalf("%q: doc %s", id, got.Doc)
		}
	}

	for _, bad := range []string{`{"_id":""}`, `{"_id":null}`, `{"_id":1}`} {
		if _, err := c.Insert(ctx, doc(bad)); code(err) != "invalid_id" {
			t.Fatalf("%s: %v", bad, err)
		}
	}
}

func TestInsertWithoutIDKeepsItInTheStoredDoc(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	res, err := c.Insert(ctx, doc(`{"n":1}`), doc(`{"n":2}`))
	if err != nil || res[0].ID >= res[1].ID {
		t.Fatalf("ids %v, err %v", res, err)
	}

	got, ok := c.Get(res[1].ID)
	if !ok || field(t, got.Doc, "_id") != res[1].ID || field(t, got.Doc, "_v") == nil {
		t.Fatalf("doc %s", got.Doc)
	}
}
