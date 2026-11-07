package ndb

import (
	"context"
	"testing"
	"time"
)

func TestElemMatchAndDateMath(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	type item struct {
		SKU string `json:"sku"`
		Qty int    `json:"qty"`
	}

	type order struct {
		ID     string    `ndb:"_id"`
		Items  []item    `ndb:"items"`
		Scores []int     `ndb:"scores"`
		TS     time.Time `ndb:"ts,time"`
	}

	orders, err := Collection[order](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()

	if _, err := orders.InsertMany(ctx, []order{{ID: "o1", Items: []item{{"a", 1}, {"b", 5}}, Scores: []int{5, 25}, TS: now.Add(-48 * time.Hour)}, {ID: "o2", Items: []item{{"a", 3}}, Scores: []int{15}, TS: now.Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}

	for name, f := range map[string]Filter{
		"object elemMatch": ElemMatch("items", Eq("sku", "a"), Gt("qty", 2)),
		"scalar elemMatch": ElemMatch("scores", Gte("", 10), Lt("", 20)),
		"date math":        Gte("ts", "now-1d"),
	} {
		rows, err := orders.Query(f).All(ctx)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		if len(rows) != 1 || rows[0].ID != "o2" {
			t.Fatalf("%s: rows = %+v", name, rows)
		}
	}
}
