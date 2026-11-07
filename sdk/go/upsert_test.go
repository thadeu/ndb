package ndb

import (
	"context"
	"errors"
	"testing"
)

func TestUpsert(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	name := newColl()

	calls, err := Collection[Call](db, name)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.Insert(ctx, Call{CallID: "c1", Status: "a", Duration: 5}); err != nil {
		t.Fatal(err)
	}

	res, err := calls.Upsert(ctx, Call{CallID: "c1", Status: "b", Duration: 5})
	if err != nil || res.V != 2 {
		t.Fatalf("upsert existing = %+v, %v", res, err)
	}

	if got, _ := calls.Get(ctx, "c1"); got.Status != "b" {
		t.Fatalf("status = %q", got.Status)
	}

	all, err := calls.UpsertMany(ctx, []Call{{CallID: "c1", Status: "c"}, {CallID: "c2", Status: "n"}})
	if err != nil || len(all) != 2 || all[0].V != 3 || all[1].V != 1 {
		t.Fatalf("upsert many = %+v, %v", all, err)
	}

	if _, err := calls.Insert(ctx, Call{CallID: "c2"}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("insert still refuses an existing _id: %v", err)
	}
}
