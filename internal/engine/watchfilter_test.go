package engine_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/feed"
)

func insertN(t *testing.T, db *engine.DB, from, to int) {
	t.Helper()

	col, err := db.GetOrCreate("posts")
	if err != nil {
		t.Fatal(err)
	}

	for i := from; i <= to; i++ {
		if _, err := col.Insert(ctx, []byte(fmt.Sprintf(`{"_id":"p%d","n":%d}`, i, i))); err != nil {
			t.Fatal(err)
		}
	}
}

// idle waits for the next event for d and reports whether none came.
func idle(s feed.Stream, d time.Duration) bool {
	c, cancel := context.WithTimeout(ctx, d)
	defer cancel()

	_, err := s.Next(c)

	return errors.Is(err, context.DeadlineExceeded)
}

func TestWatchWhereDeliversOnlyMatchingDocs(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertN(t, db, 1, 6)

	s, err := db.WatchWhere("posts", "relay", "", []byte(`{"n":{"$gte":4}}`))
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	for _, id := range []string{"p4", "p5", "p6"} {
		if ev := nextEvent(t, s); ev.ID != id {
			t.Fatalf("got %s, want %s", ev.ID, id)
		}
	}

	insertN(t, db, 7, 7)

	if ev := nextEvent(t, s); ev.ID != "p7" {
		t.Fatalf("live: got %s, want p7", ev.ID)
	}
}

func TestWatchWhereDeleteFollowsTheLastKnownDoc(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col, _ := db.GetOrCreate("posts")

	insertN(t, db, 1, 3)

	for _, id := range []string{"p1", "p3"} {
		if _, err := col.Delete(ctx, []byte(`{"_id":"`+id+`"}`), false); err != nil {
			t.Fatal(err)
		}
	}

	s, err := db.WatchWhere("posts", "relay", "", []byte(`{"n":{"$gte":3}}`))
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if ev := nextEvent(t, s); ev.Op != "put" || ev.ID != "p3" {
		t.Fatalf("got %+v, want the put of p3", ev)
	}

	if ev := nextEvent(t, s); ev.Op != "del" || ev.ID != "p3" {
		t.Fatalf("got %+v, want the del of p3 (it matched)", ev)
	}

	if !idle(s, 100*time.Millisecond) {
		t.Fatal("the del of p1 must not be delivered: p1 never matched")
	}
}

func TestWatchWhereDropsDeleteOfUnknownDoc(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col, _ := db.GetOrCreate("posts")

	insertN(t, db, 1, 1)

	s, _ := db.WatchWhere("posts", "relay", "now", []byte(`{"n":1}`))
	defer s.Close()

	if _, err := col.Delete(ctx, []byte(`{"_id":"p1"}`), false); err != nil {
		t.Fatal(err)
	}

	if !idle(s, 100*time.Millisecond) {
		t.Fatal("a del of a doc this stream never saw has no last known doc: dropped")
	}

	plain, _ := db.WatchWhere("posts", "plain", "now", nil)
	defer plain.Close()

	insertN(t, db, 2, 2)

	if ev := nextEvent(t, plain); ev.ID != "p2" {
		t.Fatalf("no where: %+v", ev)
	}
}

func TestWatchWhereAdvancesTheCursorOverFilteredEvents(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertN(t, db, 1, 5)

	s, err := db.WatchWhere("posts", "relay", "", []byte(`{"n":{"$gt":100}}`))
	if err != nil {
		t.Fatal(err)
	}

	if !idle(s, 100*time.Millisecond) {
		t.Fatal("nothing matches")
	}

	s.Close()

	insertN(t, db, 6, 6)

	again, err := db.Watch("posts", "relay", "")
	if err != nil {
		t.Fatal(err)
	}

	defer again.Close()

	if ev := nextEvent(t, again); ev.ID != "p6" {
		t.Fatalf("got %s: the cursor must have moved past the filtered events", ev.ID)
	}
}

func TestWatchWhereRejectsABadFilter(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertN(t, db, 1, 1)

	_, err := db.WatchWhere("posts", "relay", "", []byte(`{"n":{"$bogus":1}}`))

	var coded interface{ Code() string }

	if !errors.As(err, &coded) || coded.Code() != "invalid_query" {
		t.Fatalf("err = %v", err)
	}
}
