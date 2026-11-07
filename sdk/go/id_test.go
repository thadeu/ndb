package ndb

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
)

var uuid7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIDIsUUIDv7AndOrdered(t *testing.T) {
	prev := NewID()

	if !uuid7Re.MatchString(prev) {
		t.Fatalf("not a UUIDv7: %q", prev)
	}

	for i := range 10000 {
		next := NewID()

		if strings.Compare(prev, next) >= 0 {
			t.Fatalf("id %d: %s then %s", i, prev, next)
		}

		prev = next
	}
}

func TestGetUpdateIDDeleteID(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	stock, err := Collection[Stock](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stock.Insert(ctx, Stock{SKU: "a", Group: "g", Qty: 3}); err != nil {
		t.Fatal(err)
	}

	got, err := stock.Get(ctx, "a")
	if err != nil || got.SKU != "a" || got.Qty != 3 {
		t.Fatalf("get: %+v, %v", got, err)
	}

	if _, err := stock.Get(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}

	res, err := stock.Update(ctx, "a", Ops{"$inc": {"qty": -1}}, Guard(Gte("qty", 1)))
	if err != nil || res.Modified != 1 || res.Doc.Qty != 2 {
		t.Fatalf("update id: %+v, %v", res, err)
	}

	res, err = stock.Update(ctx, "a", Ops{"$set": {"qty": 0}}, Guard(Gte("qty", 10)))
	if err != nil || res.Matched != 0 {
		t.Fatalf("failed guard: %+v, %v", res, err)
	}

	res, err = stock.Update(ctx, "z", Ops{"$set": {"qty": 7}}, Upsert())
	if err != nil || res.Modified != 1 || res.Doc.SKU != "z" {
		t.Fatalf("upsert by id: %+v, %v", res, err)
	}

	if n, err := stock.Delete(ctx, "a"); err != nil || n != 1 {
		t.Fatalf("delete id: %d, %v", n, err)
	}

	if n, err := stock.Delete(ctx, "a"); err != nil || n != 0 {
		t.Fatalf("delete again: %d, %v", n, err)
	}
}
