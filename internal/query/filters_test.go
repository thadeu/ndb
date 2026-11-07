package query_test

import (
	"errors"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/query"
)

// Thursday 2026-11-05 14:30:15 UTC.
var fixedNow = time.Date(2026, 11, 5, 14, 30, 15, 0, time.UTC)

func parseAt(t *testing.T, body string, now time.Time) (*query.Query, error) {
	t.Helper()

	return query.Parse([]byte(body), query.Options{TimeField: "at", Now: func() time.Time { return now }})
}

func matchAt(t *testing.T, where, doc string) bool {
	t.Helper()

	q, err := parseAt(t, `{"where":`+where+`}`, fixedNow)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}

	return q.Match(decode(t, doc))
}

func TestDateMath(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"now", "2026-11-05T14:30:15Z"},
		{"now-7d", "2026-10-29T14:30:15Z"},
		{"now-7d/d", "2026-10-29T00:00:00Z"},
		{"now+1h", "2026-11-05T15:30:15Z"},
		{"now-90m", "2026-11-05T13:00:15Z"},
		{"now-30s/m", "2026-11-05T14:29:00Z"},
		{"now/h", "2026-11-05T14:00:00Z"},
		{"now/w", "2026-11-02T00:00:00Z"},
		{"now/M", "2026-11-01T00:00:00Z"},
		{"now-1M/M", "2026-10-01T00:00:00Z"},
		{"now/y", "2026-01-01T00:00:00Z"},
		{"now-1y+2d", "2025-11-07T14:30:15Z"},
	}

	for _, c := range cases {
		doc := `{"at":"` + c.want + `"}`

		if !matchAt(t, `{"at":"`+c.expr+`"}`, doc) {
			t.Errorf("%s: does not equal %s", c.expr, c.want)
		}
	}

	if !matchAt(t, `{"at":{"$gte":"now-7d/d","$lt":"now"}}`, `{"at":"2026-11-01T10:00:00Z"}`) {
		t.Error("range: in window")
	}

	if matchAt(t, `{"at":{"$gte":"now-7d/d"}}`, `{"at":"2026-10-28T23:59:59Z"}`) {
		t.Error("range: before window")
	}

	if !matchAt(t, `{"at":{"$in":["now/d"]}}`, `{"at":"2026-11-05T00:00:00Z"}`) {
		t.Error("$in with date math")
	}

	// Not the time field: the string stays a string.
	if !matchAt(t, `{"label":"now-1h"}`, `{"label":"now-1h"}`) {
		t.Error("non-time field must compare the literal string")
	}
}

func TestDateMathErrors(t *testing.T) {
	for _, expr := range []string{"now-7x", "now--1d", "now-d", "now-1", "now/", "now/dd", "now/x", "nowish"} {
		_, err := parseAt(t, `{"where":{"at":{"$gte":"`+expr+`"}}}`, fixedNow)

		var qe *query.Error
		if !errors.As(err, &qe) || qe.Code() != "invalid_query" {
			t.Errorf("%s: err = %v, want invalid_query", expr, err)
		}
	}
}

func TestDateMathIsIndexable(t *testing.T) {
	q, err := parseAt(t, `{"where":{"at":{"$gte":"now-1h"}}}`, fixedNow)
	if err != nil {
		t.Fatal(err)
	}

	conds := q.Indexable()

	if len(conds) != 1 || conds[0].Vals[0].Kind != query.Time {
		t.Fatalf("conds = %+v", conds)
	}
}

func TestCursorKeepsNow(t *testing.T) {
	body := `{"where":{"at":{"$gte":"now-1h"}},"sort":{"at":1}}`

	first, err := parseAt(t, body, fixedNow)
	if err != nil {
		t.Fatal(err)
	}

	tok := first.EncodeCursor(query.Pos{Keys: []query.Value{{}}, ID: "x"}, "")

	later := fixedNow.Add(10 * time.Minute)
	next, err := parseAt(t, `{"where":{"at":{"$gte":"now-1h"}},"sort":{"at":1},"after":"`+tok+`"}`, later)
	if err != nil {
		t.Fatal(err)
	}

	// 13:31 is in the window of the first page (from 13:30:15), and out of
	// a window read at 14:40:15 (from 13:40:15).
	if !next.Match(decode(t, `{"at":"2026-11-05T13:31:00Z"}`)) {
		t.Fatal("the next page must use the now of the first page")
	}
}

