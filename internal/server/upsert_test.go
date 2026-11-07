package server_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestBulkUpsert(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/users", `{"unique":["email"]}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/users/_doc", `{"_id":"u1","name":"Ana","plan":"free","email":"a@x"}`), http.StatusOK)

	// u1 exists: its fields merge; u2 is new.
	rec := call(h, "POST", "/users/_bulk", `{"docs":[{"_id":"u1","plan":"pro"},{"_id":"u2","name":"Bia"}],"upsert":true}`)

	expectStatus(t, rec, http.StatusOK)

	if got := strings.TrimSpace(rec.Body.String()); got != `{"results":[{"_id":"u1","_v":2},{"_id":"u2","_v":1}]}` {
		t.Fatalf("upsert = %s", got)
	}

	doc := decodeMap(t, call(h, "GET", "/users/_doc/u1", "").Body.String())

	if doc["name"] != "Ana" || doc["plan"] != "pro" || doc["email"] != "a@x" || doc["_v"] != float64(2) {
		t.Fatalf("merged = %v", doc)
	}

	// Without upsert, an existing _id is still duplicate_id.
	expectStatus(t, call(h, "POST", "/users/_bulk", `{"docs":[{"_id":"u1"}]}`), http.StatusConflict)

	// All or nothing: a unique violation leaves u3 out too.
	rec = call(h, "POST", "/users/_bulk", `{"docs":[{"_id":"u3","name":"Cai"},{"_id":"u2","email":"a@x"}],"upsert":true}`)

	expectStatus(t, rec, http.StatusConflict)
	expectStatus(t, call(h, "GET", "/users/_doc/u3", ""), http.StatusNotFound)

	// A document without _id is an insert.
	rec = call(h, "POST", "/users/_bulk", `{"docs":[{"name":"Dan"}],"upsert":true}`)

	if m := decodeMap(t, rec.Body.String()); m["results"] == nil {
		t.Fatalf("no _id = %s", rec.Body.String())
	}

	// expire_in needs expires_field.
	rec = call(h, "POST", "/users/_bulk", `{"docs":[{"_id":"u1"}],"upsert":true,"expire_in":"1h"}`)

	if got := errorCode(t, rec); got != "ttl_not_enabled" {
		t.Fatalf("expire_in = %s", got)
	}
}
