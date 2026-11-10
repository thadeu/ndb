package server_test

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/engine"
)

// Contract tests of partitioned collections (partition_by), the
// "Partitioned collections" part of docs/dev/http/collections.md, and of
// collection patterns over partitions.

// cpSeed is seedCalls: "calls" by month, with a (2026-09), b and c
// (2026-10), d (2026-11).
func cpSeed(t *testing.T) http.Handler {
	t.Helper()

	return seedCalls(t)
}

// cpCount is the count of a _count body.
func cpCount(t *testing.T, h http.Handler, path, body string) string {
	t.Helper()

	return strings.TrimSpace(call(h, "POST", path, body).Body.String())
}

// cpPartitions is the partitions of a GET on a logical name.
func cpPartitions(t *testing.T, h http.Handler, name string) []string {
	t.Helper()

	rec := call(h, "GET", "/"+name, "")

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Partitions []string `json:"partitions"`
	}

	decodeBody(t, rec.Body.String(), &body)

	return body.Partitions
}

// The definition rules of partition_by: it needs time and engine mode, a
// period of day, week, month or year, and a name of at most 53 characters.
func TestContractPartitionDefinitionRules(t *testing.T) {
	_, h := newHandler(t)

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"no time", "PUT", "/p", `{"partition_by":"month"}`, bad, "catalog_invalid"},
		{"bad period", "PUT", "/p", `{"time":"ts","partition_by":"hour"}`, bad, "catalog_invalid"},
		{"direct", "PUT", "/p", `{"time":"ts","partition_by":"month","write":"direct","path":"/tmp/x/*.ndjson"}`, bad, "catalog_invalid"},
		{"54 characters", "PUT", "/" + strings.Repeat("p", 54), `{"time":"ts","partition_by":"month"}`, bad, "catalog_invalid"},
		{"stream", "PUT", "/p", `{"stream":true,"partition_by":"day"}`, bad, "catalog_invalid"},
	})

	rec := call(h, "PUT", "/"+strings.Repeat("p", 53), `{"time":"ts","partition_by":"month"}`)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"partitions":[]`) {
		t.Fatalf("a new logical collection has no partitions: %s", rec.Body.String())
	}

	// partition_by cannot change.
	expectStatus(t, call(h, "PUT", "/q", `{"time":"ts","partition_by":"month"}`), http.StatusOK)
	ccExpect(t, call(h, "PUT", "/q", `{"partition_by":"day"}`), http.StatusConflict, "catalog_conflict")
}

// partition_by cannot be added to a collection that exists: 409
// catalog_conflict. Else its documents would hide behind a logical name.
func TestContractPartitionByOnAnExistingCollection(t *testing.T) {
	t.Skip("BUG: PUT {partition_by} on an existing plain collection is 200; its documents are no longer readable by the name")

	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/p", `{"time":"ts"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/p/_doc", `{"_id":"1","ts":"2026-01-01T00:00:00Z"}`), http.StatusOK)

	ccExpect(t, call(h, "PUT", "/p", `{"partition_by":"month"}`), http.StatusConflict, "catalog_conflict")

	if got := cpCount(t, h, "/p/_count", `{}`); got != `{"count":1}` {
		t.Fatalf("count = %s", got)
	}
}

