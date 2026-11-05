package collection_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
)

func newDirect(id string, max int64) *collection.Direct {
	return collection.NewDirect(collection.DirectConfig{
		Def:      catalog.Def{Name: "calls", ID: id, Write: catalog.Direct, Path: "/x/*.ndjson"},
		MaxBytes: max,
	})
}

func TestDirectLastVersionWins(t *testing.T) {
	d := newDirect("event_id", 0)

	for i, line := range []string{
		`{"event_id":"a","n":1}`,
		`{"event_id":"a","n":2}`,
		`{"event_id":"b","n":1}`,
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
	d := newDirect("event_id", 0)

	_ = d.Apply("f", 0, []byte(`{"event_id":"a"}`))
	_ = d.Apply("f", 20, []byte(`{"event_id":"a","_deleted":true}`))

	if _, ok := d.Find("a"); ok || d.Len() != 0 {
		t.Fatal("tombstone did not remove the doc")
	}

	if err := d.Apply("f", 60, []byte(`{"_deleted":true}`)); err == nil {
		t.Fatal("tombstone without id must fail")
	}
}

func TestDirectBatchAllOrNothing(t *testing.T) {
	d := newDirect("event_id", 0)

	good := `{"_batch":[{"event_id":"a"},{"event_id":"b"},{"event_id":"a","_deleted":true}]}`

	if err := d.Apply("f", 0, []byte(good)); err != nil {
		t.Fatal(err)
	}

	if _, ok := d.Find("b"); !ok || d.Len() != 1 {
		t.Fatalf("len = %d", d.Len())
	}

	bad := `{"_batch":[{"event_id":"c"},"oops",{"event_id":"d"}]}`

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
	d := newDirect("event_id", 0)

	for _, line := range []string{`{bad`, `[1,2]`, `"s"`, `{"event_id":7}`, `{"event_id":""}`} {
		if err := d.Apply("f", 0, []byte(line)); err == nil || errors.Is(err, collection.ErrFull) {
			t.Errorf("%s: err = %v", line, err)
		}
	}

	if d.Len() != 0 {
		t.Fatal("bad lines changed the state")
	}
}

func TestDirectFullThenRoom(t *testing.T) {
	d := newDirect("event_id", 40)

	if err := d.Apply("f", 0, []byte(`{"event_id":"a","pad":"0123456789012345"}`)); err != nil {
		t.Fatal(err)
	}

	err := d.Apply("f", 50, []byte(`{"event_id":"b"}`))
	if !errors.Is(err, collection.ErrFull) {
		t.Fatalf("err = %v", err)
	}

	if err := d.Apply("f", 80, []byte(`{"event_id":"a","_deleted":true}`)); err != nil {
		t.Fatalf("a delete must fit when full: %v", err)
	}

	if err := d.Apply("f", 50, []byte(`{"event_id":"b"}`)); err != nil {
		t.Fatalf("after delete: %v", err)
	}
}

func TestDirectQueryUsesIndexAndSeesTombstones(t *testing.T) {
	d := collection.NewDirect(collection.DirectConfig{
		Def: catalog.Def{Name: "calls", ID: "call_id", Write: catalog.Direct, Path: "/x/*.ndjson", Indexes: []string{"status"}},
	})

	t.Cleanup(func() { d.Close() })

	lines := []string{
		`{"call_id":"c1","status":"answered"}`,
		`{"call_id":"c2","status":"failed"}`,
		`{"call_id":"c3","status":"answered"}`,
		`{"call_id":"c3","_deleted":true}`,
	}

	for i, l := range lines {
		if err := d.Apply("f.ndjson", int64(i*100), []byte(l)); err != nil {
			t.Fatal(err)
		}
	}

	waitDirectReady(t, d)

	q, err := d.ParseQuery([]byte(`{"where":{"status":"answered"}}`))
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
