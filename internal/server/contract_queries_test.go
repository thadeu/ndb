package server_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

// The contract of the query routes (docs/dev/http/queries.md, query.md,
// aggregate.md, the order and pagination rules of http.md). The filter and
// stage cases that every SDK must also pass are in spec/cases.

// cqSeed makes "orders" with five documents and an index on g.
func cqSeed(t *testing.T) http.Handler {
	t.Helper()

	_, h := newHandler(t)

	docs := `[
		{"_id":"o1","g":1,"n":5,"s":"x"},
		{"_id":"o2","g":1,"n":3,"s":"y"},
		{"_id":"o3","g":2,"n":"str"},
		{"_id":"o4","g":2,"n":null},
		{"_id":"o5","g":3}
	]`

	expectStatus(t, call(h, "PUT", "/orders", `{"indexes":["g"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":`+docs+`}`), http.StatusOK)
	indexesReady(t, h, "orders")

	return h
}

// cqIDs reads the _id of each NDJSON row.
func cqIDs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()

	expectStatus(t, rec, http.StatusOK)

	return ids(rowsOf(t, rec.Body.String()))
}

// cqError checks the status and the code of an error answer.
func cqError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	expectStatus(t, rec, status)

	if got := errorCode(t, rec); got != code {
		t.Fatalf("code = %q, want %q; body %s", got, code, rec.Body.String())
	}
}

// cqOpen opens an engine on dir and returns its handler and a close func.
func cqOpen(t *testing.T, dir string) (http.Handler, func()) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()
	srv.Attach(db)

	return srv.Handler(), func() { db.Close() }
}

// cqNext is the X-Ndb-Next of an answer, or "" on the last page.
func cqNext(rec *httptest.ResponseRecorder) string {
	return rec.Header().Get("X-Ndb-Next")
}

func TestQueriesSearchBodyErrors(t *testing.T) {
	h := cqSeed(t)

	cases := map[string]string{
		"unknown key":          `{"filter":{"g":1}}`,
		"limit not a number":   `{"limit":"10"}`,
		"skip negative":        `{"skip":-1}`,
		"sort not an object":   `{"sort":["g"]}`,
		"sort direction":       `{"sort":{"g":2}}`,
		"sort empty path":      `{"sort":{"a..b":1}}`,
		"fields not strings":   `{"fields":"g"}`,
		"fields empty path":    `{"fields":[""]}`,
		"count not a boolean":  `{"count":"yes"}`,
		"after not a string":   `{"after":5}`,
		"versions not boolean": `{"versions":1}`,
		"per with skip":        `{"per":{"field":"g","limit":1},"skip":1}`,
		"per without field":    `{"per":{"limit":1}}`,
		"per limit zero":       `{"per":{"field":"g","limit":0}}`,
		"query not an object":  `{"query":[1]}`,
		"body not an object":   `[1]`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cqError(t, call(h, "POST", "/orders/_search", body), http.StatusBadRequest, "invalid_query")
		})
	}

	cqError(t, call(h, "POST", "/nope/_search", `{}`), http.StatusNotFound, "unknown_collection")
}

func TestQueriesSearchEmptyBodyMatchesAll(t *testing.T) {
	h := cqSeed(t)

	for _, body := range []string{``, `{}`, `{"query":{}}`} {
		if got := cqIDs(t, call(h, "POST", "/orders/_search", body)); !slices.Equal(got, []string{"o1", "o2", "o3", "o4", "o5"}) {
			t.Fatalf("%q: %v, want every row in insertion order", body, got)
		}
	}
}

// _id is the last sort key. Its direction follows the last sort key: on a
// descending sort, ties come in descending _id order.
func TestQueriesSearchIDBreaksTies(t *testing.T) {
	h := cqSeed(t)

	cases := []struct {
		sort string
		want []string
	}{
		{`{"g":1}`, []string{"o1", "o2", "o3", "o4", "o5"}},
		{`{"g":-1}`, []string{"o5", "o4", "o3", "o2", "o1"}},
	}

	for _, c := range cases {
		if got := cqIDs(t, call(h, "POST", "/orders/_search", `{"sort":`+c.sort+`}`)); !slices.Equal(got, c.want) {
			t.Fatalf("sort %s = %v, want %v", c.sort, got, c.want)
		}
	}
}

