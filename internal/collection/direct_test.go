package collection_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
)

func newDirect(id string, max int64) *collection.Direct {
	return collection.NewDirect(collection.DirectConfig{
		Def:      catalog.Def{Name: "calls", Write: catalog.Direct, Path: "/x/*.ndjson"},
		MaxBytes: max,
	})
}

func TestDirectLastVersionWins(t *testing.T) {
	d := newDirect("_id", 0)

	for i, line := range []string{
		`{"_id":"a","n":1}`,
		`{"_id":"a","n":2}`,
		`{"_id":"b","n":1}`,
	} {
		if err := d.Apply("f", int64(i*100), []byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	got, ok := d.Find("a")
	if !ok || field(t, got.Doc, "n") != float64(2) {
		t.Fatalf("a = %s, %v", got.Doc, ok)
	}

	if d.Len() != 2 {
		t.Fatalf("len = %d", d.Len())
	}
}

func TestDirectTombstone(t *testing.T) {
	d := newDirect("_id", 0)

	_ = d.Apply("f", 0, []byte(`{"_id":"a"}`))
	_ = d.Apply("f", 20, []byte(`{"_id":"a","_deleted":true}`))

	if _, ok := d.Find("a"); ok || d.Len() != 0 {
		t.Fatal("tombstone did not remove the doc")
	}

	if err := d.Apply("f", 60, []byte(`{"_deleted":true}`)); err == nil {
		t.Fatal("tombstone without id must fail")
	}
}

func TestDirectBatchAllOrNothing(t *testing.T) {
	d := newDirect("_id", 0)

	good := `{"_batch":[{"_id":"a"},{"_id":"b"},{"_id":"a","_deleted":true}]}`

	if err := d.Apply("f", 0, []byte(good)); err != nil {
		t.Fatal(err)
	}

	if _, ok := d.Find("b"); !ok || d.Len() != 1 {
		t.Fatalf("len = %d", d.Len())
	}

	bad := `{"_batch":[{"_id":"c"},"oops",{"_id":"d"}]}`

	if err := d.Apply("f", 100, []byte(bad)); err == nil {
		t.Fatal("bad batch must fail")
	}

	if _, ok := d.Find("c"); ok || d.Len() != 1 {
		t.Fatal("a failed batch applied part of its items")
	}
}

func TestDirectNoIDUsesFileOffset(t *testing.T) {
	d := newDirect("", 0)

	_ = d.Apply("a.ndjson", 0, []byte(`{"x":1}`))
	_ = d.Apply("a.ndjson", 8, []byte(`{"x":2}`))
	_ = d.Apply("b.ndjson", 0, []byte(`{"x":3}`))

	if _, ok := d.Find("a.ndjson:8"); !ok || d.Len() != 3 {
		t.Fatalf("len = %d", d.Len())
	}
}

func TestDirectRejectsBadLines(t *testing.T) {
	d := newDirect("_id", 0)

	for _, line := range []string{`{bad`, `[1,2]`, `"s"`, `{"_id":7}`, `{"_id":""}`} {
		if err := d.Apply("f", 0, []byte(line)); err == nil || errors.Is(err, collection.ErrFull) {
			t.Errorf("%s: err = %v", line, err)
		}
	}

	if d.Len() != 0 {
		t.Fatal("bad lines changed the state")
	}
}

func TestDirectFullThenRoom(t *testing.T) {
	d := newDirect("_id", 30)

	if err := d.Apply("f", 0, []byte(`{"_id":"a","pad":"0123456789012345"}`)); err != nil {
		t.Fatal(err)
	}

	err := d.Apply("f", 50, []byte(`{"_id":"b"}`))
	if !errors.Is(err, collection.ErrFull) {
		t.Fatalf("err = %v", err)
	}

	if err := d.Apply("f", 80, []byte(`{"_id":"a","_deleted":true}`)); err != nil {
		t.Fatalf("a delete must fit when full: %v", err)
	}

	if err := d.Apply("f", 50, []byte(`{"_id":"b"}`)); err != nil {
		t.Fatalf("after delete: %v", err)
	}
}

func TestDirectQueryUsesIndexAndSeesTombstones(t *testing.T) {
	d := collection.NewDirect(collection.DirectConfig{
		Def: catalog.Def{Name: "calls", Write: catalog.Direct, Path: "/x/*.ndjson", Indexes: []string{"status"}},
	})

	t.Cleanup(func() { d.Close() })

	lines := []string{
		`{"_id":"c1","status":"answered"}`,
		`{"_id":"c2","status":"failed"}`,
		`{"_id":"c3","status":"answered"}`,
		`{"_id":"c3","_deleted":true}`,
	}

	for i, l := range lines {
		if err := d.Apply("f.ndjson", int64(i*100), []byte(l)); err != nil {
			t.Fatal(err)
		}
	}

	waitDirectReady(t, d)

	q, err := d.ParseQuery([]byte(`{"query":{"status":"answered"}}`))
	if err != nil {
		t.Fatal(err)
	}

	page, err := d.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Rows) != 1 || page.Plan != "index:status" {
		t.Fatalf("rows=%s plan=%s", page.Rows, page.Plan)
	}
}

func waitDirectReady(t *testing.T, d *collection.Direct) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		ready := true

		for _, ix := range d.Indexes() {
			ready = ready && ix.State == "ready"
		}

		if ready {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("index not ready")
}

func TestDirectAggregate(t *testing.T) {
	d := newDirect("_id", 0)

	for i, line := range []string{
		`{"_id":"a","k":"x","n":1}`,
		`{"_id":"b","k":"x","n":2}`,
		`{"_id":"c","k":"y","n":4}`,
		`{"_id":"b","k":"y","n":8}`,
	} {
		if err := d.Apply("f", int64(i*100), []byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	p, err := d.ParseAggregate([]byte(`{"pipeline":[{"$group":{"_id":"$k","s":{"$sum":"$n"}}}]}`))
	if err != nil {
		t.Fatal(err)
	}

	page, err := d.Aggregate(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Rows) != 2 || string(page.Rows[0]) != `{"_id":"x","s":1}` || string(page.Rows[1]) != `{"_id":"y","s":12}` {
		t.Fatalf("rows = %s", page.Rows)
	}
}

func TestDirectLineWithIDIsAddressableAndVersioned(t *testing.T) {
	d := newDirect("", 0)

	_ = d.Apply("a.ndjson", 0, []byte(`{"_id":"k1","n":1}`))
	_ = d.Apply("a.ndjson", 30, []byte(`{"_id":"k1","n":2}`))

	got, ok := d.Get("k1")
	if !ok || field(t, got.Doc, "n") != float64(2) || field(t, got.Doc, "_id") != "k1" {
		t.Fatalf("k1 = %s, %v", got.Doc, ok)
	}

	if field(t, got.Doc, "_v") == nil || d.Len() != 1 {
		t.Fatalf("doc has no _v or old version kept: %s, len %d", got.Doc, d.Len())
	}
}

func TestDirectLineWithoutIDHasNoIDAndIsNotAddressable(t *testing.T) {
	d := newDirect("", 0)

	_ = d.Apply("a.ndjson", 0, []byte(`{"n":1}`))

	got, ok := d.Find("a.ndjson:0")
	if !ok || field(t, got.Doc, "_id") != nil {
		t.Fatalf("stored doc must have no _id: %s, %v", got.Doc, ok)
	}

	if _, ok := d.Get("a.ndjson:0"); ok {
		t.Fatal("the fallback key must not answer Get")
	}

	q, err := d.ParseQuery([]byte(`{"query":{"n":1}}`))
	if err != nil {
		t.Fatal(err)
	}

	page, err := d.Query(ctx, q)
	if err != nil || len(page.Rows) != 1 || strings.Contains(string(page.Rows[0]), `"_id"`) {
		t.Fatalf("rows = %q, err %v", page.Rows, err)
	}
}

func TestDirectTombstoneWithoutIDIsMissingID(t *testing.T) {
	d := newDirect("", 0)

	if err := d.Apply("a.ndjson", 0, []byte(`{"n":1,"_deleted":true}`)); code(err) != "missing_id" {
		t.Fatalf("err = %v", err)
	}
}
