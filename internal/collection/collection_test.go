package collection_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

var ctx = context.Background()

func open(t *testing.T, dir string, def catalog.Def) *collection.Collection {
	t.Helper()

	if def.Name == "" {
		def.Name = "posts"
	}

	if def.ID == "" {
		def.ID = "post_id"
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

	res, err := c.Insert(ctx, doc(`{"post_id":"p1","status":"draft"}`))
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

	_, err := c.Insert(ctx, doc(`{"post_id":"a"}`), doc(`{"post_id":"a"}`))

	if code(err) != "duplicate_id" {
		t.Fatalf("want duplicate_id, got %v", err)
	}

	if _, ok := c.Find("a"); ok {
		t.Fatal("half of a failed transaction was applied")
	}
}

func TestInsertErrors(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"post_id":"p1"}`))

	cases := map[string]json.RawMessage{
		"duplicate_id": doc(`{"post_id":"p1"}`),
		"missing_id":   doc(`{"status":"x"}`),
		"invalid_id":   doc(`{"post_id":5}`),
		"invalid_doc":  doc(`[1,2]`),
	}

	for want, d := range cases {
		if _, err := c.Insert(ctx, d); code(err) != want {
			t.Fatalf("%s: got %v", want, err)
		}
	}
}

func TestGeneratedID(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{ID: "_id"})

	res, err := c.Insert(ctx, doc(`{"status":"x"}`))
	if err != nil {
		t.Fatal(err)
	}

	got, _ := c.Find(res[0].ID)

	if len(res[0].ID) != 24 || field(t, got.Doc, "_id") != res[0].ID {
		t.Fatalf("id %q, doc %s", res[0].ID, got.Doc)
	}
}

func TestUpdateSetUnset(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"post_id":"p1","status":"draft","tmp":1,"meta":{"a":1}}`))

	v, err := c.Update(ctx, "p1", collection.Change{
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

	c.Insert(ctx, doc(`{"post_id":"p1"}`))

	if _, err := c.Update(ctx, "nope", collection.Change{Set: map[string]any{"a": 1}}, 0); code(err) != "not_found" {
		t.Fatalf("got %v", err)
	}

	if _, err := c.Update(ctx, "p1", collection.Change{Set: map[string]any{"a": 1}}, 9); code(err) != "version_conflict" {
		t.Fatalf("got %v", err)
	}

	if _, err := c.Update(ctx, "p1", collection.Change{Set: map[string]any{"post_id": "p2"}}, 0); code(err) != "immutable_id" {
		t.Fatalf("got %v", err)
	}
}

func TestIfVersionUnderConcurrency(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"post_id":"p1","n":0}`))

	const workers = 20

	var wg sync.WaitGroup

	wins := 0
	mu := sync.Mutex{}

	for i := range workers {
		wg.Go(func() {
			_, err := c.Update(ctx, "p1", collection.Change{Set: map[string]any{"n": i}}, 1)

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
		t.Fatalf("%d updates won with if_version=1, final v=%d", wins, got.V)
	}
}

func TestUpdatesInOneBatchSeeEachOther(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"post_id":"p1"}`))

	const n = 50

	var wg sync.WaitGroup

	for i := range n {
		wg.Go(func() {
			if _, err := c.Update(ctx, "p1", collection.Change{Set: map[string]any{fmt.Sprint("f", i): i}}, 0); err != nil {
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

	c.Insert(ctx, doc(`{"post_id":"p1"}`))

	if err := c.Delete(ctx, "p1", 5); code(err) != "version_conflict" {
		t.Fatalf("got %v", err)
	}

	if err := c.Delete(ctx, "p1", 1); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Find("p1"); ok {
		t.Fatal("found after delete")
	}

	if err := c.Delete(ctx, "p1", 0); code(err) != "not_found" {
		t.Fatalf("got %v", err)
	}

	if _, err := c.Insert(ctx, doc(`{"post_id":"p1"}`)); err != nil {
		t.Fatalf("insert after delete: %v", err)
	}
}

func TestUnique(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Unique: []string{"slug"}})

	c.Insert(ctx, doc(`{"post_id":"p1","slug":"hello"}`))

	if _, err := c.Insert(ctx, doc(`{"post_id":"p2","slug":"hello"}`)); code(err) != "unique_violation" {
		t.Fatalf("insert: got %v", err)
	}

	c.Insert(ctx, doc(`{"post_id":"p2","slug":"other"}`))

	if _, err := c.Update(ctx, "p2", collection.Change{Set: map[string]any{"slug": "hello"}}, 0); code(err) != "unique_violation" {
		t.Fatalf("update: got %v", err)
	}

	c.Delete(ctx, "p1", 0)

	if _, err := c.Update(ctx, "p2", collection.Change{Set: map[string]any{"slug": "hello"}}, 0); err != nil {
		t.Fatalf("slug free after delete: %v", err)
	}

	if _, err := c.Insert(ctx, doc(`{"post_id":"p3"}`), doc(`{"post_id":"p4"}`)); err != nil {
		t.Fatalf("missing unique field must not collide: %v", err)
	}
}

func TestUniqueInsideOneTransaction(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Unique: []string{"slug"}})

	_, err := c.Insert(ctx, doc(`{"post_id":"a","slug":"x"}`), doc(`{"post_id":"b","slug":"x"}`))

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

	c.Insert(ctx, doc(`{"post_id":"p1","slug":"a"}`), doc(`{"post_id":"p2","slug":"b"}`))
	c.Update(ctx, "p1", collection.Change{Set: map[string]any{"x": 1}}, 0)
	c.Delete(ctx, "p2", 0)
	c.Close()

	again := open(t, dir, def)

	p1, ok := again.Find("p1")

	if !ok || p1.V != 2 || field(t, p1.Doc, "x") != 1.0 {
		t.Fatalf("p1 = %+v ok=%v", p1, ok)
	}

	if _, ok := again.Find("p2"); ok {
		t.Fatal("deleted doc came back")
	}

	if _, err := again.Insert(ctx, doc(`{"post_id":"p3","slug":"a"}`)); code(err) != "unique_violation" {
		t.Fatalf("unique index not rebuilt: %v", err)
	}

	if _, err := again.Insert(ctx, doc(`{"post_id":"p4","slug":"b"}`)); err != nil {
		t.Fatalf("deleted slug still taken: %v", err)
	}
}

func withName(d catalog.Def) catalog.Def {
	d.Name = "posts"
	d.ID = "post_id"

	return d
}
