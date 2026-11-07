package server_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func seedTwo(t *testing.T) http.Handler {
	t.Helper()

	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/posts", `{"unique":["slug"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/posts/_bulk", `{"docs":[{"_id":"p1","slug":"a","n":1},{"_id":"p2","slug":"b","n":2}]}`), http.StatusOK)

	return h
}

func decodeMap(t *testing.T, body string) map[string]any {
	t.Helper()

	var m map[string]any

	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %q", body)
	}

	return m
}

func TestDefinitionWithIDIsCatalogInvalid(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "PUT", "/calls", `{"id":"call_id"}`)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "catalog_invalid" {
		t.Fatalf("code = %q", got)
	}

	if !strings.Contains(rec.Body.String(), "unique") {
		t.Fatalf("message must point to unique: %s", rec.Body.String())
	}
}

func TestInsertWithAndWithoutID(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "POST", "/posts/_bulk", `{"docs":[{"_id":"mine","n":1},{"n":2}]}`)

	expectStatus(t, rec, http.StatusOK)

	res := decodeMap(t, rec.Body.String())["results"].([]any)
	generated := res[1].(map[string]any)["_id"].(string)

	if res[0].(map[string]any)["_id"] != "mine" || len(generated) != 36 {
		t.Fatalf("results = %v", res)
	}

	rec = call(h, "GET", "/posts/_doc/"+generated, "")

	expectStatus(t, rec, http.StatusOK)

	if doc := decodeMap(t, rec.Body.String()); doc["_id"] != generated || doc["_v"] != float64(1) {
		t.Fatalf("doc = %v", doc)
	}

	expectStatus(t, call(h, "POST", "/posts/_bulk", `{"docs":[{"_id":""}]}`), http.StatusBadRequest)
}

func TestGetByID(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "GET", "/posts/_doc/p2", "")

	expectStatus(t, rec, http.StatusOK)

	if doc := decodeMap(t, rec.Body.String()); doc["slug"] != "b" || doc["_id"] != "p2" || doc["_v"] != float64(1) {
		t.Fatalf("doc = %v", doc)
	}

	rec = call(h, "GET", "/posts/_doc/nope", "")

	expectStatus(t, rec, http.StatusNotFound)

	if got := errorCode(t, rec); got != "not_found" {
		t.Fatalf("code = %q", got)
	}
}

func TestUpdateByID(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "POST", "/posts/_update/p1", `{"update":{"$inc":{"n":5}}}`)

	expectStatus(t, rec, http.StatusOK)

	out := decodeMap(t, rec.Body.String())
	doc := out["doc"].(map[string]any)

	if out["matched"] != float64(1) || out["modified"] != float64(1) || doc["n"] != float64(6) || doc["_v"] != float64(2) {
		t.Fatalf("out = %v", out)
	}

	rec = call(h, "POST", "/posts/_update/p1", `{"query":{"_v":1},"update":{"$set":{"n":0}}}`)

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["matched"] != float64(0) {
		t.Fatalf("a stale _v guard must not match: %v", out)
	}

	rec = call(h, "POST", "/posts/_update/p1", `{"query":{"n":{"$gt":100}},"update":{"$set":{"x":1}}}`)

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["matched"] != float64(0) {
		t.Fatalf("a failed guard must not match: %v", out)
	}

	rec = call(h, "POST", "/posts/_update/p1", `{"query":{"_id":"p2"},"update":{"$set":{"x":1}}}`)

	expectStatus(t, rec, http.StatusBadRequest)

	rec = call(h, "POST", "/posts/_update/p1", `{"query":{"n":1},"update":{}}`)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "no_update" {
		t.Fatalf("code = %q", got)
	}
}

func TestUpdateByIDUpsertAndExpire(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/sessions", `{"expires_field":"exp"}`), http.StatusOK)

	rec := call(h, "POST", "/sessions/_update/s1", `{"update":{"$set":{"user":"u"}},"upsert":true,"expire_in":"1h"}`)

	expectStatus(t, rec, http.StatusOK)

	doc := decodeMap(t, rec.Body.String())["doc"].(map[string]any)

	if doc["_id"] != "s1" || doc["user"] != "u" || doc["exp"] == nil {
		t.Fatalf("doc = %v", doc)
	}

	rec = call(h, "POST", "/sessions/_update/nope", `{"update":{"$set":{"user":"u"}}}`)

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["matched"] != float64(0) {
		t.Fatalf("no upsert, no match: %v", out)
	}
}

func TestDeleteByID(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "DELETE", "/posts/_doc/p1", "")

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["deleted"] != float64(1) {
		t.Fatalf("out = %v", out)
	}

	rec = call(h, "DELETE", "/posts/_doc/p1", "")

	if out := decodeMap(t, rec.Body.String()); out["deleted"] != float64(0) {
		t.Fatalf("out = %v", out)
	}

	expectStatus(t, call(h, "GET", "/posts/_doc/p1", ""), http.StatusNotFound)
	expectStatus(t, call(h, "GET", "/posts/_doc/p2", ""), http.StatusOK)
}

func TestTTLByIDRoute(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/sessions", `{"expires_field":"exp"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/sessions/_bulk", `{"docs":[{"_id":"s1"}],"expire_in":"1h"}`), http.StatusOK)

	rec := call(h, "GET", "/sessions/_ttl/s1", "")

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["ttl"].(float64) < 3590 {
		t.Fatalf("out = %v", out)
	}

	expectStatus(t, call(h, "GET", "/sessions/ttl/s1", ""), http.StatusNotFound)
}

func TestIDsNeverCollideWithRoutes(t *testing.T) {
	_, h := newHandler(t)

	ids := []string{"stats", "_search", "find", "a/b"}

	expectStatus(t, call(h, "POST", "/x/_bulk", `{"docs":[{"_id":"stats"},{"_id":"_search"},{"_id":"find"},{"_id":"a/b"}]}`), http.StatusOK)

	for _, id := range ids {
		rec := call(h, "GET", "/x/_doc/"+url.PathEscape(id), "")

		expectStatus(t, rec, http.StatusOK)

		if doc := decodeMap(t, rec.Body.String()); doc["_id"] != id {
			t.Fatalf("%s: got %v", id, doc)
		}
	}

	rec := call(h, "GET", "/x/_stats", "")

	if doc := decodeMap(t, rec.Body.String()); doc["_id"] != nil {
		t.Fatalf("GET /x/_stats returned a document: %v", doc)
	}
}