func TestQueriesSearchHeaders(t *testing.T) {
	h := cqSeed(t)

	plans := map[string]string{
		`{"query":{"_id":"o2"}}`:           "id",
		`{"query":{"g":2}}`:                "index:g",
		`{"query":{"n":3}}`:                "scan",
		`{"query":{"g":{"$in":[1,3]}}}`:    "index:g",
		`{"query":{"g":{"$gte":2}}}`:       "index:g",
		`{"query":{"_id":{"$in":["o1"]}}}`: "id",
	}

	for body, want := range plans {
		rec := call(h, "POST", "/orders/_search", body)

		expectStatus(t, rec, http.StatusOK)

		if got := rec.Header().Get("X-Ndb-Plan"); got != want {
			t.Errorf("%s: X-Ndb-Plan = %q, want %q", body, got, want)
		}

		for _, name := range []string{"X-Ndb-Examined", "X-Ndb-Took-Ms"} {
			if rec.Header().Get(name) == "" {
				t.Errorf("%s: %s is always sent", body, name)
			}
		}

		for _, name := range []string{"X-Ndb-Total", "X-Ndb-Explain", "X-Ndb-Next"} {
			if rec.Header().Get(name) != "" {
				t.Errorf("%s: %s without its key", body, name)
			}
		}
	}

	rec := call(h, "POST", "/orders/_search", `{"query":{"g":1},"limit":1,"count":true,"explain":true}`)

	if got := rec.Header().Get("X-Ndb-Total"); got != "2" {
		t.Fatalf("X-Ndb-Total = %q, want 2 (whatever limit is)", got)
	}

	var ex map[string]any

	if err := json.Unmarshal([]byte(rec.Header().Get("X-Ndb-Explain")), &ex); err != nil {
		t.Fatalf("X-Ndb-Explain: %v", err)
	}

	for _, key := range []string{"segments_read", "segments_skipped", "row_groups_read", "row_groups_skipped", "rows_read", "rows_matched", "raw_fetched", "cache_hits"} {
		if _, ok := ex[key]; !ok {
			t.Errorf("X-Ndb-Explain misses %s: %v", key, ex)
		}
	}

	last := call(h, "POST", "/orders/_search", `{"query":{"g":1},"limit":2}`)

	if cqNext(last) != "" {
		t.Fatal("X-Ndb-Next on the last page")
	}

	empty := call(h, "POST", "/orders/_search", `{"query":{"g":9},"count":true}`)

	if empty.Header().Get("X-Ndb-Total") != "0" || strings.TrimSpace(empty.Body.String()) != "" {
		t.Fatalf("no match: total %q, body %q", empty.Header().Get("X-Ndb-Total"), empty.Body.String())
	}
}

func TestQueriesSearchCursorWalk(t *testing.T) {
	h := cqSeed(t)

	body := `{"sort":{"g":1},"limit":2}`

	if got := ids(walk(t, h, "/orders/_search", body)); !slices.Equal(got, []string{"o1", "o2", "o3", "o4", "o5"}) {
		t.Fatalf("walk = %v", got)
	}

	first := call(h, "POST", "/orders/_search", body)
	next := cqNext(first)

	if next == "" {
		t.Fatal("no cursor on the first page")
	}

	// A row inserted before the cursor does not move the next pages.
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o0","g":0}]}`), http.StatusOK)

	second := call(h, "POST", "/orders/_search", `{"sort":{"g":1},"limit":2,"after":"`+next+`"}`)

	if got := cqIDs(t, second); !slices.Equal(got, []string{"o3", "o4"}) {
		t.Fatalf("second page = %v, want [o3 o4]", got)
	}

	errs := map[string]struct {
		body, code string
	}{
		"another query": {`{"query":{"g":{"$gte":0}},"sort":{"g":1},"limit":2,"after":"` + next + `"}`, "cursor_mismatch"},
		"another sort":  {`{"sort":{"g":-1},"limit":2,"after":"` + next + `"}`, "cursor_mismatch"},
		"no sort":       {`{"limit":2,"after":"` + next + `"}`, "cursor_mismatch"},
		"not base64":    {`{"sort":{"g":1},"after":"%%%"}`, "cursor_invalid"},
		"not JSON":      {`{"sort":{"g":1},"after":"bm9wZQ"}`, "cursor_invalid"},
	}

	for name, c := range errs {
		t.Run(name, func(t *testing.T) {
			cqError(t, call(h, "POST", "/orders/_search", c.body), http.StatusBadRequest, c.code)
		})
	}

	// The same limit and fields do not bind the cursor.
	other := call(h, "POST", "/orders/_search", `{"sort":{"g":1},"limit":10,"fields":["_id"],"after":"`+next+`"}`)

	if got := cqIDs(t, other); !slices.Equal(got, []string{"o3", "o4", "o5"}) {
		t.Fatalf("other limit = %v", got)
	}
}

