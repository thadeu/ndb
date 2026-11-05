package engine_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/feed"
)

func nextEvent(t *testing.T, s feed.Stream) feed.Event {
	t.Helper()

	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ev, err := s.Next(c)
	if err != nil {
		t.Fatalf("next: %v", err)
	}

	return ev
}

func insertIDs(t *testing.T, db *engine.DB, from, to int) {
	t.Helper()

	col, err := db.GetOrCreate("posts")
	if err != nil {
		t.Fatal(err)
	}

	for i := from; i <= to; i++ {
		if _, err := col.Insert(ctx, []byte(fmt.Sprintf(`{"_id":"p%d"}`, i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWatchConsumerResumesAfterRestartFromItsAck(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	insertIDs(t, db, 1, 5)

	s, err := db.Watch("posts", "relay", "")
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		nextEvent(t, s)
	}

	third := nextEvent(t, s)

	if err := db.Ack("posts", "relay", third.Pos); err != nil {
		t.Fatal(err)
	}

	s.Close()
	db.Close()

	db = open(t, cfg(dir, nil))

	s, err = db.Watch("posts", "relay", "")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	for _, id := range []string{"p4", "p5"} {
		if ev := nextEvent(t, s); ev.ID != id {
			t.Fatalf("got %s, want %s", ev.ID, id)
		}
	}
}

func TestWatchTwoConsumersHaveTheirOwnPosition(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertIDs(t, db, 1, 3)

	a, _ := db.Watch("posts", "a", "")
	ev := nextEvent(t, a)

	a.Close()

	if err := db.Ack("posts", "a", ev.Pos); err != nil {
		t.Fatal(err)
	}

	b, _ := db.Watch("posts", "b", "")
	defer b.Close()

	if first := nextEvent(t, b); first.ID != "p1" {
		t.Fatalf("b starts at %s, want p1", first.ID)
	}

	a, _ = db.Watch("posts", "a", "")
	defer a.Close()

	if first := nextEvent(t, a); first.ID != "p2" {
		t.Fatalf("a resumes at %s, want p2", first.ID)
	}
}

func TestWatchFromNowIsStoredAtFirstConnect(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertIDs(t, db, 1, 2)

	s, err := db.Watch("posts", "late", "now")
	if err != nil {
		t.Fatal(err)
	}

	s.Close()

	insertIDs(t, db, 3, 3)

	s, _ = db.Watch("posts", "late", "now")
	defer s.Close()

	if ev := nextEvent(t, s); ev.ID != "p3" {
		t.Fatalf("got %s, want p3: a second from=now must not move the stored position", ev.ID)
	}
}

func TestAckOlderThanStoredIsIgnored(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertIDs(t, db, 1, 3)

	s, _ := db.Watch("posts", "c", "")
	first, second := nextEvent(t, s), nextEvent(t, s)

	s.Close()

	for _, pos := range []string{second.Pos, first.Pos} {
		if err := db.Ack("posts", "c", pos); err != nil {
			t.Fatal(err)
		}
	}

	s, _ = db.Watch("posts", "c", "")
	defer s.Close()

	if ev := nextEvent(t, s); ev.ID != "p3" {
		t.Fatalf("got %s, want p3", ev.ID)
	}
}

func TestWatchRejectsBadInput(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertIDs(t, db, 1, 1)

	if _, err := db.Watch("nope", "c", ""); err == nil {
		t.Fatal("unknown collection must fail")
	}

	if err := db.Ack("posts", "c", "garbage"); err == nil {
		t.Fatal("garbage position must fail")
	}

	if _, err := db.Watch("posts", "c", "yesterday"); err == nil {
		t.Fatal("bad from must fail")
	}
}

func TestWatchDirectCollectionOverFiles(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	ensure(t, db, catalog.Def{Name: "calls", ID: "event_id", Write: catalog.Direct, Path: dir + "/collections/calls/in/*.ndjson"})

	if _, err := db.Watch("calls", "c", ""); err != nil {
		t.Fatal(err)
	}
}
