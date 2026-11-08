package ndb

import (
	"context"
	"errors"
	"testing"
)

type Customer struct {
	ID   string `json:"_id"`
	Name string `json:"name"`
}

type Item struct {
	ID      string `json:"_id"`
	OrderID string `json:"order_id"`
	SKU     string `json:"sku"`
	Pos     int    `json:"pos"`
}

type Order struct {
	ID         string    `ndb:"_id"`
	CustomerID string    `ndb:"customer_id"`
	Total      int       `ndb:"total"`
	Customer   *Customer `ndb:"customer,lookup"`
	Items      []Item    `ndb:"items,lookup"`
}

func TestLookup(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	ordersName, customersName, itemsName := newColl(), newColl(), newColl()

	customers, err := Collection[Customer](db, customersName)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := customers.Insert(ctx, Customer{ID: "c1", Name: "Ana"}); err != nil {
		t.Fatal(err)
	}

	items, err := Collection[Item](db, itemsName)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := items.EnsureIndex(ctx, "order_id"); err != nil {
		t.Fatal(err)
	}

	if _, err := items.InsertMany(ctx, []Item{{ID: "i1", OrderID: "o1", SKU: "A", Pos: 2}, {ID: "i2", OrderID: "o1", SKU: "B", Pos: 1}}); err != nil {
		t.Fatal(err)
	}

	orders, err := Collection[Order](db, ordersName)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := orders.Insert(ctx, Order{ID: "o1", CustomerID: "c1", Total: 120, Customer: &Customer{Name: "must not be stored"}}); err != nil {
		t.Fatal(err)
	}

	got, err := orders.Query().
		Lookup("customer", BelongsTo(customersName, "customer_id")).
		Lookup("items", HasMany(itemsName, "order_id").Sort(Asc("pos")).Limit(10)).
		All(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Customer == nil || got[0].Customer.Name != "Ana" || len(got[0].Items) != 2 || got[0].Items[0].ID != "i2" {
		t.Fatalf("got %+v", got)
	}

	plain, err := orders.Get(ctx, "o1")
	if err != nil || plain.Customer != nil {
		t.Fatalf("the lookup field was stored: %+v, %v", plain, err)
	}

	_, err = orders.Query().Lookup("items", HasMany(itemsName, "sku")).All(ctx)
	if !errors.Is(err, ErrLookupUnindexed) {
		t.Fatalf("unindexed: %v", err)
	}
}