func TestQueriesSearchSnapshotKeepsTheFirstData(t *testing.T) {
	h := cqSeed(t)

	body := `{"sort":{"g":1},"limit":2,"snapshot":true,"count":true}`

	first := call(h, "POST", "/orders/_search", body)

	if got := cqIDs(t, first); !slices.Equal(got, []string{"o1", "o2"}) {
		t.Fatalf("first page = %v", got)
	}

	// Writes after the first page: the next pages do not see them.
	expectStatus(t, call(h, "POST", "/orders/_delete_by_query", `{"query":{"_id":"o3"}}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o9","g":2}]}`), http.StatusOK)

	var got []string

	next := cqNext(first)

	for next != "" {
		rec := call(h, "POST", "/orders/_search", `{"sort":{"g":1},"limit":2,"snapshot":true,"count":true,"after":"`+next+`"}`)

		if plan := rec.Header().Get("X-Ndb-Plan"); plan != "snapshot" {
			t.Fatalf("X-Ndb-Plan = %q, want snapshot", plan)
		}

		if total := rec.Header().Get("X-Ndb-Total"); total != "5" {
			t.Fatalf("X-Ndb-Total = %q, want the total of the snapshot (5)", total)
		}

		got = append(got, cqIDs(t, rec)...)
		next = cqNext(rec)
	}

	if !slices.Equal(got, []string{"o3", "o4", "o5"}) {
		t.Fatalf("snapshot pages = %v, want [o3 o4 o5]", got)
	}

	cqError(t, call(h, "POST", "/orders/_search", `{"query":{"g":1},"sort":{"g":1},"limit":2,"snapshot":true,"after":"`+cqNext(first)+`"}`), http.StatusBadRequest, "cursor_mismatch")

	// Without snapshot, the same walk sees the writes.
	if got := ids(walk(t, h, "/orders/_search", `{"sort":{"g":1},"limit":2}`)); !slices.Equal(got, []string{"o1", "o2", "o4", "o9", "o5"}) {
		t.Fatalf("live walk = %v", got)
	}
}

// The snapshot lives in the daemon. A daemon that restarted has none: the
// cursor is cursor_expired, not a silent restart of the walk.
func TestQueriesSnapshotCursorExpires(t *testing.T) {
	dir := t.TempDir()

	h, closeDB := cqOpen(t, dir)

	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"a"},{"_id":"b"},{"_id":"c"}]}`), http.StatusOK)

	first := call(h, "POST", "/orders/_search", `{"limit":1,"snapshot":true}`)
	next := cqNext(first)

	if next == "" {
		t.Fatal("no cursor")
	}

	closeDB()

	h, closeDB = cqOpen(t, dir)

	defer closeDB()

	cqError(t, call(h, "POST", "/orders/_search", `{"limit":1,"snapshot":true,"after":"`+next+`"}`), http.StatusBadRequest, "cursor_expired")

	// A cursor without a snapshot still works after a restart.
	plain := cqNext(call(h, "POST", "/orders/_search", `{"limit":1}`))

	if got := cqIDs(t, call(h, "POST", "/orders/_search", `{"limit":1,"after":"`+plain+`"}`)); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("plain cursor = %v", got)
	}
}

// A cursor keeps the now of its first page, so the date math window of a
// walk does not move.
func TestQueriesCursorKeepsNow(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/events", `{"time":"at"}`), http.StatusOK)

	now := time.Now().UTC()
	soon := now.Add(time.Second).Format(time.RFC3339Nano)
	past := now.Add(-time.Minute).Format(time.RFC3339Nano)

	docs := fmt.Sprintf(`{"docs":[{"_id":"p1","at":%q},{"_id":"p2","at":%q},{"_id":"soon","at":%q}]}`, past, past, soon)

	expectStatus(t, call(h, "POST", "/events/_bulk", docs), http.StatusOK)

	body := `{"query":{"at":{"$lt":"now"}},"sort":{"at":1},"limit":1}`

	first := call(h, "POST", "/events/_search", body)

	if got := cqIDs(t, first); !slices.Equal(got, []string{"p1"}) {
		t.Fatalf("first page = %v", got)
	}

	time.Sleep(time.Until(now.Add(1500 * time.Millisecond)))

	rest := call(h, "POST", "/events/_search", `{"query":{"at":{"$lt":"now"}},"sort":{"at":1},"limit":5,"after":"`+cqNext(first)+`"}`)

	if got := cqIDs(t, rest); !slices.Equal(got, []string{"p2"}) {
		t.Fatalf("second page = %v, want [p2]: the cursor keeps the now of the first page", got)
	}

	if got := cqIDs(t, call(h, "POST", "/events/_search", `{"query":{"at":{"$lt":"now"}},"sort":{"at":1}}`)); !slices.Equal(got, []string{"p1", "p2", "soon"}) {
		t.Fatalf("a new search reads now again: %v", got)
	}
}

func TestQueriesCountErrors(t *testing.T) {
	h := cqSeed(t)

	cases := []struct {
		body, code string
		status     int
	}{
		{`{"query":{"g":1},"limit":1}`, "invalid_request", http.StatusBadRequest},
		{`{"query":{"g":{"$bad":1}}}`, "invalid_query", http.StatusBadRequest},
		{`{"query":"g=1"}`, "invalid_query", http.StatusBadRequest},
		{`[1]`, "invalid_request", http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.body, func(t *testing.T) {
			cqError(t, call(h, "POST", "/orders/_count", c.body), c.status, c.code)
		})
	}

	rec := call(h, "POST", "/orders/_count", `{"query":{"g":{"$in":[1,2]}}}`)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"count":4}` {
		t.Fatalf("count = %s", got)
	}
}

