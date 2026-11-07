package server_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/engine"
)

func seedCalls(t *testing.T) http.Handler {
	t.Helper()

	_, h := newHandler(t)

	rec := call(h, "PUT", "/calls", `{"time":"ts","indexes":["status"],"partition_by":"month"}`)

	expectStatus(t, rec, http.StatusOK)

	docs := `[
		{"_id":"a","ts":"2026-09-10T10:00:00Z","status":"answered","n":1},
		{"_id":"b","ts":"2026-10-10T10:00:00Z","status":"missed","n":2},
		{"_id":"c","ts":"2026-10-20T10:00:00Z","status":"answered","n":3},
		{"_id":"d","ts":"2026-11-01T10:00:00Z","status":"missed","n":4}
	]`

	expectStatus(t, call(h, "POST", "/calls/_bulk", `{"docs":`+docs+`}`), http.StatusOK)

	return h
}

func TestPartitionRouting(t *testing.T) {
	h := seedCalls(t)

	rec := call(h, "GET", "/calls", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"partitions":["calls-2026-09","calls-2026-10","calls-2026-11"]`) {
		t.Fatalf("get = %s", rec.Body.String())
	}

	rows := rowsOf(t, call(h, "POST", "/calls/_search", `{"query":{"status":"answered"},"sort":{"ts":1}}`).Body.String())

	if got := ids(rows); !slices.Equal(got, []string{"a", "c"}) || rows[1].Coll != "calls-2026-10" {
		t.Fatalf("search = %+v", rows)
	}

	if got := strings.TrimSpace(call(h, "POST", "/calls/_count", `{"query":{"status":"missed"}}`).Body.String()); got != `{"count":2}` {
		t.Fatalf("count = %s", got)
	}

	agg := call(h, "POST", "/calls/_aggregate", `{"pipeline":[{"$group":{"_id":"$status","n":{"$sum":"$n"}}}]}`).Body.String()

	if strings.Fields(agg)[0] != `{"_id":"answered","n":4}` {
		t.Fatalf("aggregate = %s", agg)
	}

	// The partition of one document, by its time field.
	expectStatus(t, call(h, "POST", "/calls/_doc", `{"_id":"e","ts":"2026-12-24T10:00:00Z","status":"answered"}`), http.StatusOK)
	expectStatus(t, call(h, "GET", "/calls-2026-12/_doc/e", ""), http.StatusOK)

	// A document without the time field.
	expectStatus(t, call(h, "POST", "/calls/_doc", `{"_id":"x"}`), http.StatusBadRequest)
}

func TestPartitionByID(t *testing.T) {
	h := seedCalls(t)

	if doc := decodeMap(t, call(h, "GET", "/calls/_doc/b", "").Body.String()); doc["n"] != float64(2) {
		t.Fatalf("get b = %v", doc)
	}

	rec := call(h, "POST", "/calls/_update/b", `{"update":{"$inc":{"n":10}}}`)

	if got := decodeMap(t, rec.Body.String()); got["matched"] != float64(1) {
		t.Fatalf("update b = %s", rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/calls/_doc/nope", ""), http.StatusNotFound)

	if got := strings.TrimSpace(call(h, "POST", "/calls/_update/nope", `{"update":{"$set":{"x":1}}}`).Body.String()); got != `{"matched":0,"modified":0}` {
		t.Fatalf("update missing = %s", got)
	}

	// Upsert by id creates the document in the partition of its time.
	call(h, "POST", "/calls/_update/z", `{"update":{"$set":{"ts":"2026-08-05T00:00:00Z","status":"new"}},"upsert":true}`)
	expectStatus(t, call(h, "GET", "/calls-2026-08/_doc/z", ""), http.StatusOK)

	if got := strings.TrimSpace(call(h, "DELETE", "/calls/_doc/a", "").Body.String()); got != `{"deleted":1}` {
		t.Fatalf("delete a = %s", got)
	}

	// Upsert in _bulk merges in the partition of the time field.
	rec = call(h, "POST", "/calls/_bulk", `{"docs":[{"_id":"c","ts":"2026-10-20T10:00:00Z","status":"done"}],"upsert":true}`)

	if !strings.Contains(rec.Body.String(), `"_v":2`) {
		t.Fatalf("bulk upsert = %s", rec.Body.String())
	}
}

func TestPartitionByQueryAndAdmin(t *testing.T) {
	h := seedCalls(t)

	rec := call(h, "POST", "/calls/_update_by_query", `{"query":{"status":"missed"},"update":{"$set":{"seen":true}},"many":true}`)

	if got := decodeMap(t, rec.Body.String()); got["matched"] != float64(2) {
		t.Fatalf("update many = %s", rec.Body.String())
	}

	rec = call(h, "POST", "/calls/_delete_by_query", `{"query":{"status":"answered"},"many":true}`)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"deleted":2}` {
		t.Fatalf("delete many = %s", got)
	}

	expectStatus(t, call(h, "PUT", "/calls/_indexes/n", ""), http.StatusOK)

	if !strings.Contains(call(h, "GET", "/calls-2026-09", "").Body.String(), `"n"`) {
		t.Fatal("the index must reach every partition")
	}

	if !strings.Contains(call(h, "GET", "/calls/_stats", "").Body.String(), `"rows":2`) {
		t.Fatal("stats must sum the partitions")
	}

	expectStatus(t, call(h, "POST", "/calls/_watch", `{"consumer":"c"}`), http.StatusBadRequest)

	expectStatus(t, call(h, "POST", "/calls/_truncate", ""), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/calls/_truncate?confirm=calls", ""), http.StatusOK)

	if got := strings.TrimSpace(call(h, "POST", "/calls/_count", "").Body.String()); got != `{"count":0}` {
		t.Fatalf("count after truncate = %s", got)
	}

	expectStatus(t, call(h, "DELETE", "/calls?confirm=calls", ""), http.StatusOK)
	expectStatus(t, call(h, "GET", "/calls", ""), http.StatusNotFound)
}

func TestPartitionRetention(t *testing.T) {
	srv, h := newHandler(t)

	_ = srv

	expectStatus(t, call(h, "PUT", "/logs", `{"time":"ts","partition_by":"day","retention":"2d"}`), http.StatusOK)

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -10).Format(time.RFC3339)
	fresh := now.Format(time.RFC3339)

	expectStatus(t, call(h, "POST", "/logs/_bulk", `{"docs":[{"_id":"old","ts":"`+old+`"},{"_id":"new","ts":"`+fresh+`"}]}`), http.StatusOK)

	// The old partition is out of reads at once.
	if got := strings.TrimSpace(call(h, "POST", "/logs/_count", "").Body.String()); got != `{"count":1}` {
		t.Fatalf("count = %s", got)
	}

	if got := engine.PeriodOf("day", now.AddDate(0, 0, -10)); !strings.Contains(call(h, "GET", "/logs", "").Body.String(), got) {
		t.Fatalf("the old partition is still listed until the sweep: %s", call(h, "GET", "/logs", "").Body.String())
	}
}

func TestPeriodNames(t *testing.T) {
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)

	for by, want := range map[string]string{"day": "2026-11-07", "week": "2026-w45", "month": "2026-11", "year": "2026"} {
		if got := engine.PeriodOf(by, at); got != want {
			t.Fatalf("%s = %s, want %s", by, got, want)
		}
	}
}
