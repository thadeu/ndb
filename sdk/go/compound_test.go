package ndb

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type Member struct {
	ID    string `ndb:"_id"`
	Acc   string `ndb:"acc"`
	Email string `ndb:"email"`
	N     int    `ndb:"n"`
}

func TestCompoundIndexAndUniqueOptions(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Member](db, newColl(), Index("acc", "n"), Unique("acc", "email"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Member{ID: "m1", Acc: "a", Email: "x@x", N: 1}); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Member{ID: "m2", Acc: "b", Email: "x@x", N: 2}); err != nil {
		t.Fatalf("the same email in another account: %v", err)
	}

	if _, err := col.Insert(ctx, Member{ID: "m3", Acc: "a", Email: "x@x"}); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("err %v, want ErrUniqueViolation", err)
	}

	indexes, err := col.EnsureIndex(ctx, "email,acc")
	if err != nil || !slices.Contains(indexes, "acc,n") || !slices.Contains(indexes, "email,acc") {
		t.Fatalf("indexes %v, err %v", indexes, err)
	}

	one, err := Collection[Member](db, newColl(), Index("items.sku"), Unique("email"))
	if err != nil {
		t.Fatalf("Index and Unique with one field: %v", err)
	}

	if _, err := one.Insert(ctx, Member{ID: "m1", Email: "x@x"}); err != nil {
		t.Fatal(err)
	}

	if _, err := one.Insert(ctx, Member{ID: "m2", Email: "x@x"}); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("err %v, want ErrUniqueViolation", err)
	}

	if indexes, err := one.EnsureIndex(ctx, "n"); err != nil || !slices.Contains(indexes, "items.sku") {
		t.Fatalf("indexes %v, err %v", indexes, err)
	}

	for _, bad := range []CollOption{Index(), Index("a", "b", "c", "d", "e"), Unique("a", "")} {
		if _, err := Collection[Member](db, newColl(), bad); err == nil {
			t.Fatal("a bad Index or Unique option was accepted")
		}
	}
}
