package ndb

import (
	"context"
	"testing"
)

type Line struct {
	Sku string `json:"sku"`
	Qty int    `json:"qty"`
}

type Basket struct {
	ID    string `ndb:"_id"`
	Items []Line `ndb:"items"`
}

func TestUnwindStage(t *testing.T) {
	ctx := context.Background()

	col, err := Collection[Basket](openTest(t, daemon.unix), newColl())
	if err != nil {
		t.Fatal(err)
	}

	_, err = col.InsertMany(ctx, []Basket{
		{ID: "b1", Items: []Line{{"a", 2}, {"b", 1}}},
		{ID: "b2", Items: []Line{{"a", 5}}},
		{ID: "b3"},
	})
	if err != nil {
		t.Fatal(err)
	}

	res, err := col.Aggregate(ctx, Pipeline{
		UnwindStage("items"),
		{"$group": map[string]any{"_id": "$items.sku", "qty": map[string]any{"$sum": "$items.qty"}}},
		SortStage(Asc("_id")),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Rows) != 2 || res.Rows[0]["_id"] != "a" || res.Rows[0]["qty"] != 7.0 || res.Rows[1]["qty"] != 1.0 {
		t.Fatalf("rows = %v", res.Rows)
	}
}
