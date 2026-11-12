package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// Helpers that the tests of more than one route share.

func expectCode(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()

	expectStatus(t, rec, status)

	if got := errorCode(t, rec); got != code {
		t.Fatalf("code %q, want %q", got, code)
	}
}

func docOf(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()

	rec := call(h, "GET", path, "")
	if rec.Code == http.StatusNotFound {
		return nil
	}

	var doc map[string]any

	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("doc %s: %q", path, rec.Body.String())
	}

	return doc
}

func waitIndexReady(t *testing.T, h http.Handler, coll string) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if body := call(h, "GET", "/"+coll+"/_stats", "").Body.String(); !strings.Contains(body, `"building"`) {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("the indexes never became ready")
}

func planOf(t *testing.T, h http.Handler, path, body string) string {
	t.Helper()

	rec := call(h, "POST", path, body)
	expectStatus(t, rec, http.StatusOK)

	return rec.Header().Get("X-Ndb-Plan")
}

// aggOf runs a pipeline and gives its rows as canonical JSON, in order.
func aggOf(t *testing.T, h http.Handler, coll, pipeline string) []string {
	t.Helper()

	path := "/" + coll + "/_aggregate"

	if name, query, ok := strings.Cut(coll, "?"); ok {
		path = "/" + name + "/_aggregate?" + query
	}

	rec := call(h, "POST", path, `{"pipeline":`+pipeline+`,"limit":1000}`)
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
