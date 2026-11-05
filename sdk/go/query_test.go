package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
)

func TestQueryBodyJSON(t *testing.T) {
	calls := new(Coll[Call])

	cases := []struct {
		name  string
		build func() *Query[Call]
		want  string
	}{
		{
			name:  "equality is a plain value",
			build: func() *Query[Call] { return calls.Where(Eq("status", "answered")) },
			want:  `{"where":{"status":"answered"}}`,
		},
		{
			name:  "two filters on different fields are one object",
			build: func() *Query[Call] { return calls.Where(Eq("status", "a"), Gte("ts", "2026-11-01")) },
			want:  `{"where":{"status":"a","ts":{"$gte":"2026-11-01"}}}`,
		},
		{
			name:  "two filters on one field become $and",
			build: func() *Query[Call] { return calls.Where(Gt("duration", 1), Lt("duration", 5)) },
			want:  `{"where":{"$and":[{"duration":{"$gt":1}},{"duration":{"$lt":5}}]}}`,
		},
		{
			name:  "ne lte",
			build: func() *Query[Call] { return calls.Where(Ne("status", "x"), Lte("n", 9)) },
			want:  `{"where":{"n":{"$lte":9},"status":{"$ne":"x"}}}`,
		},
		{
			name:  "in",
			build: func() *Query[Call] { return calls.Where(In("dnis", "1133", "1144")) },
			want:  `{"where":{"dnis":{"$in":["1133","1144"]}}}`,
		},
		{
			name:  "exists false",
			build: func() *Query[Call] { return calls.Where(Exists("dnis", false)) },
			want:  `{"where":{"dnis":{"$exists":false}}}`,
		},
		{
			name:  "or",
			build: func() *Query[Call] { return calls.Where(Or(Eq("a", 1), Eq("b", 2))) },
			want:  `{"where":{"$or":[{"a":1},{"b":2}]}}`,
		},
		{
			name:  "not",
			build: func() *Query[Call] { return calls.Where(Not(Eq("a", 1))) },
			want:  `{"where":{"$not":{"a":1}}}`,
		},
		{
			name:  "sort keeps the key order",
			build: func() *Query[Call] { return calls.Where().Sort(Desc("ts"), Asc("call_id")) },
			want:  `{"sort":{"ts":-1,"call_id":1}}`,
		},
		{
			name:  "limit and skip",
			build: func() *Query[Call] { return calls.Where().Limit(50).Skip(100) },
			want:  `{"limit":50,"skip":100}`,
		},
		{
			name:  "fields",
			build: func() *Query[Call] { return calls.Where().Fields("status", "dnis") },
			want:  `{"fields":["status","dnis"]}`,
		},
		{
			name:  "page 3 of 50 is skip 100",
			build: func() *Query[Call] { return calls.Where().Page(3, 50) },
			want:  `{"limit":50,"skip":100}`,
		},
		{
			name:  "after",
			build: func() *Query[Call] { return calls.Where().After("tok") },
			want:  `{"after":"tok"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.build().body())
			if err != nil {
				t.Fatal(err)
			}

			if string(raw) != tc.want {
				t.Fatalf("got  %s\nwant %s", raw, tc.want)
			}
		})
	}
}

// seedCalls declares Call and inserts five rows, with ids c1 to c5.
func seedCalls(t *testing.T) *Coll[Call] {
	t.Helper()

	ctx := context.Background()
	db := openTest(t, daemon.unix)

	calls, err := Collection[Call](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	day := func(d int) time.Time {
		return time.Date(2026, 11, d, 10, 0, 0, 0, time.UTC)
	}

	rows := []Call{
		{CallID: "c1", Status: "answered", DNIS: "1133", Duration: 10, TS: day(1)},
		{CallID: "c2", Status: "answered", DNIS: "1144", Duration: 20, TS: day(2)},
		{CallID: "c3", Status: "missed", DNIS: "1133", Duration: 0, TS: day(3)},
		{CallID: "c4", Status: "answered", DNIS: "1133", Duration: 30, TS: day(4)},
		{CallID: "c5", Status: "failed", DNIS: "1155", Duration: 5, TS: day(5)},
	}

	if _, err := calls.Insert(ctx, rows...); err != nil {
		t.Fatal(err)
	}

	return calls
}

func ids(calls []Call) []string {
	out := make([]string, len(calls))

	for i, c := range calls {
		out[i] = c.CallID
	}

	return out
}

func TestQueryFilters(t *testing.T) {
	ctx := context.Background()
	calls := seedCalls(t)

	cases := []struct {
		name  string
		query *Query[Call]
		want  []string
	}{
		{name: "eq", query: calls.Where(Eq("status", "answered")), want: []string{"c1", "c2", "c4"}},
		{name: "or", query: calls.Where(Or(Eq("status", "missed"), Eq("status", "failed"))), want: []string{"c3", "c5"}},
		{name: "not", query: calls.Where(Not(Eq("status", "answered"))), want: []string{"c3", "c5"}},
		{name: "in", query: calls.Where(In("dnis", "1144", "1155")), want: []string{"c2", "c5"}},
		{name: "two filters", query: calls.Where(Eq("dnis", "1133"), Gte("duration", 10)), want: []string{"c1", "c4"}},
		{name: "two filters on one field", query: calls.Where(Gt("duration", 5), Lt("duration", 30)), want: []string{"c1", "c2"}},
		{name: "exists", query: calls.Where(Exists("duration", true)), want: []string{"c1", "c2", "c3", "c4", "c5"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.query.All(ctx)
			if err != nil {
				t.Fatal(err)
			}

			gotIDs := ids(got)

			slices.Sort(gotIDs)

			if !slices.Equal(gotIDs, tc.want) {
				t.Fatalf("got %v, want %v", gotIDs, tc.want)
			}
		})
	}
}

func TestQuerySortLimitSkipFields(t *testing.T) {
	ctx := context.Background()
	calls := seedCalls(t)

	got, err := calls.Where().Sort(Desc("duration")).Limit(2).Skip(1).All(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"c2", "c1"}; !slices.Equal(ids(got), want) {
		t.Fatalf("sort+limit+skip: got %v, want %v", ids(got), want)
	}

	fields, err := calls.Where(Eq("call_id", "c3")).Fields("status").All(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(fields) != 1 || fields[0].Status != "missed" || fields[0].CallID != "" {
		t.Fatalf("fields: got %+v, want only status", fields)
	}
}

func TestQueryFirstAndCount(t *testing.T) {
	ctx := context.Background()
	calls := seedCalls(t)

	first, err := calls.Where(Eq("status", "missed")).First(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if first.CallID != "c3" {
		t.Fatalf("first: got %s, want c3", first.CallID)
	}

	if _, err := calls.Where(Eq("status", "none")).First(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first with no row: want ErrNotFound, got %v", err)
	}

	total, err := calls.Where(Eq("status", "answered")).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if total != 3 {
		t.Fatalf("count: got %d, want 3", total)
	}

	total, err = calls.Where(Eq("status", "answered")).Limit(1).Count(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if total != 3 {
		t.Fatalf("count with limit 1: got %d, want 3 (limit does not apply to count)", total)
	}
}

func TestQueryIterStopsEarly(t *testing.T) {
	ctx := context.Background()
	calls := seedCalls(t)

	var seen []string

	for c, err := range calls.Where().Sort(Asc("call_id")).Iter(ctx) {
		if err != nil {
			t.Fatal(err)
		}

		seen = append(seen, c.CallID)

		if len(seen) == 2 {
			break
		}
	}

	if want := []string{"c1", "c2"}; !slices.Equal(seen, want) {
		t.Fatalf("iter: got %v, want %v", seen, want)
	}
}

func TestQueryPageAndCursor(t *testing.T) {
	ctx := context.Background()
	calls := seedCalls(t)

	page, err := calls.Where().Sort(Asc("call_id")).Page(2, 2).All(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"c3", "c4"}; !slices.Equal(ids(page), want) {
		t.Fatalf("page 2 of 2: got %v, want %v", ids(page), want)
	}

	var walked []string

	cursor := ""

	for range 10 {
		q := calls.Where().Sort(Asc("call_id")).Limit(2)

		if cursor != "" {
			q.After(cursor)
		}

		rows, err := q.All(ctx)
		if err != nil {
			t.Fatal(err)
		}

		walked = append(walked, ids(rows)...)

		cursor = q.NextCursor()

		if cursor == "" {
			break
		}
	}

	if want := []string{"c1", "c2", "c3", "c4", "c5"}; !slices.Equal(walked, want) {
		t.Fatalf("cursor walk: got %v, want %v", walked, want)
	}
}

func TestCursorBoundToQuery(t *testing.T) {
	ctx := context.Background()
	calls := seedCalls(t)

	q := calls.Where().Sort(Asc("call_id")).Limit(2)

	if _, err := q.All(ctx); err != nil {
		t.Fatal(err)
	}

	other := calls.Where().Sort(Desc("call_id")).Limit(2).After(q.NextCursor())

	if _, err := other.All(ctx); !errors.Is(err, ErrCursorMismatch) {
		t.Fatalf("cursor for another sort: want ErrCursorMismatch, got %v", err)
	}
}
