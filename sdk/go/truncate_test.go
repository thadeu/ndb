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