// PUT on the logical name again (partEnsure): idempotent, merges, and gives
// new indexes and unique fields to every partition.
func TestContractPartitionEnsureAgain(t *testing.T) {
	h := cpSeed(t)

	rec := call(h, "PUT", "/calls", `{"time":"ts","partition_by":"month","indexes":["status"]}`)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"partitions":["calls-2026-09","calls-2026-10","calls-2026-11"]`) {
		t.Fatalf("again = %s", rec.Body.String())
	}

	rec = call(h, "PUT", "/calls", `{"indexes":["n"]}`)

	expectStatus(t, rec, http.StatusOK)

	def := decodeMap(t, rec.Body.String())["definition"].(map[string]any)

	if fmt.Sprint(def["indexes"]) != "[n status]" || def["partition_by"] != "month" {
		t.Fatalf("merged = %v", def)
	}

	for _, p := range []string{"calls-2026-09", "calls-2026-10", "calls-2026-11"} {
		body := call(h, "GET", "/"+p, "").Body.String()

		if !strings.Contains(body, `"field":"n"`) || !strings.Contains(body, `"partition_of":"calls"`) {
			t.Errorf("%s = %s", p, body)
		}
	}

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"not JSON", "PUT", "/calls", `{"indexes":`, bad, "invalid_request"},
		{"unknown key", "PUT", "/calls", `{"bogus":1}`, bad, "invalid_request"},
		{"id key", "PUT", "/calls", `{"id":"x"}`, bad, "catalog_invalid"},
		{"empty index", "PUT", "/calls", `{"indexes":[""]}`, bad, "catalog_invalid"},
		{"other time", "PUT", "/calls", `{"time":"at"}`, http.StatusConflict, "catalog_conflict"},
		{"other period", "PUT", "/calls", `{"partition_by":"year"}`, http.StatusConflict, "catalog_conflict"},
	})

	huge := `{"indexes":["` + strings.Repeat("a", 1<<20) + `"]}`

	ccExpect(t, call(h, "PUT", "/calls", huge), http.StatusRequestEntityTooLarge, "request_too_large")
}

// "PUT /{coll}: change the logical definition and every partition": a key
// set later (ttl here) reaches the partitions that exist.
func TestContractPartitionEnsureReachesEveryPartition(t *testing.T) {
	t.Skip("BUG: PUT on a logical name gives only indexes and unique to existing partitions; ttl, expires_field, durability, max_line_size, blob_threshold and pitr stay old")

	h := cpSeed(t)

	expectStatus(t, call(h, "PUT", "/calls", `{"ttl":"1h","durability":"relaxed"}`), http.StatusOK)

	def := decodeMap(t, call(h, "GET", "/calls-2026-10", "").Body.String())["definition"].(map[string]any)

	if def["ttl"] != "1h" || def["durability"] != "relaxed" {
		t.Fatalf("partition = %v", def)
	}
}

// The partition names of each period, from the time field (UTC).
func TestContractPartitionPeriods(t *testing.T) {
	_, h := newHandler(t)

	for by, want := range map[string]string{
		"day":   "d-day-2026-11-07",
		"week":  "d-week-2026-w45",
		"month": "d-month-2026-11",
		"year":  "d-year-2026",
	} {
		name := "d-" + by

		expectStatus(t, call(h, "PUT", "/"+name, `{"time":"at","partition_by":"`+by+`"}`), http.StatusOK)

		// 2026-11-07T23:30-02:00 is 2026-11-08 in UTC: the day is UTC.
		at := "2026-11-07T10:00:00Z"

		if by == "day" {
			expectStatus(t, call(h, "POST", "/"+name+"/_doc", `{"_id":"utc","at":"2026-11-07T23:30:00-02:00"}`), http.StatusOK)
			expectStatus(t, call(h, "GET", "/d-day-2026-11-08/_doc/utc", ""), http.StatusOK)
		}

		expectStatus(t, call(h, "POST", "/"+name+"/_doc", `{"_id":"x","at":"`+at+`"}`), http.StatusOK)
		expectStatus(t, call(h, "GET", "/"+want+"/_doc/x", ""), http.StatusOK)
	}
}

// _doc and _bulk: a document without the time field, or with a time that
// is not RFC 3339, is 400 invalid_doc.
func TestContractPartitionInsertErrors(t *testing.T) {
	h := cpSeed(t)

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"doc without time", "POST", "/calls/_doc", `{"_id":"x"}`, bad, "invalid_doc"},
		{"doc with a number time", "POST", "/calls/_doc", `{"_id":"x","ts":1}`, bad, "invalid_doc"},
		{"doc with a bad time", "POST", "/calls/_doc", `{"_id":"x","ts":"yesterday"}`, bad, "invalid_doc"},
		{"doc not an object", "POST", "/calls/_doc", `[1]`, bad, "invalid_doc"},
		{"bulk no docs", "POST", "/calls/_bulk", `{"docs":[]}`, bad, "invalid_request"},
		{"bulk not JSON", "POST", "/calls/_bulk", `{"docs":`, bad, "invalid_request"},
		{"bulk unknown key", "POST", "/calls/_bulk", `{"rows":[]}`, bad, "invalid_request"},
		{"bulk bad expire_in", "POST", "/calls/_bulk", `{"docs":[{"ts":"2026-10-01T00:00:00Z"}],"expire_in":"soon"}`, bad, "invalid_request"},
		{"bulk doc without time", "POST", "/calls/_bulk", `{"docs":[{"_id":"y","ts":"2026-12-01T00:00:00Z"},{"_id":"z"}]}`, bad, "invalid_doc"},
	})

	// A bulk with a bad document writes nothing.
	if got := cpCount(t, h, "/calls/_count", `{}`); got != `{"count":4}` {
		t.Fatalf("count = %s", got)
	}

	// The results keep the order of docs across partitions.
	rec := call(h, "POST", "/calls/_bulk", `{"docs":[{"_id":"n1","ts":"2026-11-02T00:00:00Z"},{"_id":"o1","ts":"2026-09-02T00:00:00Z"},{"_id":"n2","ts":"2026-11-03T00:00:00Z"}]}`)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"results":[{"_id":"n1","_v":1},{"_id":"o1","_v":1},{"_id":"n2","_v":1}]}` {
		t.Fatalf("bulk = %s", got)
	}

	expectStatus(t, call(h, "GET", "/calls-2026-09/_doc/o1", ""), http.StatusOK)
	expectStatus(t, call(h, "GET", "/calls/_doc/n2", ""), http.StatusOK)
}

// A refused write (a bulk with a bad document) leaves no trace: no new
// empty partition.
func TestContractPartitionRefusedBulkMakesNoPartition(t *testing.T) {
	t.Skip("BUG: a partitioned _bulk refused for a later bad document still creates the empty partition of the documents before it")

	h := cpSeed(t)

	ccExpect(t, call(h, "POST", "/calls/_bulk", `{"docs":[{"_id":"y","ts":"2026-12-01T00:00:00Z"},{"_id":"z"}]}`), http.StatusBadRequest, "invalid_doc")

	if got := cpPartitions(t, h, "calls"); slices.Contains(got, "calls-2026-12") {
		t.Fatalf("partitions = %v", got)
	}
}

// "_bulk (all or nothing)" (http.md, Consistency): a bulk that fails must
// write no document, also across partitions.
func TestContractPartitionBulkIsAllOrNothing(t *testing.T) {
	t.Skip("BUG: a partitioned _bulk that fails in a later partition (409 duplicate_id) keeps the documents of the partitions written before")

	h := cpSeed(t)

	rec := call(h, "POST", "/calls/_bulk", `{"docs":[{"_id":"new","ts":"2026-08-01T00:00:00Z"},{"_id":"d","ts":"2026-11-01T10:00:00Z"}]}`)

	ccExpect(t, rec, http.StatusConflict, "duplicate_id")

	expectStatus(t, call(h, "GET", "/calls/_doc/new", ""), http.StatusNotFound)
}

// The routes by id go to the partition that holds the id.
func TestContractPartitionRoutesByID(t *testing.T) {
	h := cpSeed(t)

	expectStatus(t, call(h, "PUT", "/sess", `{"time":"ts","partition_by":"month","expires_field":"exp"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/sess/_bulk", `{"docs":[{"_id":"s1","ts":"2026-10-01T00:00:00Z"},{"_id":"s2","ts":"2026-11-01T00:00:00Z"}]}`), http.StatusOK)

	rec := call(h, "POST", "/sess/_expire/s1", `{"in":"1h"}`)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"expires_at"`) {
		t.Fatalf("expire = %s", rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/sess/_ttl/s1", ""), http.StatusOK)
	expectStatus(t, call(h, "GET", "/sess-2026-10/_ttl/s1", ""), http.StatusOK)
	expectStatus(t, call(h, "POST", "/sess/_persist/s1", ""), http.StatusOK)

	ccExpect(t, call(h, "GET", "/sess/_ttl/nope", ""), http.StatusNotFound, "not_found")
	ccExpect(t, call(h, "POST", "/sess/_expire/nope", `{"in":"1h"}`), http.StatusNotFound, "not_found")
	ccExpect(t, call(h, "POST", "/sess/_persist/nope", ""), http.StatusNotFound, "not_found")
	ccExpect(t, call(h, "POST", "/calls/_expire/b", `{"in":"1h"}`), http.StatusBadRequest, "ttl_not_enabled")

	if got := strings.TrimSpace(call(h, "DELETE", "/calls/_doc/nope", "").Body.String()); got != `{"deleted":0}` {
		t.Fatalf("delete missing = %s", got)
	}

	// An update by id of a missing id: bad body is 400, no upsert matches
	// nothing.
	ccExpect(t, call(h, "POST", "/calls/_update/nope", `{"update":`), http.StatusBadRequest, "invalid_request")
	ccExpect(t, call(h, "POST", "/calls/_update/nope", `{"update":{"$set":{"x":1}},"bogus":1}`), http.StatusBadRequest, "invalid_request")

	// An upsert of a new id without the time field goes to the partition
	// of now.
	rec = call(h, "POST", "/calls/_update/fresh", `{"update":{"$set":{"status":"new"}},"upsert":true}`)

	expectStatus(t, rec, http.StatusOK)

	now := "calls-" + engine.PeriodOf("month", time.Now())

	expectStatus(t, call(h, "GET", "/"+now+"/_doc/fresh", ""), http.StatusOK)

	// $setOnInsert gives the time too.
	expectStatus(t, call(h, "POST", "/calls/_update/old", `{"update":{"$setOnInsert":{"ts":"2025-01-05T00:00:00Z"}},"upsert":true}`), http.StatusOK)
	expectStatus(t, call(h, "GET", "/calls-2025-01/_doc/old", ""), http.StatusOK)

	// An id held by an old partition is updated there, not created again.
	rec = call(h, "POST", "/calls/_update/a", `{"update":{"$set":{"ts":"2026-11-30T00:00:00Z"}},"upsert":true}`)

	if got := decodeMap(t, rec.Body.String()); got["matched"] != float64(1) {
		t.Fatalf("update a = %s", rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/calls-2026-09/_doc/a", ""), http.StatusOK)
}

// _update_by_query on a logical name: newest partition first; without many
// it stops at the first partition that matched.
func TestContractPartitionUpdateByQuery(t *testing.T) {
	h := cpSeed(t)

	rec := call(h, "POST", "/calls/_update_by_query", `{"query":{"status":"answered"},"update":{"$set":{"hit":true}}}`)

	expectStatus(t, rec, http.StatusOK)

	got := decodeMap(t, rec.Body.String())

	if got["matched"] != float64(1) || got["modified"] != float64(1) {
		t.Fatalf("one = %s", rec.Body.String())
	}

	// c (2026-10) is newer than a (2026-09).
	if doc, _ := got["doc"].(map[string]any); doc == nil || doc["_id"] != "c" {
		t.Fatalf("the newest partition goes first: %s", rec.Body.String())
	}

	if got := cpCount(t, h, "/calls/_count", `{"query":{"hit":true}}`); got != `{"count":1}` {
		t.Fatalf("hit = %s", got)
	}

	rec = call(h, "POST", "/calls/_update_by_query", `{"query":{"status":{"$in":["answered","missed"]}},"update":{"$inc":{"n":1}},"many":true}`)

	if got := decodeMap(t, rec.Body.String()); got["matched"] != float64(4) || got["modified"] != float64(4) {
		t.Fatalf("many = %s", rec.Body.String())
	}

	// No match and no upsert: 200 and matched 0.
	if got := strings.TrimSpace(call(h, "POST", "/calls/_update_by_query", `{"query":{"status":"none"},"update":{"$set":{"x":1}}}`).Body.String()); got != `{"matched":0,"modified":0}` {
		t.Fatalf("none = %s", got)
	}

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"not JSON", "POST", "/calls/_update_by_query", `{"query":`, bad, "invalid_request"},
		{"unknown key", "POST", "/calls/_update_by_query", `{"query":{},"update":{"$set":{"x":1}},"bogus":1}`, bad, "invalid_request"},
		{"bad expire_in", "POST", "/calls/_update_by_query", `{"query":{},"update":{"$set":{"x":1}},"expire_in":"soon"}`, bad, "invalid_request"},
		{"no update", "POST", "/calls/_update_by_query", `{"query":{"status":"missed"}}`, bad, "no_update"},
		{"filter in update", "POST", "/calls/_update_by_query", `{"query":{},"update":{"status":"x"}}`, bad, "unknown_operator"},
		{"operator in query", "POST", "/calls/_update_by_query", `{"query":{"$set":{"x":1}},"update":{"$set":{"x":1}}}`, bad, "unknown_operator"},
	})
}

// An upsert by query that matches nothing creates the document in the
// partition of the time field of its operators, or of now.
func TestContractPartitionUpsertByQuery(t *testing.T) {
	h := cpSeed(t)

	rec := call(h, "POST", "/calls/_update_by_query", `{"query":{"k":"may"},"update":{"$set":{"ts":"2026-05-01T00:00:00Z"}},"upsert":true}`)

	expectStatus(t, rec, http.StatusOK)

	got := decodeMap(t, rec.Body.String())

	if got["matched"] != float64(0) || got["modified"] != float64(1) {
		t.Fatalf("upsert = %s", rec.Body.String())
	}

	doc, _ := got["doc"].(map[string]any)

	if doc == nil || doc["k"] != "may" {
		t.Fatalf("the query equality goes into the new document: %s", rec.Body.String())
	}

	if got := cpCount(t, h, "/calls-2026-05/_count", `{"query":{"k":"may"}}`); got != `{"count":1}` {
		t.Fatalf("may = %s", got)
	}

	// Without the time field: the partition of now.
	expectStatus(t, call(h, "POST", "/calls/_update_by_query", `{"query":{"k":"now"},"update":{"$set":{"x":1}},"upsert":true}`), http.StatusOK)

	now := "calls-" + engine.PeriodOf("month", time.Now())

	if got := cpCount(t, h, "/"+now+"/_count", `{"query":{"k":"now"}}`); got != `{"count":1}` {
		t.Fatalf("now = %s", got)
	}

	// A match: no new document.
	rec = call(h, "POST", "/calls/_update_by_query", `{"query":{"k":"may"},"update":{"$set":{"y":1}},"upsert":true}`)

	if got := decodeMap(t, rec.Body.String()); got["matched"] != float64(1) {
		t.Fatalf("second upsert = %s", rec.Body.String())
	}

	if got := cpCount(t, h, "/calls/_count", `{"query":{"k":"may"}}`); got != `{"count":1}` {
		t.Fatalf("may twice = %s", got)
	}

	// The upsert of a new logical collection with no partition at all.
	expectStatus(t, call(h, "PUT", "/empty", `{"time":"ts","partition_by":"year"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/empty/_update_by_query", `{"query":{"k":"a"},"update":{"$set":{"ts":"2024-03-01T00:00:00Z"}},"upsert":true}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/empty-2024/_count", `{}`), http.StatusOK)

	// A bad operator fails in the upsert too, and writes nothing.
	expectStatus(t, call(h, "PUT", "/bare", `{"time":"ts","partition_by":"year"}`), http.StatusOK)

	rec = call(h, "POST", "/bare/_update_by_query", `{"query":{"k":"zz"},"update":{"$set":{"ts":"2023-03-01T00:00:00Z"},"$inc":{"k":"x"}},"upsert":true}`)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := cpCount(t, h, "/bare/_count", `{}`); got != `{"count":0}` {
		t.Fatalf("bare = %s", got)
	}
}

// _delete_by_query on a logical name: newest partition first; without many
// it stops at the first partition that deleted.
func TestContractPartitionDeleteByQuery(t *testing.T) {
	h := cpSeed(t)

	if got := strings.TrimSpace(call(h, "POST", "/calls/_delete_by_query", `{"query":{"status":"missed"}}`).Body.String()); got != `{"deleted":1}` {
		t.Fatalf("one = %s", got)
	}

	// d (2026-11) is newer than b (2026-10).
	expectStatus(t, call(h, "GET", "/calls/_doc/d", ""), http.StatusNotFound)
	expectStatus(t, call(h, "GET", "/calls/_doc/b", ""), http.StatusOK)

	if got := strings.TrimSpace(call(h, "POST", "/calls/_delete_by_query", `{"query":{"status":"none"},"many":true}`).Body.String()); got != `{"deleted":0}` {
		t.Fatalf("none = %s", got)
	}

	if got := strings.TrimSpace(call(h, "POST", "/calls/_delete_by_query", `{"query":{"status":{"$in":["answered","missed"]}},"many":true}`).Body.String()); got != `{"deleted":3}` {
		t.Fatalf("all = %s", got)
	}

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"not JSON", "POST", "/calls/_delete_by_query", `{"query":`, bad, "invalid_request"},
		{"unknown key", "POST", "/calls/_delete_by_query", `{"query":{},"bogus":1}`, bad, "invalid_request"},
	})
}

// _indexes on a logical name reach the logical definition and every
// partition, and the partitions made later.
func TestContractPartitionIndexes(t *testing.T) {
	h := cpSeed(t)

	rec := call(h, "PUT", "/calls/_indexes/n", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"calls","indexes":["n","status"]}` {
		t.Fatalf("put = %s", got)
	}

	rec = call(h, "DELETE", "/calls/_indexes/status", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"calls","indexes":["n"]}` {
		t.Fatalf("delete = %s", got)
	}

	for _, p := range cpPartitions(t, h, "calls") {
		body := call(h, "GET", "/"+p, "").Body.String()

		if !strings.Contains(body, `"field":"n"`) || strings.Contains(body, `"field":"status"`) {
			t.Errorf("%s = %s", p, body)
		}
	}

	// A new partition takes the definition of the logical collection.
	expectStatus(t, call(h, "POST", "/calls/_doc", `{"_id":"z","ts":"2027-01-01T00:00:00Z"}`), http.StatusOK)

	if body := call(h, "GET", "/calls-2027-01", "").Body.String(); !strings.Contains(body, `"indexes":["n"]`) {
		t.Fatalf("new partition = %s", body)
	}
}

// _watch, _ack, _compact, _quarantine and stream routes on a logical name
// are 400 not_supported; the message names a partition.
func TestContractPartitionNotSupported(t *testing.T) {
	h := cpSeed(t)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/calls/_watch", `{"consumer":"c"}`},
		{"POST", "/calls/_ack", `{"consumer":"c","position":1}`},
		{"POST", "/calls/_compact", ""},
		{"GET", "/calls/_quarantine", ""},
		{"GET", "/calls/_streams", ""},
		{"POST", "/calls/_streams/append", `{"fields":{"a":1}}`},
	} {
		rec := call(h, c.method, c.path, c.body)

		ccExpect(t, rec, http.StatusBadRequest, "not_supported")

		if !strings.Contains(rec.Body.String(), "calls-"+engine.PeriodOf("month", time.Now())) {
			t.Errorf("%s %s: the message must name a partition: %s", c.method, c.path, rec.Body.String())
		}
	}

	// A partition is a collection: compact and quarantine work on it.
	expectStatus(t, call(h, "POST", "/calls-2026-10/_compact", ""), http.StatusOK)
	expectStatus(t, call(h, "GET", "/calls-2026-10/_quarantine", ""), http.StatusOK)
}

// _stats on a logical name: the rows and the stats of each partition.
func TestContractPartitionStats(t *testing.T) {
	h := cpSeed(t)

	rec := call(h, "GET", "/calls/_stats", "")

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Name        string `json:"name"`
		PartitionBy string `json:"partition_by"`
		Rows        int    `json:"rows"`
		Partitions  []struct {
			Name string `json:"name"`
			Rows int    `json:"rows"`
		} `json:"partitions"`
	}

	decodeBody(t, rec.Body.String(), &body)

	if body.Name != "calls" || body.PartitionBy != "month" || body.Rows != 4 || len(body.Partitions) != 3 {
		t.Fatalf("stats = %s", rec.Body.String())
	}

	if body.Partitions[1].Name != "calls-2026-10" || body.Partitions[1].Rows != 2 {
		t.Fatalf("partition stats = %+v", body.Partitions)
	}
}

// DELETE and _truncate on a logical name: confirm rules; truncate keeps the
// definition; drop removes the definition and every partition.
func TestContractPartitionDropAndTruncate(t *testing.T) {
	h := cpSeed(t)

	for _, route := range []struct{ method, path string }{
		{"DELETE", "/calls"},
		{"POST", "/calls/_truncate"},
	} {
		ccExpect(t, call(h, route.method, route.path, ""), http.StatusBadRequest, "confirm_required")
		ccExpect(t, call(h, route.method, route.path+"?confirm=calls-2026-10", ""), http.StatusBadRequest, "confirm_required")
	}

	if got := cpCount(t, h, "/calls/_count", `{}`); got != `{"count":4}` {
		t.Fatalf("count = %s", got)
	}

	// One partition can be dropped by its name.
	expectStatus(t, call(h, "DELETE", "/calls-2026-09?confirm=calls-2026-09", ""), http.StatusOK)

	if got := cpPartitions(t, h, "calls"); !slices.Equal(got, []string{"calls-2026-10", "calls-2026-11"}) {
		t.Fatalf("partitions = %v", got)
	}

	rec := call(h, "POST", "/calls/_truncate?confirm=calls", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"calls","truncated":true}` {
		t.Fatalf("truncate = %s", got)
	}

	if got := cpPartitions(t, h, "calls"); len(got) != 0 {
		t.Fatalf("partitions after truncate = %v", got)
	}

	// The definition stays: a new document makes its partition again.
	expectStatus(t, call(h, "POST", "/calls/_doc", `{"_id":"x","ts":"2026-10-01T00:00:00Z"}`), http.StatusOK)

	if body := call(h, "GET", "/calls-2026-10", "").Body.String(); !strings.Contains(body, `"field":"status"`) {
		t.Fatalf("the indexes must stay: %s", body)
	}

	rec = call(h, "DELETE", "/calls?confirm=calls", "")

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"calls","dropped":true}` {
		t.Fatalf("drop = %s", got)
	}

	ccExpect(t, call(h, "GET", "/calls", ""), http.StatusNotFound, "unknown_collection")
	ccExpect(t, call(h, "GET", "/calls-2026-10", ""), http.StatusNotFound, "unknown_collection")

	if body := call(h, "GET", "/_collections", "").Body.String(); strings.Contains(body, "calls") {
		t.Fatalf("collections = %s", body)
	}
}

// _rename on a logical name renames its partitions; a partition cannot be
// renamed, nor be a target.
func TestContractPartitionRename(t *testing.T) {
	h := cpSeed(t)

	expectStatus(t, call(h, "PUT", "/plain", `{}`), http.StatusOK)

	bad := http.StatusBadRequest

	ccRun(t, h, []ccCase{
		{"a partition", "POST", "/calls-2026-10/_rename", `{"to":"x"}`, bad, "catalog_invalid"},
		{"onto a partition", "POST", "/plain/_rename", `{"to":"calls-2026-10"}`, bad, "catalog_invalid"},
		{"onto a partition with replace", "POST", "/plain/_rename", `{"to":"calls-2026-10","replace":true}`, bad, "catalog_invalid"},
		{"name over 53", "POST", "/calls/_rename", `{"to":"` + strings.Repeat("c", 54) + `"}`, bad, "catalog_invalid"},
		{"onto an existing name", "POST", "/calls/_rename", `{"to":"plain"}`, http.StatusConflict, "collection_exists"},
	})

	rec := call(h, "POST", "/calls/_rename", `{"to":"phone"}`)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collection":"phone","renamed_from":"calls","replaced":false}` {
		t.Fatalf("rename = %s", got)
	}

	if got := cpPartitions(t, h, "phone"); !slices.Equal(got, []string{"phone-2026-09", "phone-2026-10", "phone-2026-11"}) {
		t.Fatalf("partitions = %v", got)
	}

	if got := cpCount(t, h, "/phone/_count", `{}`); got != `{"count":4}` {
		t.Fatalf("count = %s", got)
	}

	ccExpect(t, call(h, "GET", "/calls", ""), http.StatusNotFound, "unknown_collection")
	ccExpect(t, call(h, "GET", "/calls-2026-10", ""), http.StatusNotFound, "unknown_collection")

	// A plain collection replaces the logical one and its partitions.
	expectStatus(t, call(h, "POST", "/plain/_rename", `{"to":"phone","replace":true}`), http.StatusOK)

	rec = call(h, "GET", "/phone", "")

	expectStatus(t, rec, http.StatusOK)

	if strings.Contains(rec.Body.String(), "partition") {
		t.Fatalf("phone = %s", rec.Body.String())
	}

	ccExpect(t, call(h, "GET", "/phone-2026-10", ""), http.StatusNotFound, "unknown_collection")
}