func TestQueriesAggregatePages(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 20)

	pipeline := `{"pipeline":[{"$group":{"_id":"$k","n":{"$count":{}}}}],"limit":2,"count":true`

	var got []string

	after := ""

	for range 10 {
		b := pipeline

		if after != "" {
			b += `,"after":"` + after + `"`
		}

		rec := call(h, "POST", "/posts/_aggregate", b+"}")

		expectStatus(t, rec, http.StatusOK)

		if total := rec.Header().Get("X-Ndb-Total"); total != "5" {
			t.Fatalf("X-Ndb-Total = %q, want 5 output rows", total)
		}

		for line := range strings.Lines(rec.Body.String()) {
			got = append(got, strings.TrimSpace(line))
		}

		if after = cqNext(rec); after == "" {
			break
		}
	}

	want := []string{`{"_id":0,"n":4}`, `{"_id":1,"n":4}`, `{"_id":2,"n":4}`, `{"_id":3,"n":4}`, `{"_id":4,"n":4}`}

	if !slices.Equal(got, want) {
		t.Fatalf("pages = %v", got)
	}

	next := cqNext(call(h, "POST", "/posts/_aggregate", pipeline+"}"))

	cqError(t, call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":"$n"}}],"limit":2,"after":"`+next+`"}`), http.StatusBadRequest, "cursor_mismatch")

	search := cqNext(call(h, "POST", "/posts/_search", `{"limit":1}`))

	cqError(t, call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":"$k"}}],"limit":2,"after":"`+search+`"}`), http.StatusBadRequest, "cursor_mismatch")

	rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":"$k"}}],"skip":3,"limit":5}`)

	if got := strings.Count(rec.Body.String(), "\n"); got != 2 || cqNext(rec) != "" {
		t.Fatalf("skip 3 of 5: %d rows, next %q", got, cqNext(rec))
	}
}

func TestQueriesAggregateErrors(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 3)

	expectStatus(t, call(h, "POST", "/people/_bulk", `{"docs":[{"_id":"x","name":"Ana"}]}`), http.StatusOK)

	cases := map[string]struct {
		body, code string
		status     int
	}{
		"not JSON":           {`{`, "invalid_pipeline", http.StatusBadRequest},
		"pipeline object":    {`{"pipeline":{"$group":{"_id":null}}}`, "invalid_pipeline", http.StatusBadRequest},
		"stage two keys":     {`{"pipeline":[{"$group":{"_id":null},"$limit":1}]}`, "invalid_pipeline", http.StatusBadRequest},
		"limit before group": {`{"pipeline":[{"$limit":1},{"$group":{"_id":null}}]}`, "invalid_pipeline", http.StatusBadRequest},
		"skip negative":      {`{"pipeline":[{"$group":{"_id":null}}],"skip":-1}`, "invalid_pipeline", http.StatusBadRequest},
		"lookup unindexed":   {`{"pipeline":[{"$group":{"_id":"$k"}},{"$lookup":{"from":"people","localField":"_id","foreignField":"name","as":"p"}}]}`, "lookup_unindexed", http.StatusBadRequest},
		"lookup unknown":     {`{"pipeline":[{"$group":{"_id":"$k"}},{"$lookup":{"from":"nope","localField":"_id","foreignField":"_id","as":"p"}}]}`, "unknown_collection", http.StatusNotFound},
		"lookup pipeline":    {`{"pipeline":[{"$group":{"_id":"$k"}},{"$lookup":{"from":"people","pipeline":[],"as":"p"}}]}`, "lookup_invalid", http.StatusBadRequest},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cqError(t, call(h, "POST", "/posts/_aggregate", c.body), c.status, c.code)
		})
	}
}

// The distinct values of $distinct and $addToSet count against the group
// limit: over it is group_limit_exceeded, not a wrong count.
func TestQueriesAggregateDistinctCountsAgainstTheGroupLimit(t *testing.T) {
	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true, GroupLimit: 2048}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	srv := server.New()
	srv.Attach(db)

	h := srv.Handler()

	seedPosts(t, h, 200)

	for _, acc := range []string{`{"$distinct":"$_id"}`, `{"$addToSet":"$_id"}`} {
		rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":null,"d":`+acc+`}}]}`)

		cqError(t, rec, http.StatusUnprocessableEntity, "group_limit_exceeded")
	}
}

