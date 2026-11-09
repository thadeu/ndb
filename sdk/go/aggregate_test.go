package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func seedAggCalls(t *testing.T) (*Coll[Call], context.Context) {
	t.Helper()

	ctx := context.Background()
	db := openTest(t, daemon.unix)

	c, err := Collection[Call](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	statuses := []string{"answered", "missed", "answered", "answered", "abandoned"}

	var docs []Call

	for i, s := range statuses {
		docs = append(docs, Call{
			CallID:   "c" + string(rune('1'+i)),
			Status:   s,
			Duration: 10 * (i + 1),
			TS:       base.Add(time.Duration(i) * 40 * time.Minute),
		})
	}

	if _, err := c.InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}

	return c, ctx
}

func TestAggregateReturnsMaps(t *testing.T) {
	c, ctx := seedAggCalls(t)

	res, err := c.Aggregate(ctx, Pipeline{
		{"$group": map[string]any{"_id": "$status", "n": map[string]any{"$count": map[string]any{}}, "avg": map[string]any{"$avg": "$duration"}}},
		SortStage(Desc("n"), Asc("_id")),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Rows) != 3 || res.Rows[0]["_id"] != "answered" || res.Rows[0]["n"] != 3.0 || res.Rows[0]["avg"] != 80.0/3 {
		t.Fatalf("rows = %v", res.Rows)
	}

	if res.Next != "" || res.Total != 0 {
		t.Fatalf("next %q total %d", res.Next, res.Total)
	}
}

func TestAggregateAsDecodesIntoAType(t *testing.T) {
	c, ctx := seedAggCalls(t)

	type hourly struct {
		ID struct {
			Status string `json:"status"`
			Hour   string `json:"hour"`
		} `json:"_id"`
		Total int `json:"total"`
	}

	res, err := AggregateAs[hourly](ctx, c, Pipeline{
		{"$match": map[string]any{"status": "answered"}},
		{"$group": map[string]any{
			"_id": map[string]any{
				"status": "$status",
				"hour":   map[string]any{"$dateTrunc": map[string]any{"date": "$ts", "unit": "hour"}},
			},
			"total": map[string]any{"$count": map[string]any{}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Rows) != 3 || res.Rows[0].ID.Hour != "2026-11-01T10:00:00Z" || res.Rows[2].ID.Hour != "2026-11-01T12:00:00Z" || res.Rows[1].Total != 1 {
		t.Fatalf("rows = %+v", res.Rows)
	}
}

func TestAggregatePagesWithCursor(t *testing.T) {
	c, ctx := seedAggCalls(t)

	pipe := Pipeline{{"$group": map[string]any{"_id": "$duration"}}}

	var ids []any

	opts := []AggOption{AggLimit(2), AggCount()}

	for {
		res, err := c.Aggregate(ctx, pipe, opts...)
		if err != nil {
			t.Fatal(err)
		}

		if res.Total != 5 {
			t.Fatalf("total = %d, want 5", res.Total)
		}

		for _, r := range res.Rows {
			ids = append(ids, r["_id"])
		}

		if res.Next == "" {
			break
		}

		opts = []AggOption{AggLimit(2), AggCount(), AggAfter(res.Next)}
	}

	if len(ids) != 5 || ids[0] != 10.0 || ids[4] != 50.0 {
		t.Fatalf("ids = %v", ids)
	}
}

func TestAggregateSkip(t *testing.T) {
	c, ctx := seedAggCalls(t)

	res, err := c.Aggregate(ctx, Pipeline{{"$group": map[string]any{"_id": "$duration"}}}, AggSkip(3))
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Rows) != 2 {
		t.Fatalf("rows = %v", res.Rows)
	}
}

func TestAggregateErrorCodes(t *testing.T) {
	c, ctx := seedAggCalls(t)

	_, err := c.Aggregate(ctx, Pipeline{{"$limit": 1}, {"$match": map[string]any{}}})

	if !errors.Is(err, ErrInvalidPipeline) {
		t.Fatalf("want ErrInvalidPipeline, got %v", err)
	}

	_, err = c.Aggregate(ctx, Pipeline{{"$group": map[string]any{"_id": nil}}}, AggAfter("!!!"))

	if !errors.Is(err, ErrCursorInvalid) {
		t.Fatalf("want ErrCursorInvalid, got %v", err)
	}
}

func TestSortStageKeepsKeyOrder(t *testing.T) {
	raw, err := jsonOf(SortStage(Asc("b"), Desc("a")))
	if err != nil {
		t.Fatal(err)
	}

	if raw != `{"$sort":{"b":1,"a":-1}}` {
		t.Fatalf("got %s", raw)
	}
}

func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)

	return string(b), err
}