// A rename whose new partition names are taken by other collections must
// fail before it moves anything (409 collection_exists), and leave the
// collection as it was.
func TestContractPartitionRenameOntoTakenPartitionNames(t *testing.T) {
	t.Skip("BUG: rename of a logical name onto a name whose {to}-{period} exists is 500 internal and leaves the partitions closed and unreadable")

	h := cpSeed(t)

	expectStatus(t, call(h, "POST", "/phone-2026-10/_doc", `{"_id":"other"}`), http.StatusOK)

	rec := call(h, "POST", "/calls/_rename", `{"to":"phone"}`)

	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}

	if got := cpCount(t, h, "/calls/_count", `{}`); got != `{"count":4}` {
		t.Fatalf("count after a refused rename = %s", got)
	}
}

// A collection named like a partition ({coll}-{period}) but made apart
// (implicit insert, PUT) must not take the writes of the logical
// collection: they would be out of its reads.
func TestContractPartitionNameTakenByAPlainCollection(t *testing.T) {
	t.Skip("BUG: a plain collection named {coll}-{period} takes the logical writes of that period, and the logical reads do not see them")

	h := cpSeed(t)

	expectStatus(t, call(h, "POST", "/calls-2026-12/_doc", `{"_id":"z","ts":"2026-12-01T00:00:00Z"}`), http.StatusOK)

	rec := call(h, "POST", "/calls/_doc", `{"_id":"y","ts":"2026-12-02T00:00:00Z"}`)

	if rec.Code == http.StatusOK {
		expectStatus(t, call(h, "GET", "/calls/_doc/y", ""), http.StatusOK)
	}
}

