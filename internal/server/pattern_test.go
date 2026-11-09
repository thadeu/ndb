package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// seedLogs makes three partitions and two collections outside logs-2026-*.
// The "at" values tie across partitions, so the order needs the tie-breaks.
func seedLogs(t *testing.T) http.Handler {
	t.Helper()

	_, h := newHandler(t)

	seed := map[string]string{
		"logs-2026-10": `[{"_id":"a1","at":1,"lvl":"info"},{"_id":"a2","at":3,"lvl":"error"},{"_id":"a3","at":5,"lvl":"info"}]`,
		"logs-2026-11": `[{"_id":"b1","at":1,"lvl":"error"},{"_id":"b2","at":4,"lvl":"info"},{"_id":"b3","at":5,"lvl":"error"}]`,
		"logs-2026-12": `[{"_id":"c1","at":2,"lvl":"info","_coll":"stored"}]`,
		"logs-archive": `[{"_id":"z1","at":9,"lvl":"info"}]`,
		"other":        `[{"_id":"o1","at":1,"lvl":"info"}]`,
	}

	for name, docs := range seed {
		expectStatus(t, call(h, "POST", "/"+name+"/_bulk", `{"docs":`+docs+`}`), http.StatusOK)
	}

	// One partition on segments, the others in the memtable.
	expectStatus(t, call(h, "POST", "/logs-2026-10/_compact", ``), http.StatusOK)

	return h
}

type patternRow struct {
	ID   string `json:"_id"`
	Coll string `json:"_coll"`
}

func rowsOf(t *testing.T, body string) []patternRow {
	t.Helper()

	var out []patternRow

	for line := range strings.Lines(body) {
		var r patternRow

		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("row %q: %v", line, err)
		}

		out = append(out, r)
	}

	return out
}

func ids(rows []patternRow) []string {
	out := make([]string, len(rows))

	for i, r := range rows {
		out[i] = r.ID
	}

	return out
}

// walk reads every page of a find, limit rows at a time.
func walk(t *testing.T, h http.Handler, path, body string) []patternRow {
	t.Helper()

	var all []patternRow

	after := ""

	for range 20 {
		b := strings.TrimSuffix(body, "}")

		if after != "" {
			b += `,"after":"` + after + `"`
		}

		rec := call(h, "POST", path, b+"}")

		expectStatus(t, rec, http.StatusOK)

		all = append(all, rowsOf(t, rec.Body.String())...)

		if after = rec.Header().Get("X-Ndb-Next"); after == "" {
			return all
		}
	}

	t.Fatal("too many pages")

	return nil
}

func TestPatternFindSorted(t *testing.T) {
	h := seedLogs(t)

	rec := call(h, "POST", "/logs-2026-*/_search", `{"sort":{"at":-1},"count":true}`)

	expectStatus(t, rec, http.StatusOK)

	// at desc; a tie goes by collection desc, then _id desc.
	want := []string{"b3", "a3", "b2", "a2", "c1", "b1", "a1"}

	rows := rowsOf(t, rec.Body.String())

	if got := ids(rows); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	if rows[0].Coll != "logs-2026-11" || rows[1].Coll != "logs-2026-10" {
		t.Fatalf("_coll = %+v", rows[:2])
	}

	// A _coll field stored in the document gives way to the collection name.
	if rows[4].Coll != "logs-2026-12" || strings.Count(rec.Body.String(), `"_coll"`) != 7 {
		t.Fatalf("stored _coll: %s", rec.Body.String())
	}

	if got := rec.Header().Get("X-Ndb-Total"); got != "7" {
		t.Fatalf("total = %s", got)
	}

	if got := rec.Header().Get("X-Ndb-Plan"); got != "pattern" {
		t.Fatalf("plan = %s", got)
	}

	for _, limit := range []string{"1", "2", "3"} {
		got := ids(walk(t, h, "/logs-2026-*/_search", `{"sort":{"at":-1},"limit":`+limit+`}`))

		if !slices.Equal(got, want) {
			t.Fatalf("limit %s pages = %v, want %v", limit, got, want)
		}
	}

	asc := []string{"a1", "b1", "c1", "a2", "b2", "a3", "b3"}

	if got := ids(walk(t, h, "/logs-2026-*/_search", `{"sort":{"at":1},"limit":2}`)); !slices.Equal(got, asc) {
		t.Fatalf("asc pages = %v, want %v", got, asc)
	}
}

func TestPatternFindUnsorted(t *testing.T) {
	h := seedLogs(t)

	want := []string{"a1", "a2", "a3", "b1", "b2", "b3", "c1"}

	if got := ids(walk(t, h, "/logs-2026-*/_search", `{"limit":2}`)); !slices.Equal(got, want) {
		t.Fatalf("pages = %v, want %v", got, want)
	}

	rec := call(h, "POST", "/logs-2026-*/_search", `{"limit":2,"count":true}`)
	next := rec.Header().Get("X-Ndb-Next")
	rec = call(h, "POST", "/logs-2026-*/_search", `{"limit":2,"count":true,"after":"`+next+`"}`)

	if got := rec.Header().Get("X-Ndb-Total"); got != "7" {
		t.Fatalf("total on page 2 = %s", got)
	}
}

