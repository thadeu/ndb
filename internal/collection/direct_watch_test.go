package collection_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/feed"
)

func newDirectBuf(id string, buf int) *collection.Direct {
	return collection.NewDirect(collection.DirectConfig{
		Def:         catalog.Def{Name: "calls", Write: catalog.Direct, Path: "/x/*.ndjson"},
		WatchBuffer: buf,
	})
}

// writeLines appends lines to path and applies them to d, like the tailer.
func writeLines(t *testing.T, d interface {
	Apply(string, int64, []byte) error
}, path string, lines ...string) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	defer f.Close()

	for _, l := range lines {
		off, _ := f.Seek(0, 2)

		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}

		if err := d.Apply(path, off, []byte(l)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDirectWatchReplaysFilesThenFollowsLive(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.ndjson"), filepath.Join(dir, "b.ndjson")

	d := newDirect("_id", 0)

	writeLines(t, d, a, `{"_id":"a1"}`, `{"_id":"a2"}`)
	writeLines(t, d, b, `{"_id":"b1"}`, `{"_id":"a1","n":2}`)

	s, err := d.Feed().Open("")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	writeLines(t, d, a, `{"_id":"a3"}`, `{"_id":"a2","_deleted":true}`)

	want := []struct{ op, id string }{
		{"put", "a1"}, {"put", "a2"}, {"put", "b1"}, {"put", "a1"}, {"put", "a3"}, {"del", "a2"},
	}

	for i, w := range want {
		ev := nextEv(t, s)

		if ev.Op != w.op || ev.ID != w.id {
			t.Fatalf("event %d = %s %s, want %s %s", i, ev.Op, ev.ID, w.op, w.id)
		}

		if w.op == "del" && len(ev.Doc) != 0 {
			t.Fatal("del must have no doc")
		}
	}
}

func TestDirectWatchResumesAfterRestartRebuild(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.ndjson")
	lines := []string{`{"_id":"e1"}`, `{"_id":"e2"}`, `{"_id":"e3"}`, `{"_id":"e4"}`}

	d := newDirect("_id", 0)

	writeLines(t, d, path, lines...)

	s, _ := d.Feed().Open("")

	nextEv(t, s)

	ack := nextEv(t, s).Pos

	stored, err := d.Feed().Merge("", ack)
	if err != nil {
		t.Fatal(err)
	}

	s.Close()

	// A restart: a new memtable rebuilt from the same file, plus a new line.
	d2 := newDirect("_id", 0)

	var off int64

	for _, l := range lines {
		if err := d2.Apply(path, off, []byte(l)); err != nil {
			t.Fatal(err)
		}

		off += int64(len(l)) + 1
	}

	writeLines(t, d2, path, `{"_id":"e5"}`)

	r, err := d2.Feed().Open(stored)
	if err != nil {
		t.Fatal(err)
	}

	defer r.Close()

	for _, id := range []string{"e3", "e4", "e5"} {
		if ev := nextEv(t, r); ev.ID != id {
			t.Fatalf("got %s, want %s", ev.ID, id)
		}
	}
}

func TestDirectWatchBatchLineResumesInsideTheBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.ndjson")
	d := newDirect("_id", 0)

	writeLines(t, d, path, `{"_batch":[{"_id":"x"},{"_id":"y"},{"_id":"z"}]}`)

	s, _ := d.Feed().Open("")

	nextEv(t, s)

	stored, _ := d.Feed().Merge("", nextEv(t, s).Pos)

	s.Close()

	r, _ := d.Feed().Open(stored)
	defer r.Close()

	if ev := nextEv(t, r); ev.ID != "z" {
		t.Fatalf("got %s, want z", ev.ID)
	}
}

func TestDirectWatchNowAndMonotonicAck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.ndjson")
	d := newDirect("_id", 0)

	writeLines(t, d, path, `{"_id":"old"}`, `{"_id":"old2"}`)

	now := d.Feed().Now()

	writeLines(t, d, path, `{"_id":"fresh"}`)

	s, err := d.Feed().Open(now)
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	fresh := nextEv(t, s)

	if fresh.ID != "fresh" {
		t.Fatalf("got %s, want fresh", fresh.ID)
	}

	all, _ := d.Feed().Open("")
	defer all.Close()

	first := nextEv(t, all).Pos

	stored, _ := d.Feed().Merge("", fresh.Pos)

	if got, _ := d.Feed().Merge(stored, first); got != stored {
		t.Fatal("an older ack moved the position back")
	}

	if _, err := d.Feed().Merge("", "garbage"); err == nil {
		t.Fatal("garbage token must fail")
	}
}

func TestDirectSlowWatcherLags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.ndjson")
	d := newDirectBuf("_id", 3)

	s, _ := d.Feed().Open("")
	defer s.Close()

	for i := range 20 {
		writeLines(t, d, path, fmt.Sprintf(`{"_id":"e%d"}`, i))
	}

	for range 3 {
		nextEv(t, s)
	}

	if _, err := s.Next(ctx); err != feed.ErrLagged {
		t.Fatalf("err = %v, want ErrLagged", err)
	}
}