// partition_of is set by ndb on a partition. A client that sends it in PUT
// must not make a collection a partition of another one.
func TestContractPartitionOfIsNotAClientKey(t *testing.T) {
	t.Skip("BUG: PUT accepts partition_of: the collection joins the reads, drop and truncate of that logical collection")

	h := cpSeed(t)

	rec := call(h, "PUT", "/evil", `{"partition_of":"calls"}`)

	ccExpect(t, rec, http.StatusBadRequest, "invalid_request")
}

// _restore of a logical name restores every partition under the new name;
// _pitr answers for the logical name.
func TestContractPartitionRestoreAndPITR(t *testing.T) {
	h := cpSeed(t)

	time.Sleep(3 * time.Millisecond)

	at := time.Now().UTC().Format(time.RFC3339Nano)

	time.Sleep(3 * time.Millisecond)

	expectStatus(t, call(h, "POST", "/calls/_delete_by_query", `{"query":{"status":{"$in":["answered","missed"]}},"many":true}`), http.StatusOK)

	expectStatus(t, call(h, "GET", "/calls/_pitr", ""), http.StatusOK)
	ccExpect(t, call(h, "POST", "/calls-2026-10/_restore", `{"at":"`+at+`","to":"one"}`), http.StatusBadRequest, "catalog_invalid")

	rec := call(h, "POST", "/calls/_restore", `{"at":"`+at+`","to":"back"}`)

	expectStatus(t, rec, http.StatusOK)

	if got := cpPartitions(t, h, "back"); !slices.Equal(got, []string{"back-2026-09", "back-2026-10", "back-2026-11"}) {
		t.Fatalf("partitions = %v", got)
	}

	if got := cpCount(t, h, "/back/_count", `{}`); got != `{"count":4}` {
		t.Fatalf("restored count = %s", got)
	}

	// The restored collection is logical too.
	expectStatus(t, call(h, "POST", "/back/_doc", `{"_id":"n","ts":"2026-12-01T00:00:00Z"}`), http.StatusOK)
	expectStatus(t, call(h, "GET", "/back-2026-12/_doc/n", ""), http.StatusOK)
}