func TestPatternFindWhere(t *testing.T) {
	h := seedLogs(t)

	rec := call(h, "POST", "/logs-*/_search", `{"query":{"lvl":"error"},"sort":{"at":1},"fields":["at"]}`)

	expectStatus(t, rec, http.StatusOK)

	want := []string{
		`{"_coll":"logs-2026-11","at":1}`,
		`{"_coll":"logs-2026-10","at":3}`,
		`{"_coll":"logs-2026-11","at":5}`,
	}

	if got := strings.Fields(rec.Body.String()); !slices.Equal(got, want) {
		t.Fatalf("rows = %v", got)
	}

	// logs-* also matches logs-archive: a plain prefix.
	rec = call(h, "POST", "/logs-*/_search", `{"query":{"at":9}}`)

	if rows := rowsOf(t, rec.Body.String()); len(rows) != 1 || rows[0].Coll != "logs-archive" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestPatternAggregate(t *testing.T) {
	h := seedLogs(t)

	rec := call(h, "POST", "/logs-2026-*/_aggregate", `{"pipeline":[{"$group":{"_id":"$lvl","n":{"$count":{}},"sum":{"$sum":"$at"}}}]}`)

	expectStatus(t, rec, http.StatusOK)

	want := []string{`{"_id":"error","n":3,"sum":9}`, `{"_id":"info","n":4,"sum":12}`}

	if got := strings.Fields(rec.Body.String()); !slices.Equal(got, want) {
		t.Fatalf("rows = %v, want %v", got, want)
	}
}

func TestPatternNoMatch(t *testing.T) {
	h := seedLogs(t)

	rec := call(h, "POST", "/logs-2025-*/_search", `{"count":true}`)

	expectStatus(t, rec, http.StatusOK)

	if rec.Body.Len() != 0 || rec.Header().Get("X-Ndb-Total") != "0" {
		t.Fatalf("body = %q, total = %q", rec.Body.String(), rec.Header().Get("X-Ndb-Total"))
	}

	expectStatus(t, call(h, "POST", "/logs-2025-*/_aggregate", `{"pipeline":[{"$group":{"_id":"$lvl"}}]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/logs-2025-*/_search", `{"bad":1}`), http.StatusBadRequest)
}

func TestPatternList(t *testing.T) {
	h := seedLogs(t)

	rec := call(h, "GET", "/logs-2026-*", ``)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collections":["logs-2026-10","logs-2026-11","logs-2026-12"]}` {
		t.Fatalf("body = %s", got)
	}

	rec = call(h, "GET", "/nope-*", ``)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"collections":[]}` {
		t.Fatalf("body = %s", got)
	}
}

func TestPatternErrors(t *testing.T) {
	h := seedLogs(t)

	for _, c := range []struct{ method, path, body string }{
		{"POST", "/logs-*/_bulk", `{"docs":[{"a":1}]}`},
		{"POST", "/logs-*/_update_by_query", `{"query":{"lvl":"info"},"update":{"$set":{"x":1}}}`},
		{"POST", "/logs-*/_delete_by_query", `{"query":{"lvl":"info"}}`},
		{"POST", "/logs-*/_truncate?confirm=logs-*", ``},
		{"DELETE", "/logs-*?confirm=logs-*", ``},
		{"PUT", "/logs-*", `{}`},
		{"POST", "/logs-*/_watch", `{}`},
	} {
		rec := call(h, c.method, c.path, c.body)

		expectStatus(t, rec, http.StatusBadRequest)

		if got := errorCode(t, rec); got != "pattern_read_only" {
			t.Fatalf("%s %s: code = %s", c.method, c.path, got)
		}
	}

	for _, p := range []string{"*", "lo*gs", "logs-**", "Logs-*"} {
		rec := call(h, "POST", "/"+p+"/_search", `{}`)

		expectStatus(t, rec, http.StatusBadRequest)

		if got := errorCode(t, rec); got != "invalid_pattern" {
			t.Fatalf("%s: code = %s", p, got)
		}
	}

	rec := call(h, "POST", "/logs-2026-*/_search", `{"limit":1}`)
	next := rec.Header().Get("X-Ndb-Next")

	for _, path := range []string{"/logs-*/_search", "/logs-2026-10/_search"} {
		rec = call(h, "POST", path, `{"limit":1,"after":"`+next+`"}`)

		if got := errorCode(t, rec); got != "cursor_mismatch" {
			t.Fatalf("%s: code = %s", path, got)
		}
	}

	rec = call(h, "POST", "/logs-*/_search", `{"snapshot":true}`)

	if got := errorCode(t, rec); got != "invalid_query" {
		t.Fatalf("snapshot: code = %s", got)
	}
}

// A list of collections split by commas reads each of them; it mixes with a
// pattern, and each collection counts once.
func TestListOfCollections(t *testing.T) {
	h := seedLogs(t)

	rec := call(h, "POST", "/logs-2026-12,logs-2026-10/_search", `{"sort":{"at":1}}`)
	expectStatus(t, rec, http.StatusOK)

	if got := ids(rowsOf(t, rec.Body.String())); !slices.Equal(got, []string{"a1", "c1", "a2", "a3"}) {
		t.Fatalf("list: %v", got)
	}

	rec = call(h, "POST", "/logs-2026-1*,logs-2026-11,other/_count", `{}`)
	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"count":8`) {
		t.Fatalf("pattern, name and other: %s", rec.Body.String())
	}

	rec = call(h, "POST", "/logs-2026-10,logs-2026-11/_aggregate", `{"pipeline":[{"$group":{"_id":"$lvl","n":{"$count":{}}}},{"$sort":{"_id":1}}]}`)
	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"n":3`) {
		t.Fatalf("aggregate over a list: %s", rec.Body.String())
	}
}

// A name of the list that does not exist is an error, not a silent skip; an
// empty item is a bad request; a list only reads.
func TestListOfCollectionsErrors(t *testing.T) {
	h := seedLogs(t)

	expectStatus(t, call(h, "POST", "/logs-2026-10,nope/_search", `{}`), http.StatusNotFound)
	expectStatus(t, call(h, "POST", "/logs-2026-10,,other/_search", `{}`), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/logs-2026-10,other/_doc", `{"a":1}`), http.StatusBadRequest)
}
