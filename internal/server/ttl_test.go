package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var m map[string]any

	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not JSON: %q", rec.Body.String())
	}

	return m
}

func ttlOf(t *testing.T, rec *httptest.ResponseRecorder) float64 {
	t.Helper()

	expectStatus(t, rec, http.StatusOK)

	return jsonBody(t, rec)["ttl"].(float64)
}

func TestTTLEndpoints(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/sessions", `{"expires_field":"exp"}`), http.StatusOK)

	rec := call(h, "POST", "/v1/c/sessions/insert", `{"insert":[{"_id":"s1"},{"_id":"s2"}],"expire_in":"1h"}`)
	expectStatus(t, rec, http.StatusOK)

	if got := ttlOf(t, call(h, "GET", "/v1/c/sessions/s1/ttl", "")); got < 3599 || got > 3600 {
		t.Fatalf("ttl s1 = %v", got)
	}

	rec = call(h, "POST", "/v1/c/sessions/insert", `{"insert":[{"_id":"s3"}],"expire_in":90}`)
	expectStatus(t, rec, http.StatusOK)

	if got := ttlOf(t, call(h, "GET", "/v1/c/sessions/s3/ttl", "")); got < 89 || got > 90 {
		t.Fatalf("ttl s3 = %v", got)
	}

	rec = call(h, "POST", "/v1/c/sessions/update?expire_in=30m", `{"_id":"s1","$set":{"n":1}}`)
	expectStatus(t, rec, http.StatusOK)

	if got := ttlOf(t, call(h, "GET", "/v1/c/sessions/s1/ttl", "")); got < 1799 || got > 1800 {
		t.Fatalf("ttl s1 after update = %v", got)
	}

	rec = call(h, "POST", "/v1/c/sessions/expire", `{"_id":"s2","in":"10m"}`)
	expectStatus(t, rec, http.StatusOK)

	body := jsonBody(t, rec)

	if body["_id"] != "s2" || body["v"].(float64) != 2 || body["expires_at"] == "" {
		t.Fatalf("expire body = %v", body)
	}

	rec = call(h, "POST", "/v1/c/sessions/expire", `{"_id":"s2","at":"2999-01-01T00:00:00Z"}`)
	expectStatus(t, rec, http.StatusOK)

	rec = call(h, "POST", "/v1/c/sessions/persist", `{"_id":"s2"}`)
	expectStatus(t, rec, http.StatusOK)

	rec = call(h, "GET", "/v1/c/sessions/s2/ttl", "")
	expectStatus(t, rec, http.StatusOK)

	if jsonBody(t, rec)["ttl"].(float64) != -1 {
		t.Fatalf("ttl after persist = %s", rec.Body.String())
	}

	rec = call(h, "POST", "/v1/c/sessions/expire", `{"_id":"s2","at":"2020-01-01T00:00:00Z"}`)
	expectStatus(t, rec, http.StatusOK)

	for _, c := range []struct{ method, path, body string }{
		{"GET", "/v1/c/sessions/s2/ttl", ""},
		{"GET", "/v1/c/sessions/nope/ttl", ""},
		{"POST", "/v1/c/sessions/expire", `{"_id":"s2","in":"1h"}`},
		{"POST", "/v1/c/sessions/persist", `{"_id":"nope"}`},
	} {
		rec = call(h, c.method, c.path, c.body)
		expectStatus(t, rec, http.StatusNotFound)

		if code := errorCode(t, rec); code != "not_found" {
			t.Fatalf("%s %s code = %s", c.method, c.path, code)
		}
	}
}

func TestTTLErrors(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/plain", `{}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/v1/c/derived", `{"time":"at","ttl":"1h"}`), http.StatusOK)
	expectStatus(t, call(h, "PUT", "/v1/c/ex", `{"expires_field":"exp"}`), http.StatusOK)
	expectStatus(t, call(h, "POST", "/v1/c/plain/insert", `{"insert":[{"_id":"a"}]}`), http.StatusOK)

	cases := []struct {
		path, body, code string
	}{
		{"/v1/c/plain/insert", `{"insert":[{"_id":"b"}],"expire_in":"1h"}`, "ttl_not_enabled"},
		{"/v1/c/plain/update?expire_in=1h", `{"_id":"a","$set":{"n":1}}`, "ttl_not_enabled"},
		{"/v1/c/ex/update?expire_in=soon", `{"_id":"a","$set":{"n":1}}`, "invalid_request"},
		{"/v1/c/plain/expire", `{"_id":"a","in":"1h"}`, "ttl_not_enabled"},
		{"/v1/c/plain/persist", `{"_id":"a"}`, "ttl_not_enabled"},
		{"/v1/c/derived/expire", `{"_id":"a","in":"1h"}`, "ttl_derived"},
		{"/v1/c/derived/persist", `{"_id":"a"}`, "ttl_derived"},
		{"/v1/c/ex/insert", `{"insert":[{"_id":"b"}],"expire_in":"soon"}`, "invalid_request"},
		{"/v1/c/ex/insert", `{"insert":[{"_id":"b"}],"expire_in":-5}`, "invalid_request"},
		{"/v1/c/ex/expire", `{}`, "invalid_request"},
		{"/v1/c/ex/expire", `{"_id":"b","in":"1h","at":"2999-01-01T00:00:00Z"}`, "invalid_request"},
		{"/v1/c/ex/expire", `{"_id":"b","at":"tomorrow"}`, "invalid_request"},
	}

	for _, c := range cases {
		rec := call(h, "POST", c.path, c.body)
		expectStatus(t, rec, http.StatusBadRequest)

		if code := errorCode(t, rec); code != c.code {
			t.Fatalf("%s %s: code = %s, want %s", c.path, c.body, code, c.code)
		}
	}
}

func TestTTLOfDerivedCollection(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/v1/c/events", `{"time":"at","ttl":"1h"}`), http.StatusOK)

	now := `"` + nowRFC() + `"`

	expectStatus(t, call(h, "POST", "/v1/c/events/insert", `{"insert":[{"_id":"e1","at":`+now+`}]}`), http.StatusOK)

	if got := ttlOf(t, call(h, "GET", "/v1/c/events/e1/ttl", "")); got < 3598 || got > 3600 {
		t.Fatalf("ttl = %v", got)
	}
}

func nowRFC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
