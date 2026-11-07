package ndb

import (
	"context"
	"slices"
	"testing"
)

func TestInsertOneAndCount(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	calls, err := Collection[Call](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	res, err := calls.Insert(ctx, Call{CallID: "c1", Status: "a"})
	if err != nil || res.ID != "c1" || res.V != 1 {
		t.Fatalf("insert = %+v, %v", res, err)
	}

	gen, err := calls.Insert(ctx, Call{Status: "b"})
	if err != nil || gen.ID == "" {
		t.Fatalf("generated id = %+v, %v", gen, err)
	}

	if n, err := calls.Count(ctx); err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}

	if n, err := calls.Count(ctx, Eq("status", "a")); err != nil || n != 1 {
		t.Fatalf("count a = %d, %v", n, err)
	}
}

func TestAdmin(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	name := newColl()

	calls, err := Collection[Call](db, name)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.Insert(ctx, Call{CallID: "c1", Duration: 3}); err != nil {
		t.Fatal(err)
	}

	if err := db.Health(ctx); err != nil {
		t.Fatalf("health: %v", err)
	}

	list, err := db.Collections(ctx)
	if err != nil || !slices.ContainsFunc(list, func(d map[string]any) bool { return d["name"] == name }) {
		t.Fatalf("collections = %v, %v", list, err)
	}

	for what, call := range map[string]func(context.Context) (map[string]any, error){
		"db stats":   db.Stats,
		"db size":    db.Size,
		"coll stats": calls.Stats,
		"compact":    calls.Compact,
	} {
		if out, err := call(ctx); err != nil || len(out) == 0 {
			t.Fatalf("%s = %v, %v", what, out, err)
		}
	}

	indexes, err := calls.EnsureIndex(ctx, "duration")
	if err != nil || !slices.Contains(indexes, "duration") {
		t.Fatalf("ensure index = %v, %v", indexes, err)
	}

	indexes, err = calls.DropIndex(ctx, "duration")
	if err != nil || slices.Contains(indexes, "duration") {
		t.Fatalf("drop index = %v, %v", indexes, err)
	}
}