func TestQueriesUpdateByQueryOperators(t *testing.T) {
	cases := []struct {
		name, update string
		modified     float64
		want         map[string]any
	}{
		{"set dot path", `{"$set":{"meta.city":"Rio"}}`, 1, map[string]any{"meta": map[string]any{"city": "Rio"}}},
		{"unset true", `{"$unset":{"s":true}}`, 1, map[string]any{"s": nil}},
		{"unset empty string", `{"$unset":{"s":""}}`, 1, map[string]any{"s": nil}},
		{"unset missing", `{"$unset":{"zz":true}}`, 0, nil},
		{"inc", `{"$inc":{"n":2}}`, 1, map[string]any{"n": 7.0}},
		{"inc negative", `{"$inc":{"n":-5}}`, 1, map[string]any{"n": 0.0}},
		{"inc missing starts at 0", `{"$inc":{"hits":1}}`, 1, map[string]any{"hits": 1.0}},
		{"inc fraction", `{"$inc":{"n":0.5}}`, 1, map[string]any{"n": 5.5}},
		{"push", `{"$push":{"tags":"c"}}`, 1, map[string]any{"tags": []any{"a", "b", "a", "c"}}},
		{"push each", `{"$push":{"tags":{"$each":["c","d"]}}}`, 1, map[string]any{"tags": []any{"a", "b", "a", "c", "d"}}},
		{"push missing", `{"$push":{"log":1}}`, 1, map[string]any{"log": []any{1.0}}},
		{"pull value", `{"$pull":{"tags":"a"}}`, 1, map[string]any{"tags": []any{"b"}}},
		{"pull condition", `{"$pull":{"scores":{"$lt":10}}}`, 1, map[string]any{"scores": []any{12.0}}},
		{"pull object", `{"$pull":{"ev":{"k":"draft"}}}`, 1, map[string]any{"ev": []any{map[string]any{"k": "paid"}}}},
		{"pull nothing", `{"$pull":{"tags":"zz"}}`, 0, nil},
		{"min lower", `{"$min":{"n":3}}`, 1, map[string]any{"n": 3.0}},
		{"min higher", `{"$min":{"n":9}}`, 0, nil},
		{"max higher", `{"$max":{"n":9}}`, 1, map[string]any{"n": 9.0}},
		{"max string", `{"$max":{"s":"z"}}`, 1, map[string]any{"s": "z"}},
		{"min missing", `{"$min":{"lo":4}}`, 1, map[string]any{"lo": 4.0}},
		{"setOnInsert on a match", `{"$setOnInsert":{"s":"new"}}`, 0, nil},
		{"many operators", `{"$inc":{"n":1},"$push":{"tags":"z"},"$set":{"s":"q"}}`, 1, map[string]any{"n": 6.0, "s": "q"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, h := newHandler(t)

			doc := `{"_id":"o1","n":5,"s":"m","tags":["a","b","a"],"scores":[1,8,12],"ev":[{"k":"draft"},{"k":"paid"}]}`

			expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[`+doc+`]}`), http.StatusOK)

			rec := call(h, "POST", "/orders/_update_by_query", `{"query":{"n":{"$gte":1}},"update":`+c.update+`}`)

			expectStatus(t, rec, http.StatusOK)

			got := jsonBody(t, rec)
			out, _ := got["doc"].(map[string]any)

			if got["matched"] != 1.0 || got["modified"] != c.modified || out == nil {
				t.Fatalf("answer = %v", got)
			}

			wantV := 1.0 + c.modified

			if out["_v"] != wantV {
				t.Fatalf("_v = %v, want %v (no new version when nothing changes)", out["_v"], wantV)
			}

			for k, v := range c.want {
				if v == nil {
					if _, ok := out[k]; ok {
						t.Fatalf("%s still set: %v", k, out)
					}

					continue
				}

				if fmt.Sprint(out[k]) != fmt.Sprint(v) {
					t.Fatalf("%s = %v, want %v", k, out[k], v)
				}
			}
		})
	}
}

func TestQueriesUpdateByQueryErrors(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/orders", `{"indexes":["g"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o1","g":1,"n":5,"s":"m","tags":["a"]}]}`), http.StatusOK)

	cases := map[string]struct {
		body, code string
		status     int
	}{
		"no query":            {`{"update":{"$set":{"a":1}}}`, "empty_filter", http.StatusBadRequest},
		"no update":           {`{"query":{"_id":"o1"}}`, "no_update", http.StatusBadRequest},
		"inc a string":        {`{"query":{"_id":"o1"},"update":{"$inc":{"s":1}}}`, "invalid_field", http.StatusBadRequest},
		"inc by a string":     {`{"query":{"_id":"o1"},"update":{"$inc":{"n":"1"}}}`, "invalid_field", http.StatusBadRequest},
		"push to a number":    {`{"query":{"_id":"o1"},"update":{"$push":{"n":1}}}`, "invalid_field", http.StatusBadRequest},
		"max another type":    {`{"query":{"_id":"o1"},"update":{"$max":{"s":1}}}`, "invalid_field", http.StatusBadRequest},
		"set not an object":   {`{"query":{"_id":"o1"},"update":{"$set":"x"}}`, "invalid_field", http.StatusBadRequest},
		"parent and child":    {`{"query":{"_id":"o1"},"update":{"$set":{"a":1},"$unset":{"a.b":true}}}`, "conflicting_update", http.StatusBadRequest},
		"set _id":             {`{"query":{"_id":"o1"},"update":{"$set":{"_id":"x"}}}`, "immutable_id", http.StatusBadRequest},
		"inc _id":             {`{"query":{"_id":"o1"},"update":{"$inc":{"_id":1}}}`, "immutable_id", http.StatusBadRequest},
		"plain key in update": {`{"query":{"_id":"o1"},"update":{"n":1}}`, "unknown_operator", http.StatusBadRequest},
		"bad filter":          {`{"query":{"n":{"$bogus":1}},"update":{"$set":{"a":1}}}`, "invalid_query", http.StatusBadRequest},
		"filter in update":    {`{"query":{"_id":"o1"},"update":{"$gt":{"n":1}}}`, "unknown_operator", http.StatusBadRequest},
		"many unindexed":      {`{"query":{"s":"m"},"update":{"$set":{"a":1}},"many":true}`, "unindexed_many", http.StatusBadRequest},
		"unknown key":         {`{"query":{"_id":"o1"},"update":{"$set":{"a":1}},"multi":true}`, "invalid_request", http.StatusBadRequest},
		"bad expire_in":       {`{"query":{"_id":"o1"},"update":{"$set":{"a":1}},"expire_in":"soon"}`, "invalid_request", http.StatusBadRequest},
		"unknown collection":  {`{"query":{"_id":"o1"},"update":{"$set":{"a":1}}}`, "unknown_collection", http.StatusNotFound},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := "/orders/_update_by_query"

			if name == "unknown collection" {
				path = "/nope/_update_by_query"
			}

			cqError(t, call(h, "POST", path, c.body), c.status, c.code)
		})
	}
}

func TestQueriesUpdateByQueryAnswers(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/users", `{"indexes":["email","plan"],"expires_field":"exp"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/users/_bulk", `{"docs":[{"_id":"u1","email":"a@b.c","plan":"free"},{"_id":"u2","email":"d@e.f","plan":"free"}]}`), http.StatusOK)
	indexesReady(t, h, "users")

	// Nothing matched is not an error.
	none := jsonBody(t, call(h, "POST", "/users/_update_by_query", `{"query":{"email":"zz"},"update":{"$set":{"x":1}}}`))

	if len(none) != 2 || none["matched"] != 0.0 || none["modified"] != 0.0 {
		t.Fatalf("no match = %v, want {matched:0, modified:0}", none)
	}

	// many: every match, and no doc.
	many := jsonBody(t, call(h, "POST", "/users/_update_by_query", `{"query":{"plan":"free"},"update":{"$set":{"plan":"pro"}},"many":true}`))

	if len(many) != 2 || many["matched"] != 2.0 || many["modified"] != 2.0 {
		t.Fatalf("many = %v", many)
	}

	// upsert: the equality fields (dot paths too) and the operators.
	up := jsonBody(t, call(h, "POST", "/users/_update_by_query", `{"query":{"email":"new@x.y","meta.city":"Rio","age":{"$gte":18}},"update":{"$setOnInsert":{"name":"Ana"},"$inc":{"logins":1},"$push":{"log":"in"},"$max":{"hi":3}},"upsert":true}`))
	doc, _ := up["doc"].(map[string]any)

	if up["matched"] != 0.0 || up["modified"] != 1.0 || doc == nil {
		t.Fatalf("upsert = %v", up)
	}

	if doc["email"] != "new@x.y" || fmt.Sprint(doc["meta"]) != "map[city:Rio]" || doc["name"] != "Ana" || doc["logins"] != 1.0 || fmt.Sprint(doc["log"]) != "[in]" || doc["hi"] != 3.0 || doc["_v"] != 1.0 {
		t.Fatalf("upserted doc = %v", doc)
	}

	if _, ok := doc["age"]; ok {
		t.Fatalf("a range is not an equality field: %v", doc)
	}

	if id, _ := doc["_id"].(string); len(id) != 36 {
		t.Fatalf("generated _id = %v", doc["_id"])
	}

	// The second call finds it: $setOnInsert does nothing then.
	again := jsonBody(t, call(h, "POST", "/users/_update_by_query", `{"query":{"email":"new@x.y"},"update":{"$setOnInsert":{"name":"Bob"},"$inc":{"logins":1}},"upsert":true}`))
	doc, _ = again["doc"].(map[string]any)

	if again["matched"] != 1.0 || doc["name"] != "Ana" || doc["logins"] != 2.0 {
		t.Fatalf("second upsert = %v", again)
	}

	// The id exists, but the rest of the query does not match.
	cqError(t, call(h, "POST", "/users/_update_by_query", `{"query":{"_id":"u1","email":"other"},"update":{"$set":{"x":1}},"upsert":true}`), http.StatusConflict, "duplicate_id")

	// expire_in sets the expires_field.
	exp := jsonBody(t, call(h, "POST", "/users/_update_by_query", `{"query":{"_id":"u2"},"update":{"$set":{"x":1}},"expire_in":"1h"}`))
	doc, _ = exp["doc"].(map[string]any)

	at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(doc["exp"]))
	if err != nil || time.Until(at) < 59*time.Minute || time.Until(at) > 61*time.Minute {
		t.Fatalf("exp = %v (%v)", doc["exp"], err)
	}

	// A version guard: a stale _v matches nothing.
	stale := jsonBody(t, call(h, "POST", "/users/_update_by_query", `{"query":{"_id":"u2","_v":1},"update":{"$set":{"x":2}}}`))

	if stale["matched"] != 0.0 {
		t.Fatalf("stale guard = %v", stale)
	}
}