// Patterns and lists over partitions: a pattern reads the partitions (the
// logical name holds no data); a logical name in a list is its live
// partitions.
func TestContractPatternsOverPartitions(t *testing.T) {
	h := cpSeed(t)

	expectStatus(t, call(h, "POST", "/other/_doc", `{"_id":"o","status":"answered"}`), http.StatusOK)

	if got := strings.TrimSpace(call(h, "GET", "/calls*", "").Body.String()); got != `{"collections":["calls-2026-09","calls-2026-10","calls-2026-11"]}` {
		t.Fatalf("GET pattern = %s", got)
	}

	if got := strings.TrimSpace(call(h, "GET", "/calls,other", "").Body.String()); got != `{"collections":["calls-2026-09","calls-2026-10","calls-2026-11","other"]}` {
		t.Fatalf("GET list = %s", got)
	}

	rows := rowsOf(t, call(h, "POST", "/calls-2026-1*/_search", `{"sort":{"ts":1}}`).Body.String())

	if got := ids(rows); !slices.Equal(got, []string{"b", "c", "d"}) || rows[0].Coll != "calls-2026-10" || rows[2].Coll != "calls-2026-11" {
		t.Fatalf("pattern search = %+v", rows)
	}

	if got := cpCount(t, h, "/calls,other/_count", `{"query":{"status":"answered"}}`); got != `{"count":3}` {
		t.Fatalf("list count = %s", got)
	}

	rec := call(h, "POST", "/calls,other/_aggregate", `{"pipeline":[{"$group":{"_id":"$status","n":{"$count":{}}}},{"$sort":{"_id":1}}]}`)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.Fields(rec.Body.String()); len(got) != 2 || got[0] != `{"_id":"answered","n":3}` {
		t.Fatalf("list aggregate = %v", got)
	}

	// Writes through a list or pattern stay refused.
	ccExpect(t, call(h, "POST", "/calls,other/_doc", `{"ts":"2026-10-01T00:00:00Z"}`), http.StatusBadRequest, "pattern_read_only")
	ccExpect(t, call(h, "POST", "/calls-*/_delete_by_query", `{"query":{}}`), http.StatusBadRequest, "pattern_read_only")
}

// With retention, a partition past it leaves the pattern and list reads at
// once, as it leaves the reads of the logical name.
func TestContractPartitionRetentionInLists(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/logs", `{"time":"ts","partition_by":"day","retention":"2d"}`), http.StatusOK)

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -10).Format(time.RFC3339)
	fresh := now.Format(time.RFC3339)

	expectStatus(t, call(h, "POST", "/logs/_bulk", `{"docs":[{"_id":"old","ts":"`+old+`"},{"_id":"new","ts":"`+fresh+`"}]}`), http.StatusOK)

	if got := cpCount(t, h, "/logs,logs/_count", `{}`); got != `{"count":1}` {
		t.Fatalf("list count = %s", got)
	}

	// A by-id route does not reach a partition past the retention.
	expectStatus(t, call(h, "GET", "/logs/_doc/old", ""), http.StatusNotFound)
	expectStatus(t, call(h, "GET", "/logs/_doc/new", ""), http.StatusOK)
}