func TestElemMatch(t *testing.T) {
	items := `{"items":[{"sku":"a","qty":1},{"sku":"b","qty":5}]}`

	cases := []struct {
		name, where, doc string
		want             bool
	}{
		{"dot paths match across elements", `{"items.sku":"a","items.qty":{"$gt":2}}`, items, true},
		{"elemMatch needs one element", `{"items":{"$elemMatch":{"sku":"a","qty":{"$gt":2}}}}`, items, false},
		{"elemMatch hit", `{"items":{"$elemMatch":{"sku":"b","qty":{"$gt":2}}}}`, items, true},
		{"elemMatch with $or", `{"items":{"$elemMatch":{"$or":[{"sku":"z"},{"qty":5}]}}}`, items, true},
		{"scalar range one element", `{"n":{"$elemMatch":{"$gte":10,"$lt":20}}}`, `{"n":[5,25]}`, false},
		{"scalar range hit", `{"n":{"$elemMatch":{"$gte":10,"$lt":20}}}`, `{"n":[5,15]}`, true},
		{"scalar $in", `{"tags":{"$elemMatch":{"$in":["x","y"]}}}`, `{"tags":["a","y"]}`, true},
		{"not an array", `{"n":{"$elemMatch":{"$gte":10}}}`, `{"n":15}`, false},
		{"missing", `{"n":{"$elemMatch":{"$gte":10}}}`, `{}`, false},
		{"nested", `{"orders":{"$elemMatch":{"lines":{"$elemMatch":{"sku":"a","qty":2}}}}}`, `{"orders":[{"lines":[{"sku":"a","qty":1},{"sku":"b","qty":2}]},{"lines":[{"sku":"a","qty":2}]}]}`, true},
		{"nested miss", `{"orders":{"$elemMatch":{"lines":{"$elemMatch":{"sku":"a","qty":2}}}}}`, `{"orders":[{"lines":[{"sku":"a","qty":1},{"sku":"b","qty":2}]}]}`, false},
		{"dot path to array", `{"a.items":{"$elemMatch":{"sku":"a"}}}`, `{"a":{"items":[{"sku":"a"}]}}`, true},
		{"under $not", `{"$not":{"items":{"$elemMatch":{"sku":"a"}}}}`, items, false},
	}

	for _, c := range cases {
		if got := match(t, c.where, c.doc); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestElemMatchErrors(t *testing.T) {
	for _, where := range []string{
		`{"items":{"$elemMatch":{}}}`,
		`{"items":{"$elemMatch":1}}`,
		`{"items":{"$elemMatch":{"sku":"a"},"$size":1}}`,
		`{"items":{"$gt":1,"$elemMatch":{"sku":"a"}}}`,
		`{"items":{"$elemMatch":{"$bad":1}}}`,
	} {
		if _, err := query.Parse([]byte(`{"where":`+where+`}`), query.Options{}); err == nil {
			t.Errorf("%s: want an error", where)
		}
	}
}

func TestElemMatchNeedsDoc(t *testing.T) {
	q, err := query.Parse([]byte(`{"where":{"status":"a","items":{"$elemMatch":{"sku":"a"}}}}`), query.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !q.NeedsDoc() {
		t.Fatal("NeedsDoc = false")
	}

	if _, ok := q.IndexOnly(); ok {
		t.Fatal("IndexOnly must be false with $elemMatch")
	}

	if conds := q.Indexable(); len(conds) != 1 || conds[0].Path != "status" {
		t.Fatalf("conds = %+v", conds)
	}
}