// "Integers stay exact" (query.md, write operators). An integer above 2^53
// fits int64, so $inc can add 1 exactly.
func TestQueriesIncKeepsBigIntegersExact(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/c/_bulk", `{"docs":[{"_id":"a","n":1152921504606846976},{"_id":"b","n":9223372036854775807}]}`), http.StatusOK)

	rec := call(h, "POST", "/c/_update_by_query", `{"query":{"_id":"a"},"update":{"$inc":{"n":1}}}`)

	if !strings.Contains(rec.Body.String(), `"n":1152921504606846977`) || !strings.Contains(rec.Body.String(), `"modified":1`) {
		t.Fatalf("2^60 + 1 = %s", rec.Body.String())
	}

	rec = call(h, "POST", "/c/_update_by_query", `{"query":{"_id":"b"},"update":{"$inc":{"n":1}}}`)

	if strings.Contains(rec.Body.String(), `"modified":0`) {
		t.Fatalf("an int64 overflow must write a float or fail with invalid_field, not do nothing: %s", rec.Body.String())
	}
}

func TestQueriesDeleteByQuery(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/calls", `{"indexes":["status"]}`), http.StatusOK)

	docs := `[{"_id":"c1","status":"failed","n":1},{"_id":"c2","status":"failed","n":2},{"_id":"c3","status":"ok","n":3},{"_id":"c4","status":"ok","n":4}]`

	expectStatus(t, call(h, "POST", "/calls/_bulk", `{"docs":`+docs+`}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/calls/_compact", ``), http.StatusOK)
	indexesReady(t, h, "calls")

	cases := map[string]struct {
		body, code string
	}{
		"empty query":       {`{"query":{}}`, "empty_filter"},
		"many unindexed":    {`{"query":{"n":1},"many":true}`, "unindexed_many"},
		"write op in query": {`{"query":{"n":1,"$set":{"a":1}}}`, "unknown_operator"},
		"bad filter":        {`{"query":{"n":{"$bogus":1}}}`, "invalid_query"},
		"unknown key":       {`{"query":{"n":1},"update":{}}`, "invalid_request"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cqError(t, call(h, "POST", "/calls/_delete_by_query", c.body), http.StatusBadRequest, c.code)
		})
	}

	cqError(t, call(h, "POST", "/nope/_delete_by_query", `{"query":{"n":1}}`), http.StatusNotFound, "unknown_collection")

	answers := []struct {
		body, want string
	}{
		{`{"query":{"status":"ok"}}`, `{"deleted":1}`},
		{`{"query":{"status":"none"},"many":true}`, `{"deleted":0}`},
		{`{"query":{"status":{"$in":["failed","ok"]}},"many":true}`, `{"deleted":3}`},
		{`{"query":{"status":"failed"}}`, `{"deleted":0}`},
	}

	for _, a := range answers {
		if got := strings.TrimSpace(call(h, "POST", "/calls/_delete_by_query", a.body).Body.String()); got != a.want {
			t.Fatalf("%s = %s, want %s", a.body, got, a.want)
		}
	}

	if got := strings.TrimSpace(call(h, "POST", "/calls/_count", ``).Body.String()); got != `{"count":0}` {
		t.Fatalf("count after the deletes = %s", got)
	}
}

// "An empty query is empty_filter" (queries.md): a body with no query at all
// is the same empty filter.
func TestQueriesDeleteWithoutQueryIsEmptyFilter(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/calls/_bulk", `{"docs":[{"_id":"c1"}]}`), http.StatusOK)

	cqError(t, call(h, "POST", "/calls/_delete_by_query", `{}`), http.StatusBadRequest, "empty_filter")
}

func TestQueriesLookupRules(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/customers", `{"indexes":["code"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/customers/_bulk", `{"docs":[{"_id":"c1","name":"Ana","code":"A"},{"_id":"c2","name":"Bo","code":"B"}]}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/invoices", `{"indexes":["order_id"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/invoices/_bulk", `{"docs":[{"_id":"i1","order_id":"o1","n":1},{"_id":"i2","order_id":"o1","n":2}]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o1","cid":"c1","customer":"stored","codes":["A","B","Z"]},{"_id":"o2","cid":"zz"}]}`), http.StatusOK)
	indexesReady(t, h, "customers")
	indexesReady(t, h, "invoices")

	rows := func(body string) string {
		t.Helper()

		rec := call(h, "POST", "/orders/_search", body)

		expectStatus(t, rec, http.StatusOK)

		return strings.TrimSpace(rec.Body.String())
	}

	cases := []struct {
		name, body, want string
	}{
		{
			"belongs_to: an object or null; the name replaces a stored field; fields drops the fk it added",
			`{"fields":["_id"],"lookup":{"customer":{"belongs_to":"customers","fk":"cid","fields":["name"]}}}`,
			`{"_id":"o1","customer":{"name":"Ana"}}` + "\n" + `{"_id":"o2","customer":null}`,
		},
		{
			"an array fk gives an array; pk names the other field",
			`{"fields":["_id"],"lookup":{"cs":{"belongs_to":"customers","fk":"codes","pk":"code","fields":["name"]}}}`,
			`{"_id":"o1","cs":[{"name":"Ana"},{"name":"Bo"}]}` + "\n" + `{"_id":"o2","cs":null}`,
		},
		{
			"has_one: the first in sort order, or null",
			`{"fields":["_id"],"lookup":{"inv":{"has_one":"invoices","fk":"order_id","sort":{"n":-1},"fields":["n"]}}}`,
			`{"_id":"o1","inv":{"n":2}}` + "\n" + `{"_id":"o2","inv":null}`,
		},
		{
			"has_many: an array with query, sort and limit; empty when none",
			`{"fields":["_id"],"lookup":{"inv":{"has_many":"invoices","fk":"order_id","query":{"n":{"$gte":1}},"sort":{"n":-1},"limit":1,"fields":["n"]}}}`,
			`{"_id":"o1","inv":[{"n":2}]}` + "\n" + `{"_id":"o2","inv":[]}`,
		},
		{
			"long names: foreign_key and primary_key",
			`{"query":{"_id":"o1"},"fields":["_id"],"lookup":{"c":{"belongs_to":"customers","foreign_key":"cid","primary_key":"_id","fields":["name"]}}}`,
			`{"_id":"o1","c":{"name":"Ana"}}`,
		},
		{
			"two levels",
			`{"query":{"_id":"o1"},"fields":["_id"],"lookup":{"inv":{"has_many":"invoices","fk":"order_id","sort":{"n":1},"fields":["n","order_id"],"lookup":{"o":{"belongs_to":"orders","fk":"order_id","fields":["cid"]}}}}}`,
			`{"_id":"o1","inv":[{"n":1,"order_id":"o1","o":{"cid":"c1"}},{"n":2,"order_id":"o1","o":{"cid":"c1"}}]}`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rows(c.body); got != c.want {
				t.Fatalf("rows =\n%s\nwant\n%s", got, c.want)
			}
		})
	}

	errs := map[string]string{
		"three levels":                `{"lookup":{"a":{"belongs_to":"customers","fk":"cid","lookup":{"b":{"belongs_to":"customers","fk":"_id","lookup":{"c":{"belongs_to":"customers","fk":"_id"}}}}}}}`,
		"has_many limit over 1000":    `{"lookup":{"i":{"has_many":"invoices","fk":"order_id","limit":1001}}}`,
		"has_many limit 0":            `{"lookup":{"i":{"has_many":"invoices","fk":"order_id","limit":0}}}`,
		"belongs_to with a query":     `{"lookup":{"c":{"belongs_to":"customers","fk":"cid","query":{"name":"Ana"}}}}`,
		"two relations in one":        `{"lookup":{"c":{"belongs_to":"customers","has_one":"invoices","fk":"cid"}}}`,
		"fk and foreign_key":          `{"lookup":{"c":{"belongs_to":"customers","fk":"cid","foreign_key":"cid"}}}`,
		"unknown key":                 `{"lookup":{"c":{"belongs_to":"customers","fk":"cid","as":"x"}}}`,
		"_id as the name":             `{"lookup":{"_id":{"belongs_to":"customers","fk":"cid"}}}`,
		"lookup not an object":        `{"lookup":[1]}`,
		"has_one with a limit":        `{"lookup":{"i":{"has_one":"invoices","fk":"order_id","limit":1}}}`,
		"a pattern as the other side": `{"lookup":{"c":{"belongs_to":"cust*","fk":"cid"}}}`,
	}

	for name, body := range errs {
		t.Run(name, func(t *testing.T) {
			cqError(t, call(h, "POST", "/orders/_search", body), http.StatusBadRequest, "lookup_invalid")
		})
	}

	cqError(t, call(h, "POST", "/orders/_search", `{"lookup":{"c":{"belongs_to":"customers","fk":"cid","pk":"name"}}}`), http.StatusBadRequest, "lookup_unindexed")
}

// One answer attaches 10 000 documents at most.
func TestQueriesLookupTooLarge(t *testing.T) {
	_, h := newHandler(t)

	const n = 10_001

	docs := make([]string, n)
	keys := make([]string, n)

	for i := range n {
		docs[i] = fmt.Sprintf(`{"_id":"c%05d"}`, i)
		keys[i] = fmt.Sprintf(`"c%05d"`, i)
	}

	expectStatus(t, call(h, "POST", "/customers/_bulk", `{"docs":[`+strings.Join(docs, ",")+`]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/orders/_bulk", `{"docs":[{"_id":"o1","cids":[`+strings.Join(keys, ",")+`]}]}`), http.StatusOK)

	rec := call(h, "POST", "/orders/_search", `{"lookup":{"cs":{"belongs_to":"customers","fk":"cids"}}}`)

	cqError(t, rec, http.StatusUnprocessableEntity, "lookup_too_large")
}
