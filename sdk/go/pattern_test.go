package ndb

import (
	"context"
	"errors"
	"slices"
	"testing"
)

type logLine struct {
	ID   string `ndb:"_id"`
	At   int    `ndb:"at"`
	Lvl  string `ndb:"lvl"`
	Coll string `ndb:"_coll"`
}

func TestPattern(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	base := newColl() + "-logs-"

	for i, docs := range [][]logLine{
		{{ID: "a1", At: 1, Lvl: "info"}, {ID: "a2", At: 3, Lvl: "error"}},
		{{ID: "b1", At: 2, Lvl: "error"}, {ID: "b2", At: 4, Lvl: "info"}},
	} {
		part, err := Collection[logLine](db, base+string(rune('1'+i)))
		if err != nil {
			t.Fatal(err)
		}

		if _, err := part.InsertMany(ctx, docs); err != nil {
			t.Fatal(err)
		}
	}

	logs, err := Collection[logLine](db, base+"*")
	if err != nil {
		t.Fatal(err)
	}

	var got []string

	for row, err := range logs.Query().Sort(Desc("at")).Limit(1).Each(ctx) {
		if err != nil {
			t.Fatal(err)
		}

		got = append(got, row.ID+"@"+row.Coll)
	}

	want := []string{"b2@" + base + "2", "a2@" + base + "1", "b1@" + base + "2", "a1@" + base + "1"}

	if !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}

	n, err := logs.Query(Eq("lvl", "error")).Count(ctx)
	if err != nil || n != 2 {
		t.Fatalf("count = %d, %v", n, err)
	}

	res, err := logs.Aggregate(ctx, Pipeline{{"$group": map[string]any{"_id": "$lvl", "n": map[string]any{"$count": map[string]any{}}}}})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Rows) != 2 {
		t.Fatalf("aggregate rows = %v", res.Rows)
	}

	_, err = logs.Insert(ctx, logLine{ID: "x"})

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "pattern_read_only" {
		t.Fatalf("insert on a pattern: %v", err)
	}
}
