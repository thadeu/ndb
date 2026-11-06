package server_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func seedTwo(t *testing.T) http.Handler {
	t.Helper()

	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/posts", `{"unique":["slug"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/v1/c/posts/insert", `{"insert":[{"_id":"p1","slug":"a","n":1},{"_id":"p2","slug":"b","n":2}]}`), http.StatusOK)

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

	rec := call(h, "PUT", "/v1/c/calls", `{"id":"call_id"}`)

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

	rec := call(h, "POST", "/v1/c/posts/insert", `{"insert":[{"_id":"mine","n":1},{"n":2}]}`)

	expectStatus(t, rec, http.StatusOK)

	res := decodeMap(t, rec.Body.String())["results"].([]any)
	generated := res[1].(map[string]any)["_id"].(string)

	if res[0].(map[string]any)["_id"] != "mine" || len(generated) != 36 {
		t.Fatalf("results = %v", res)
	}

	rec = call(h, "GET", "/v1/c/posts/"+generated, "")

	expectStatus(t, rec, http.StatusOK)

	if doc := decodeMap(t, rec.Body.String()); doc["_id"] != generated || doc["_v"] != float64(1) {
		t.Fatalf("doc = %v", doc)
	}

	expectStatus(t, call(h, "POST", "/v1/c/posts/insert", `{"insert":[{"_id":""}]}`), http.StatusBadRequest)
}

func TestGetByID(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "GET", "/v1/c/posts/p2", "")

	expectStatus(t, rec, http.StatusOK)

	if doc := decodeMap(t, rec.Body.String()); doc["slug"] != "b" || doc["_id"] != "p2" || doc["_v"] != float64(1) {
		t.Fatalf("doc = %v", doc)
	}

	rec = call(h, "GET", "/v1/c/posts/nope", "")

	expectStatus(t, rec, http.StatusNotFound)

	if got := errorCode(t, rec); got != "not_found" {
		t.Fatalf("code = %q", got)
	}
}

func TestUpdateByID(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "POST", "/v1/c/posts/p1/update", `{"$inc":{"n":5}}`)

	expectStatus(t, rec, http.StatusOK)

	out := decodeMap(t, rec.Body.String())
	doc := out["doc"].(map[string]any)

	if out["matched"] != float64(1) || out["modified"] != float64(1) || doc["n"] != float64(6) || doc["_v"] != float64(2) {
		t.Fatalf("out = %v", out)
	}

	rec = call(h, "POST", "/v1/c/posts/p1/update", `{"_v":1,"$set":{"n":0}}`)

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["matched"] != float64(0) {
		t.Fatalf("a stale _v guard must not match: %v", out)
	}

	rec = call(h, "POST", "/v1/c/posts/p1/update", `{"n":{"$gt":100},"$set":{"x":1}}`)

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["matched"] != float64(0) {
		t.Fatalf("a failed guard must not match: %v", out)
	}

	rec = call(h, "POST", "/v1/c/posts/p1/update", `{"_id":"p2","$set":{"x":1}}`)

	expectStatus(t, rec, http.StatusBadRequest)

	rec = call(h, "POST", "/v1/c/posts/p1/update", `{"n":1}`)

	expectStatus(t, rec, http.StatusBadRequest)

	if got := errorCode(t, rec); got != "no_update" {
		t.Fatalf("code = %q", got)
	}
}

func TestUpdateByIDUpsertAndExpire(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/sessions", `{"expires_field":"exp"}`), http.StatusOK)

	rec := call(h, "POST", "/v1/c/sessions/s1/update?upsert=true&expire_in=1h", `{"$set":{"user":"u"}}`)

	expectStatus(t, rec, http.StatusOK)

	doc := decodeMap(t, rec.Body.String())["doc"].(map[string]any)

	if doc["_id"] != "s1" || doc["user"] != "u" || doc["exp"] == nil {
		t.Fatalf("doc = %v", doc)
	}

	rec = call(h, "POST", "/v1/c/sessions/nope/update", `{"$set":{"user":"u"}}`)

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["matched"] != float64(0) {
		t.Fatalf("no upsert, no match: %v", out)
	}
}

func TestDeleteByID(t *testing.T) {
	h := seedTwo(t)

	rec := call(h, "DELETE", "/v1/c/posts/p1", "")

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["deleted"] != float64(1) {
		t.Fatalf("out = %v", out)
	}

	rec = call(h, "DELETE", "/v1/c/posts/p1", "")

	if out := decodeMap(t, rec.Body.String()); out["deleted"] != float64(0) {
		t.Fatalf("out = %v", out)
	}

	expectStatus(t, call(h, "GET", "/v1/c/posts/p1", ""), http.StatusNotFound)
	expectStatus(t, call(h, "GET", "/v1/c/posts/p2", ""), http.StatusOK)
}

func TestTTLByIDRoute(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/sessions", `{"expires_field":"exp"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/v1/c/sessions/insert", `{"insert":[{"_id":"s1"}],"expire_in":"1h"}`), http.StatusOK)

	rec := call(h, "GET", "/v1/c/sessions/s1/ttl", "")

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["ttl"].(float64) < 3590 {
		t.Fatalf("out = %v", out)
	}

	expectStatus(t, call(h, "GET", "/v1/c/sessions/ttl/s1", ""), http.StatusNotFound)
}

func TestFixedRoutesWinOverAnID(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/x", `{}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/v1/c/x/insert", `{"insert":[{"_id":"stats"},{"_id":"quarantine"},{"_id":"find"}]}`), http.StatusOK)

	rec := call(h, "GET", "/v1/c/x/stats", "")

	expectStatus(t, rec, http.StatusOK)

	if out := decodeMap(t, rec.Body.String()); out["rows"] == nil && out["name"] == nil && out["write"] == nil {
		t.Fatalf("stats must answer, not the document: %v", out)
	}

	if doc := decodeMap(t, rec.Body.String()); doc["_id"] != nil {
		t.Fatalf("GET /stats returned a document: %v", doc)
	}

	rec = call(h, "POST", "/v1/c/x/find", `{"where":{}}`)

	expectStatus(t, rec, http.StatusOK)

	if lines := strings.Count(strings.TrimSpace(rec.Body.String()), "\n") + 1; lines != 3 {
		t.Fatalf("find must run as find: %q", rec.Body.String())
	}
}
