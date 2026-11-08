package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDateMathRoute(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/events", `{"time":"at","indexes":["at"]}`), http.StatusOK)

	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

	docs := `[{"_id":"old","at":"` + at(10*24*time.Hour) + `"},{"_id":"day","at":"` + at(20*time.Hour) + `"},{"_id":"hour","at":"` + at(30*time.Minute) + `"}]`

	expectStatus(t, call(h, "POST", "/events/_bulk", `{"docs":`+docs+`}`), http.StatusOK)

	// Two docs sealed in a segment, one in the memtable.
	expectStatus(t, call(h, "POST", "/events/_compact", ``), http.StatusOK)
	expectStatus(t, call(h, "POST", "/events/_bulk", `{"docs":[{"_id":"now","at":"`+at(time.Second)+`"}]}`), http.StatusOK)

	find := func(where string) []string {
		t.Helper()

		rec := call(h, "POST", "/events/_search", `{"query":`+where+`,"sort":{"at":1}}`)

		expectStatus(t, rec, http.StatusOK)

		return ids(rowsOf(t, rec.Body.String()))
	}

	if got := find(`{"at":{"$gte":"now-1h"}}`); !slices.Equal(got, []string{"hour", "now"}) {
		t.Fatalf("now-1h = %v", got)
	}

	if got := find(`{"at":{"$gte":"now-7d","$lt":"now-1h"}}`); !slices.Equal(got, []string{"day"}) {
		t.Fatalf("now-7d..now-1h = %v", got)
	}

	rec := call(h, "POST", "/events/_search", `{"query":{"at":{"$gte":"now-1h"}}}`)

	if got := rec.Header().Get("X-Ndb-Plan"); got != "index:at" {
		t.Fatalf("plan = %s", got)
	}

	rec = call(h, "POST", "/events/_search", `{"query":{"at":{"$gte":"now-1x"}}}`)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "invalid_query" {
		t.Fatalf("code = %s", got)
	}

	rec = call(h, "POST", "/events/_aggregate", `{"pipeline":[{"$match":{"at":{"$gte":"now-1d"}}},{"$group":{"_id":null,"n":{"$count":{}}}}]}`)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"_id":null,"n":3}` {
		t.Fatalf("aggregate = %s", got)
	}

	rec = call(h, "POST", "/events/_delete_by_query", `{"query":{"at":{"$lt":"now-7d"}},"many":true}`)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"deleted":1}` {
		t.Fatalf("delete = %s", got)
	}

	// Pages over a time window: each row once.
	if got := ids(walk(t, h, "/events/_search", `{"query":{"at":{"$gte":"now-1d"}},"sort":{"at":-1},"limit":1}`)); !slices.Equal(got, []string{"now", "hour", "day"}) {
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

	expectStatus(t, call(h, "PUT", "/orders", `{"indexes":["items.sku"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":`+docs+`}`), http.StatusOK)
	indexesReady(t, h, "orders")

	for _, sealed := range []bool{false, true} {
		if sealed {
			expectStatus(t, call(h, "POST", "/orders/_compact", ``), http.StatusOK)
		}

		find := func(where string) []string {
			t.Helper()

			rec := call(h, "POST", "/orders/_search", `{"query":`+where+`}`)

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

		if plan := call(h, "POST", "/orders/_search", `{"query":`+where+`}`).Header().Get("X-Ndb-Plan"); plan != "index:items.sku" {
			t.Fatalf("sealed=%v: plan = %s", sealed, plan)
		}
	}

	rec := call(h, "POST", "/orders/_update_by_query", `{"query":{"items.sku":"a","items":{"$elemMatch":{"sku":"a","qty":1}}},"update":{"$set":{"flag":true}},"many":true}`)

	if got := strings.TrimSpace(rec.Body.String()); !strings.Contains(got, `"matched":1`) {
		t.Fatalf("update = %s", got)
	}
}

// indexesReady waits for the background build of the indexes of coll.
func indexesReady(t *testing.T, h http.Handler, coll string) {
	t.Helper()

	type index struct {
		State string `json:"state"`
	}

	for range 500 {
		var body struct {
			Indexes []index `json:"indexes"`
		}

		_ = json.Unmarshal(call(h, "GET", "/"+coll, ``).Body.Bytes(), &body)

		if !slices.ContainsFunc(body.Indexes, func(ix index) bool { return ix.State != "ready" }) {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("the indexes of %s are not ready", coll)
}
