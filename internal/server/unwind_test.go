package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// aggOf runs a pipeline and gives its rows as canonical JSON, in order.
func aggOf(t *testing.T, h http.Handler, coll, pipeline string) []string {
	t.Helper()

	rec := call(h, "POST", "/"+coll+"/_aggregate", `{"pipeline":`+pipeline+`,"limit":1000}`)
	expectStatus(t, rec, http.StatusOK)

	var out []string

	for line := range strings.Lines(rec.Body.String()) {
		var m map[string]any

		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("row %q: %v", line, err)
		}

		b, _ := json.Marshal(m)
		out = append(out, string(b))
	}

	return out
}

func expectRows(t *testing.T, got []string, want ...string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("rows\n got %v\nwant %v", got, want)
	}
}

func seedOrders(t *testing.T, h http.Handler) {
	t.Helper()

	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[
		{"_id":"o1","status":"paid","items":[{"sku":"a","qty":2},{"sku":"b","qty":1}]},
		{"_id":"o2","status":"paid","items":[{"sku":"a","qty":5}]},
		{"_id":"o3","status":"open","items":[{"sku":"b","qty":9}]},
		{"_id":"o4","status":"paid","items":[]},
		{"_id":"o5","status":"paid"},
		{"_id":"o6","status":"paid","items":null},
		{"_id":"o7","status":"paid","items":{"sku":"c","qty":4}},
		{"_id":"o8","status":"paid","items":[{"sku":"a","qty":1},{"sku":"a","qty":1}]}
	]}`), http.StatusOK)
}

func TestUnwindGroupsByTheElementsOfAnArray(t *testing.T) {
	_, h := newHandler(t)

	seedOrders(t, h)

	for _, stage := range []string{"memtable", "sealed"} {
		got := aggOf(t, h, "orders", `[
			{"$match":{"status":"paid"}},
			{"$unwind":"$items"},
			{"$group":{"_id":"$items.sku","qty":{"$sum":"$items.qty"},"lines":{"$count":{}},"orders":{"$distinct":"$_id"}}},
			{"$sort":{"_id":1}}]`)

		expectRows(t, got,
			`{"_id":"a","lines":4,"orders":3,"qty":9}`,
			`{"_id":"b","lines":1,"orders":1,"qty":1}`,
			`{"_id":"c","lines":1,"orders":1,"qty":4}`,
		)

		if stage == "memtable" {
			expectStatus(t, call(h, "POST", "/orders/_compact", ``), http.StatusOK)
		}
	}
}

func TestUnwindOptionsAndAMatchOnTheElements(t *testing.T) {
	_, h := newHandler(t)

	seedOrders(t, h)

	got := aggOf(t, h, "orders", `[
		{"$unwind":{"path":"$items","preserveNullAndEmptyArrays":true,"includeArrayIndex":"i"}},
		{"$group":{"_id":"$i","n":{"$count":{}}}},
		{"$sort":{"_id":1}}]`)

	// o4, o5, o6 (empty, missing, null) and o7 (not an array) have no index.
	expectRows(t, got, `{"_id":null,"n":4}`, `{"_id":0,"n":4}`, `{"_id":1,"n":2}`)

	got = aggOf(t, h, "orders", `[
		{"$unwind":"$items"},
		{"$match":{"items.qty":{"$gte":2}}},
		{"$group":{"_id":"$items.sku","qty":{"$sum":"$items.qty"}}},
		{"$sort":{"_id":1}}]`)

	expectRows(t, got, `{"_id":"a","qty":7}`, `{"_id":"b","qty":9}`, `{"_id":"c","qty":4}`)

	got = aggOf(t, h, "orders", `[
		{"$match":{"_id":"o1"}},
		{"$unwind":"$items"},
		{"$group":{"_id":"$_id","first":{"$first":"$items.sku"},"last":{"$last":"$items.sku"},"skus":{"$addToSet":"$items.sku"}}}]`)

	expectRows(t, got, `{"_id":"o1","first":"a","last":"b","skus":["a","b"]}`)
}

func TestUnwindErrors(t *testing.T) {
	_, h := newHandler(t)

	seedOrders(t, h)

	group := `{"$group":{"_id":"$items.sku","n":{"$count":{}}}}`

	for name, pipeline := range map[string]string{
		"two unwinds":      `[{"$unwind":"$items"},{"$unwind":"$tags"},` + group + `]`,
		"after the group":  `[` + group + `,{"$unwind":"$items"}]`,
		"no group":         `[{"$unwind":"$items"}]`,
		"no dollar":        `[{"$unwind":"items"},` + group + `]`,
		"empty path":       `[{"$unwind":"$"},` + group + `]`,
		"unknown option":   `[{"$unwind":{"path":"$items","nope":1}},` + group + `]`,
		"index not a name": `[{"$unwind":{"path":"$items","includeArrayIndex":"$i"}},` + group + `]`,
		"not a string":     `[{"$unwind":3},` + group + `]`,
	} {
		rec := call(h, "POST", "/orders/_aggregate", `{"pipeline":`+pipeline+`}`)

		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_pipeline" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

func TestUnwindOnAPartitionedCollectionAndAPattern(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/sales", `{"time":"ts","partition_by":"month"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/sales/_bulk", `{"docs":[
		{"_id":"s1","ts":"2026-10-05T00:00:00Z","items":[{"sku":"a","qty":1},{"sku":"b","qty":2}]},
		{"_id":"s2","ts":"2026-11-05T00:00:00Z","items":[{"sku":"a","qty":3}]}]}`), http.StatusOK)

	pipeline := `[{"$unwind":"$items"},{"$group":{"_id":"$items.sku","qty":{"$sum":"$items.qty"}}},{"$sort":{"_id":1}}]`

	expectRows(t, aggOf(t, h, "sales", pipeline), `{"_id":"a","qty":4}`, `{"_id":"b","qty":2}`)
	expectRows(t, aggOf(t, h, "sales-*", pipeline), `{"_id":"a","qty":4}`, `{"_id":"b","qty":2}`)
}
