package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// raw sends a request with no Accept header: the default answer format.
func raw(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))

	return rec
}

func TestUnknownRoutes(t *testing.T) {
	_, h := newHandler(t)

	for _, path := range []string{"/", "/_nope", "/v1/c/x/find", "/x/find", "/x/_nope", "/x/_doc/a/b", "/x/y/z/w", "/_blobs/a/b"} {
		rec := call(h, "GET", path, "")

		expectStatus(t, rec, http.StatusNotFound)

		if got := errorCode(t, rec); got != "unknown_route" {
			t.Fatalf("%s: code = %s", path, got)
		}
	}

	rec := call(h, "GET", "/x/_search", "")

	expectStatus(t, rec, http.StatusMethodNotAllowed)

	if rec.Header().Get("Allow") != "POST" || errorCode(t, rec) != "method_not_allowed" {
		t.Fatalf("405: allow %q, body %s", rec.Header().Get("Allow"), rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/_health", ""), http.StatusOK)
	expectStatus(t, call(h, "GET", "/_metrics", ""), http.StatusOK)
	expectStatus(t, call(h, "GET", "/_collections", ""), http.StatusOK)
}

func TestSearchFormats(t *testing.T) {
	h := seedTwo(t)

	body := `{"query":{"n":{"$gte":1}},"sort":{"n":1},"limit":1,"count":true}`

	js := raw(h, "POST", "/posts/_search", body)

	expectStatus(t, js, http.StatusOK)

	if ct := js.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %s", ct)
	}

	var rows []map[string]any

	if err := json.Unmarshal(js.Body.Bytes(), &rows); err != nil || len(rows) != 1 || rows[0]["_id"] != "p1" {
		t.Fatalf("JSON rows = %s (%v)", js.Body.String(), err)
	}

	nd := call(h, "POST", "/posts/_search", body)

	if ct := nd.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content type = %s", ct)
	}

	if strings.TrimSpace(nd.Body.String()) != strings.TrimSpace(strings.Trim(strings.TrimSpace(js.Body.String()), "[]")) {
		t.Fatalf("the rows differ: %q vs %q", nd.Body.String(), js.Body.String())
	}

	for _, h := range []string{"X-Ndb-Next", "X-Ndb-Total", "X-Ndb-Plan", "X-Ndb-Examined", "X-Ndb-Took-Ms"} {
		if js.Header().Get(h) == "" || js.Header().Get(h) == "" {
			t.Fatalf("%s missing", h)
		}
	}

	if js.Header().Get("X-Ndb-Total") != nd.Header().Get("X-Ndb-Total") {
		t.Fatal("total differs between formats")
	}

	empty := raw(h, "POST", "/posts/_search", `{"query":{"n":99}}`)

	if strings.TrimSpace(empty.Body.String()) != "[]" {
		t.Fatalf("no rows = %q", empty.Body.String())
	}

	agg := raw(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":null,"n":{"$count":{}}}}]}`)

	if strings.TrimSpace(agg.Body.String()) != `[{"_id":null,"n":2}]` {
		t.Fatalf("aggregate = %q", agg.Body.String())
	}
}

func TestWhereIsGone(t *testing.T) {
	h := seedTwo(t)

	for _, path := range []string{"/posts/_search", "/posts/_count"} {
		rec := call(h, "POST", path, `{"where":{"n":1}}`)

		expectStatus(t, rec, http.StatusBadRequest)
	}

	rec := call(h, "POST", "/posts/_watch", `{"consumer":"c","where":{"n":1}}`)

	expectStatus(t, rec, http.StatusBadRequest)
}

func TestCount(t *testing.T) {
	h := seedTwo(t)

	for body, want := range map[string]string{
		``:                          `{"count":2}`,
		`{}`:                        `{"count":2}`,
		`{"query":{"n":{"$gt":1}}}`: `{"count":1}`,
		`{"query":{"slug":"nope"}}`: `{"count":0}`,
	} {
		rec := call(h, "POST", "/posts/_count", body)

		expectStatus(t, rec, http.StatusOK)

		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Fatalf("%q: %s, want %s", body, got, want)
		}
	}

	expectStatus(t, call(h, "POST", "/posts/_count", `{"query":{"n":{"$bad":1}}}`), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/nope/_count", `{}`), http.StatusNotFound)
}

func TestInsertOneAndVersionName(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "POST", "/posts/_doc", `{"_id":"p1","n":1}`)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"_id":"p1","_v":1}` {
		t.Fatalf("insert = %s", got)
	}

	rec = call(h, "POST", "/posts/_doc", `{"n":2}`)

	if m := decodeMap(t, rec.Body.String()); m["_id"] == "" || m["_v"] != float64(1) {
		t.Fatalf("generated id = %v", m)
	}

	expectStatus(t, call(h, "POST", "/posts/_doc", `[{"n":1}]`), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/posts/_doc", `{"_id":"p1"}`), http.StatusConflict)

	rec = call(h, "POST", "/posts/_bulk", `{"docs":[{"_id":"b1"},{"_id":"b2"}]}`)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"results":[{"_id":"b1","_v":1},{"_id":"b2","_v":1}]}` {
		t.Fatalf("bulk = %s", got)
	}

	expectStatus(t, call(h, "POST", "/posts/_bulk", `{"docs":[]}`), http.StatusBadRequest)
	expectStatus(t, call(h, "POST", "/posts/_bulk", `{"insert":[{"a":1}]}`), http.StatusBadRequest)
}

func TestIndexRoutes(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "PUT", "/posts/_indexes/n", ``)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"n"`) {
		t.Fatalf("indexes = %s", rec.Body.String())
	}

	rec = call(h, "DELETE", "/posts/_indexes/n", ``)

	expectStatus(t, rec, http.StatusOK)

	if strings.Contains(rec.Body.String(), `"n"`) {
		t.Fatalf("after drop = %s", rec.Body.String())
	}

	expectStatus(t, call(h, "PUT", "/nope/_indexes/n", ``), http.StatusNotFound)
}

func TestUpdateQueryAndUpdateApart(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "POST", "/posts/_update_by_query", `{"query":{"$not":{"slug":"a"}},"update":{"$set":{"x":1}}}`)

	if got := decodeMap(t, rec.Body.String()); got["matched"] != float64(1) {
		t.Fatalf("$not in query = %v", got)
	}

	rec = call(h, "POST", "/posts/_update/p1", `{"query":{"_v":1},"update":{"$inc":{"n":1}},"many":true}`)

	expectStatus(t, rec, http.StatusBadRequest)
}
