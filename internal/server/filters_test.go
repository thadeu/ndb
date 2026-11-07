package server_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDateMathRoute(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/events", `{"time":"at","indexes":["at"]}`), http.StatusOK)

	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

	docs := `[{"_id":"old","at":"` + at(10*24*time.Hour) + `"},{"_id":"day","at":"` + at(20*time.Hour) + `"},{"_id":"hour","at":"` + at(30*time.Minute) + `"}]`

	expectStatus(t, call(h, "POST", "/v1/c/events/insert", `{"insert":`+docs+`}`), http.StatusOK)

	// Two docs sealed in a segment, one in the memtable.
	expectStatus(t, call(h, "POST", "/v1/c/events/compact", ``), http.StatusOK)
	expectStatus(t, call(h, "POST", "/v1/c/events/insert", `{"insert":[{"_id":"now","at":"`+at(time.Second)+`"}]}`), http.StatusOK)

	find := func(where string) []string {
		t.Helper()

		rec := call(h, "POST", "/v1/c/events/find", `{"where":`+where+`,"sort":{"at":1}}`)

		expectStatus(t, rec, http.StatusOK)

		return ids(rowsOf(t, rec.Body.String()))
	}

	if got := find(`{"at":{"$gte":"now-1h"}}`); !slices.Equal(got, []string{"hour", "now"}) {
		t.Fatalf("now-1h = %v", got)
	}

	if got := find(`{"at":{"$gte":"now-7d","$lt":"now-1h"}}`); !slices.Equal(got, []string{"day"}) {
		t.Fatalf("now-7d..now-1h = %v", got)
	}

	rec := call(h, "POST", "/v1/c/events/find", `{"where":{"at":{"$gte":"now-1h"}}}`)

	if got := rec.Header().Get("X-Ndb-Plan"); got != "index:at" {
		t.Fatalf("plan = %s", got)
	}

	rec = call(h, "POST", "/v1/c/events/find", `{"where":{"at":{"$gte":"now-1x"}}}`)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "invalid_query" {
		t.Fatalf("code = %s", got)
	}

	rec = call(h, "POST", "/v1/c/events/aggregate", `{"pipeline":[{"$match":{"at":{"$gte":"now-1d"}}},{"$group":{"_id":null,"n":{"$count":{}}}}]}`)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"_id":null,"n":3}` {
		t.Fatalf("aggregate = %s", got)
	}

	rec = call(h, "POST", "/v1/c/events/delete?many=true", `{"at":{"$lt":"now-7d"}}`)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"deleted":1}` {
		t.Fatalf("delete = %s", got)
	}

	// Pages over a time window: each row once.
	if got := ids(walk(t, h, "/v1/c/events/find", `{"where":{"at":{"$gte":"now-1d"}},"sort":{"at":-1},"limit":1}`)); !slices.Equal(got, []string{"now", "hour", "day"}) {
		t.Fatalf("pages = %v", got)
	}
}

func TestElemMatchRoute(t *testing.T) {
	_, h := newHandler(t)

	docs := `[
		{"_id":"o1","items":[{"sku":"a","qty":1},{"sku":"b","qty":5}]},
		{"_id":"o2","items":[{"sku":"a","qty":3}]},
		{"_id":"o3","items":[{"sku":"c","qty":9}],"tags":[5,25]},
		{"_id":"o4","tags":[5,15]}
	]`

	expectStatus(t, call(h, "PUT", "/v1/c/orders", `{"indexes":["items.sku"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/v1/c/orders/insert", `{"insert":`+docs+`}`), http.StatusOK)

	for _, sealed := range []bool{false, true} {
		if sealed {
			expectStatus(t, call(h, "POST", "/v1/c/orders/compact", ``), http.StatusOK)
		}

		find := func(where string) []string {
			t.Helper()

			rec := call(h, "POST", "/v1/c/orders/find", `{"where":`+where+`}`)

			expectStatus(t, rec, http.StatusOK)

			return ids(rowsOf(t, rec.Body.String()))
		}

		if got := find(`{"items":{"$elemMatch":{"sku":"a","qty":{"$gt":2}}}}`); !slices.Equal(got, []string{"o2"}) {
			t.Fatalf("sealed=%v: elemMatch = %v", sealed, got)
		}

		if got := find(`{"items.sku":"a","items.qty":{"$gt":2}}`); !slices.Equal(got, []string{"o1", "o2"}) {
			t.Fatalf("sealed=%v: dot paths = %v", sealed, got)
		}

		if got := find(`{"tags":{"$elemMatch":{"$gte":10,"$lt":20}}}`); !slices.Equal(got, []string{"o4"}) {
			t.Fatalf("sealed=%v: scalar elemMatch = %v", sealed, got)
		}

		// The index on items.sku gives the candidates; $elemMatch checks each.
		where := `{"items.sku":"a","items":{"$elemMatch":{"sku":"a","qty":{"$gt":2}}}}`

		if got := find(where); !slices.Equal(got, []string{"o2"}) {
			t.Fatalf("sealed=%v: with an index condition = %v", sealed, got)
		}

		if plan := call(h, "POST", "/v1/c/orders/find", `{"where":`+where+`}`).Header().Get("X-Ndb-Plan"); plan != "index:items.sku" {
			t.Fatalf("sealed=%v: plan = %s", sealed, plan)
		}
	}

	rec := call(h, "POST", "/v1/c/orders/update?many=true", `{"items.sku":"a","items":{"$elemMatch":{"sku":"a","qty":1}},"$set":{"flag":true}}`)

	if got := strings.TrimSpace(rec.Body.String()); !strings.Contains(got, `"matched":1`) {
		t.Fatalf("update = %s", got)
	}
}
