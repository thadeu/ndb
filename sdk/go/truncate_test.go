package ndb

import (
	"context"
	"errors"
	"testing"
)

func TestTruncateAndDrop(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	name := newColl()

	calls, err := Collection[Call](db, name)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.InsertMany(ctx, []Call{{CallID: "c1", Status: "a"}, {CallID: "c2", Status: "b"}}); err != nil {
		t.Fatal(err)
	}

	if err := calls.Truncate(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := calls.Get(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after truncate: %v", err)
	}

	if _, err := calls.Insert(ctx, Call{CallID: "c3", Status: "c"}); err != nil {
		t.Fatal(err)
	}

	var ops []Op

	for ev, err := range calls.Watch(ctx, "relay", Limit(2)) {
		if err != nil {
			t.Fatal(err)
		}

		ops = append(ops, ev.Op)
	}

	if len(ops) != 2 || ops[0] != OpTruncate || ops[1] != OpPut {
		t.Fatalf("events = %v, want [truncate put]", ops)
	}

	if err := calls.Drop(ctx); err != nil {
		t.Fatal(err)
	}

	if err := calls.Drop(ctx); !errors.Is(err, ErrUnknownCollection) {
		t.Fatalf("drop again: %v", err)
	}
}

func TestRename(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	name, other := newColl(), newColl()

	calls, err := Collection[Call](db, name)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.Insert(ctx, Call{CallID: "c1", Status: "a"}); err != nil {
		t.Fatal(err)
	}

	target, err := Collection[Call](db, other)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := target.Insert(ctx, Call{CallID: "old", Status: "z"}); err != nil {
		t.Fatal(err)
	}

	if err := calls.Rename(ctx, other, false); !errors.Is(err, ErrCollectionExists) {
		t.Fatalf("rename onto an existing name: %v", err)
	}

	if err := calls.Rename(ctx, other, true); err != nil {
		t.Fatal(err)
	}

	if _, err := target.Get(ctx, "c1"); err != nil {
		t.Fatalf("get under the new name: %v", err)
	}

	if _, err := target.Get(ctx, "old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the replaced collection is still there: %v", err)
	}
}
