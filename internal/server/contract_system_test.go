package server_test

// Contract tests of the system routes, the router, the auth and the
// transport, as docs/dev/http.md, http/system.md, http/errors.md and
// configuration.md (listen, tokens) promise them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

// tcpDo sends one request to a TCP server, with a bearer token when token
// is not empty, and returns the answer and its body.
func tcpDo(t *testing.T, c *http.Client, addr, method, path, body, token string) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))

	if err != nil {
		t.Fatal(err)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.Do(req)

	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)

	if err != nil {
		t.Fatal(err)
	}

	return resp, string(data)
}

// errorBody checks the error body of errors.md: JSON, a code and a message.
// It returns the code.
func errorBody(t *testing.T, where string, header http.Header, body string) string {
	t.Helper()

	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("%s: error content type %q, want application/json; body %q", where, ct, body)
	}

	var eb struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(body), &eb); err != nil {
		t.Errorf("%s: error body is not JSON: %q", where, body)

		return ""
	}

	if eb.Error.Code == "" || eb.Error.Message == "" {
		t.Errorf("%s: error body without code or message: %q", where, body)
	}

	return eb.Error.Code
}

// sha256Ref is the blob reference of data.
func sha256Ref(data string) string {
	sum := sha256.Sum256([]byte(data))

	return "sha256:" + hex.EncodeToString(sum[:])
}

// Every system route answers its documented method with 200 and the body
// shape of system.md. Every answer is JSON, except /_metrics.
func TestContractSystemRoutesAnswer(t *testing.T) {
	_, h := newHandler(t)

	cases := []struct {
		method, path, body string
		contentType        string
		keys               []string
	}{
		{"GET", "/_health", "", "application/json", []string{"status"}},
		{"GET", "/_collections", "", "application/json", []string{"collections"}},
		{"GET", "/_stats", "", "application/json", []string{"version", "uptime_seconds", "collections", "totals", "blobs_bytes", "cursors_bytes", "shared_bytes"}},
		{"GET", "/_size", "", "application/json", []string{"data_dir", "total", "collections", "shared"}},
		{"GET", "/_pitr", "", "application/json", []string{"default", "collections"}},
		{"POST", "/_blobs", "abc", "application/json", []string{"$blob", "size"}},
		{"GET", "/_metrics", "", "text/plain", nil},
	}

	for _, c := range cases {
		rec := call(h, c.method, c.path, c.body)

		if rec.Code != http.StatusOK {
			t.Errorf("%s %s: status %d, want 200: %s", c.method, c.path, rec.Code, rec.Body.String())

			continue
		}

		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, c.contentType) {
			t.Errorf("%s %s: content type %q, want %s", c.method, c.path, ct, c.contentType)
		}

		if c.keys == nil {
			continue
		}

		var body map[string]any

		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s %s: not a JSON object: %s", c.method, c.path, rec.Body.String())

			continue
		}

		for _, k := range c.keys {
			if _, ok := body[k]; !ok {
				t.Errorf("%s %s: no key %q in %s", c.method, c.path, k, rec.Body.String())
			}
		}
	}
}

// With no collection, the lists of /_collections, /_stats, /_size and
// /_pitr are empty arrays, not null: a client can range over them.
func TestContractSystemListsAreEmptyArrays(t *testing.T) {
	_, h := newHandler(t)

	for _, path := range []string{"/_collections", "/_stats", "/_size", "/_pitr"} {
		rec := call(h, "GET", path, "")

		expectStatus(t, rec, http.StatusOK)

		if !strings.Contains(rec.Body.String(), `"collections":[]`) {
			t.Errorf("%s: want \"collections\":[], got %s", path, rec.Body.String())
		}
	}

	rec := call(h, "GET", "/_health", "")

	if strings.TrimSpace(rec.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("health body = %s", rec.Body.String())
	}
}

// GET /_collections is the catalog, one definition per collection, sorted
// by name.
func TestContractCollectionsSortedByName(t *testing.T) {
	_, h := newHandler(t)

	for _, name := range []string{"zeta", "alpha", "mid"} {
		expectStatus(t, call(h, "PUT", "/"+name, `{}`), http.StatusOK)
	}

	var body struct {
		Collections []map[string]any `json:"collections"`
	}

	decodeBody(t, call(h, "GET", "/_collections", "").Body.String(), &body)

	var names []string

	for _, def := range body.Collections {
		names = append(names, def["name"].(string))

		for _, k := range []string{"name", "write", "durability"} {
			if _, ok := def[k]; !ok {
				t.Errorf("definition %v has no %q", def, k)
			}
		}
	}

	if !slices.Equal(names, []string{"alpha", "mid", "zeta"}) {
		t.Fatalf("names = %v, want sorted", names)
	}
}

// GET /_stats reports the version the daemon was given and a growing
// uptime; totals.bytes includes shared_bytes.
func TestContractStatsVersionUptimeAndTotals(t *testing.T) {
	srv, h := newHandler(t)

	srv.SetVersion("9.8.7")

	expectStatus(t, call(h, "POST", "/posts/_bulk", `{"docs":[{"_id":"a"},{"_id":"b"}]}`), http.StatusOK)

	var body struct {
		Version     string  `json:"version"`
		Uptime      float64 `json:"uptime_seconds"`
		SharedBytes int64   `json:"shared_bytes"`
		Collections []struct {
			Name string `json:"name"`
			Rows int    `json:"rows"`
		} `json:"collections"`
		Totals struct {
			Collections int   `json:"collections"`
			Rows        int   `json:"rows"`
			Bytes       int64 `json:"bytes"`
		} `json:"totals"`
	}

	decodeBody(t, call(h, "GET", "/_stats", "").Body.String(), &body)

	if body.Version != "9.8.7" {
		t.Errorf("version = %q, want 9.8.7", body.Version)
	}

	if body.Uptime <= 0 {
		t.Errorf("uptime_seconds = %v, want > 0", body.Uptime)
	}

	if body.Totals.Collections != 1 || body.Totals.Rows != 2 || len(body.Collections) != 1 || body.Collections[0].Name != "posts" {
		t.Errorf("stats = %+v", body)
	}

	if body.Totals.Bytes < body.SharedBytes {
		t.Errorf("totals.bytes %d < shared_bytes %d", body.Totals.Bytes, body.SharedBytes)
	}
}

// GET /_pitr leaves the partitions out: the logical collection stands for
// them.
func TestContractPITRLeavesPartitionsOut(t *testing.T) {
	h := seedCalls(t)

	var body struct {
		Default     string `json:"default"`
		Collections []struct {
			Collection string `json:"collection"`
			Window     string `json:"window"`
		} `json:"collections"`
	}

	rec := call(h, "GET", "/_pitr", "")

	expectStatus(t, rec, http.StatusOK)
	decodeBody(t, rec.Body.String(), &body)

	if body.Default != "1d" {
		t.Errorf("default = %q, want 1d", body.Default)
	}

	var names []string

	for _, c := range body.Collections {
		names = append(names, c.Collection)
	}

	if !slices.Contains(names, "calls") {
		t.Errorf("collections %v miss the logical collection calls", names)
	}

	for _, n := range names {
		if strings.HasPrefix(n, "calls-") {
			t.Errorf("collections %v hold the partition %s", names, n)
		}
	}
}

// A path that no route has is 404 unknown_route, with the error body.
func TestContractSystemUnknownRoutes(t *testing.T) {
	_, h := newHandler(t)

	paths := []string{
		"/_health/x",
		"/_metrics/x",
		"/_stats/x",
		"/_collections/x",
		"/_size/x",
		"/_pitr/x",
		"/_backup/x",
		"/_restore/x",
		"/_blobs/a/b",
		"/_",
		"/__health",
		"/_HEALTH",
		"/_nope",
		"/v1/_health",
		"/_blobs//x",
		"/a/b/c/d/e/f/g",
	}

	for _, path := range paths {
		rec := call(h, "GET", path, "")

		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: status %d, want 404: %s", path, rec.Code, rec.Body.String())

			continue
		}

		if code := errorBody(t, path, rec.Header(), rec.Body.String()); code != "unknown_route" {
			t.Errorf("GET %s: code %q, want unknown_route", path, code)
		}
	}
}

// A known path with another method is 405 method_not_allowed, with an
// Allow header that names the methods of the path. The table is the
// "Every route" table of http.md.
func TestContractEveryRouteMethodNotAllowed(t *testing.T) {
	_, h := newHandler(t)

	routes := []struct {
		path    string
		methods []string
	}{
		{"/_health", []string{"GET"}},
		{"/_metrics", []string{"GET"}},
		{"/_collections", []string{"GET"}},
		{"/_stats", []string{"GET"}},
		{"/_size", []string{"GET"}},
		{"/_backup", []string{"POST"}},
		{"/_restore", []string{"POST"}},
		{"/_pitr", []string{"GET"}},
		{"/_log", []string{"GET", "PUT"}},
		{"/_blobs", []string{"POST"}},
		{"/_blobs/sha256:00", []string{"GET"}},
		{"/c", []string{"PUT", "GET", "DELETE"}},
		{"/c/_truncate", []string{"POST"}},
		{"/c/_rename", []string{"POST"}},
		{"/c/_restore", []string{"POST"}},
		{"/c/_pitr", []string{"GET"}},
		{"/c/_indexes/f", []string{"PUT", "DELETE"}},
		{"/c/_stats", []string{"GET"}},
		{"/c/_compact", []string{"POST"}},
		{"/c/_quarantine", []string{"GET"}},
		{"/c/_doc", []string{"POST"}},
		{"/c/_bulk", []string{"POST"}},
		{"/c/_doc/x", []string{"GET", "DELETE"}},
		{"/c/_update/x", []string{"POST"}},
		{"/c/_expire/x", []string{"POST"}},
		{"/c/_persist/x", []string{"POST"}},
		{"/c/_ttl/x", []string{"GET"}},
		{"/c/_search", []string{"POST"}},
		{"/c/_count", []string{"POST"}},
		{"/c/_aggregate", []string{"POST"}},
		{"/c/_update_by_query", []string{"POST"}},
		{"/c/_delete_by_query", []string{"POST"}},
		{"/c/_watch", []string{"POST"}},
		{"/c/_ack", []string{"POST"}},
		{"/s/_streams", []string{"GET"}},
		{"/s/_streams/append", []string{"POST"}},
		{"/s/_streams/trim", []string{"POST"}},
		{"/s/_streams/read", []string{"POST"}},
		{"/s/_streams/groups", []string{"GET"}},
		{"/s/_streams/groups/g", []string{"PUT", "DELETE"}},
		{"/s/_streams/groups/g/setid", []string{"POST"}},
		{"/s/_streams/groups/g/consumers", []string{"GET"}},
		{"/s/_streams/groups/g/consumers/c", []string{"PUT", "DELETE"}},
		{"/s/_streams/groups/g/read", []string{"POST"}},
		{"/s/_streams/groups/g/ack", []string{"POST"}},
		{"/s/_streams/groups/g/pending", []string{"POST"}},
		{"/s/_streams/groups/g/claim", []string{"POST"}},
		{"/s/_streams/groups/g/nack", []string{"POST"}},
	}

	all := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}

	for _, r := range routes {
		for _, m := range all {
			if slices.Contains(r.methods, m) {
				continue
			}

			rec := call(h, m, r.path, "")
			where := m + " " + r.path

			if rec.Code != http.StatusMethodNotAllowed {
				t.Errorf("%s: status %d, want 405: %s", where, rec.Code, rec.Body.String())

				continue
			}

			if code := errorBody(t, where, rec.Header(), rec.Body.String()); code != "method_not_allowed" {
				t.Errorf("%s: code %q", where, code)
			}

			allow := strings.Split(rec.Header().Get("Allow"), ", ")

			slices.Sort(allow)

			want := slices.Clone(r.methods)

			slices.Sort(want)

			if !slices.Equal(allow, want) {
				t.Errorf("%s: Allow %v, want %v", where, allow, want)
			}
		}
	}
}

// A trailing slash, a double slash and a query string do not change the
// route of a system path.
func TestContractSystemPathForms(t *testing.T) {
	_, h := newHandler(t)

	for _, path := range []string{"/_health/", "//_health", "/_health?x=1"} {
		expectStatus(t, call(h, "GET", path, ""), http.StatusOK)
	}
}

// Until recovery ends, every route except /_health and /_metrics is 503
// not_ready; /_health is 503 {"status":"recovering"}; /_metrics is open and
// says ndb_ready 0.
func TestContractNotReadyDuringRecovery(t *testing.T) {
	srv := server.New()
	h := srv.Handler()

	routes := []struct{ method, path, body string }{
		{"GET", "/_collections", ""},
		{"GET", "/_stats", ""},
		{"GET", "/_size", ""},
		{"GET", "/_pitr", ""},
		{"POST", "/_backup", "{}"},
		{"POST", "/_restore", "{}"},
		{"POST", "/_blobs", "abc"},
		{"GET", "/_blobs/" + sha256Ref("abc"), ""},
		{"PUT", "/c", "{}"},
		{"GET", "/c", ""},
		{"POST", "/c/_doc", `{"a":1}`},
		{"POST", "/c/_search", "{}"},
		{"GET", "/c/_doc/x", ""},
		{"POST", "/logs-*/_search", "{}"},
		{"GET", "/s/_streams", ""},
		{"POST", "/s/_streams/append", `{"doc":{"a":1}}`},
	}

	for _, r := range routes {
		rec := call(h, r.method, r.path, r.body)
		where := r.method + " " + r.path

		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d, want 503: %s", where, rec.Code, rec.Body.String())

			continue
		}

		if code := errorBody(t, where, rec.Header(), rec.Body.String()); code != "not_ready" {
			t.Errorf("%s: code %q, want not_ready", where, code)
		}
	}

	rec := call(h, "GET", "/_health", "")

	expectStatus(t, rec, http.StatusServiceUnavailable)

	if strings.TrimSpace(rec.Body.String()) != `{"status":"recovering"}` {
		t.Errorf("health while recovering = %s", rec.Body.String())
	}

	rec = call(h, "GET", "/_metrics", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), "ndb_ready 0\n") {
		t.Errorf("metrics while recovering miss ndb_ready 0")
	}

	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.Default())

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	srv.Attach(db)

	if !strings.Contains(call(h, "GET", "/_metrics", "").Body.String(), "ndb_ready 1\n") {
		t.Errorf("metrics after recovery miss ndb_ready 1")
	}

	expectStatus(t, call(h, "GET", "/_collections", ""), http.StatusOK)
}

// "On shutdown, new calls get 503 closed": once the engine is closed, a
// write must not be applied.
func TestContractWriteAfterCloseIsClosed(t *testing.T) {
	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.Default())

	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()
	srv.Attach(db)

	h := srv.Handler()

	expectStatus(t, call(h, "POST", "/x/_doc", `{"a":1}`), http.StatusOK)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rec := call(h, "POST", "/x/_doc", `{"a":2}`)

	expectStatus(t, rec, http.StatusServiceUnavailable)

	if code := errorCode(t, rec); code != "closed" && code != "stopping" {
		t.Fatalf("code %q, want closed or stopping", code)
	}
}

// Every error status has the error body of errors.md, with a JSON content
// type.
func TestContractErrorBodyOnEveryStatus(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/small", `{"max_line_size":256}`), http.StatusOK)

	cases := []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"POST", "/_restore", "not json", 400, "invalid_request"},
		{"POST", "/_restore", "", 400, "invalid_request"},
		{"POST", "/_restore", `{"collection":"small","at":"2026-01-01T00:00:00Z","to":"x","nope":1}`, 400, "invalid_request"},
		{"POST", "/_restore", `{"at":"2026-01-01T00:00:00Z","to":"x"}`, 400, "invalid_request"},
		{"POST", "/_restore", `{"collection":"small","at":"2026-01-01T00:00:00Z"}`, 400, "invalid_request"},
		{"POST", "/_restore", `{"collection":"small","at":"yesterday","to":"x"}`, 400, "invalid_request"},
		{"POST", "/_restore", `{"collection":"nope","at":"2026-01-01T00:00:00Z","to":"x"}`, 404, "unknown_collection"},
		{"POST", "/_restore", strings.Repeat(" ", engine.DefaultMaxLine+1), 413, "request_too_large"},
		{"POST", "/small/_doc", `{"a":"` + strings.Repeat("x", 300) + `"}`, 413, "request_too_large"},
		{"GET", "/_blobs/abc", "", 400, "invalid_blob_ref"},
		{"GET", "/_blobs/" + sha256Ref("missing"), "", 404, "not_found"},
		{"GET", "/_nope", "", 404, "unknown_route"},
		{"DELETE", "/_health", "", 405, "method_not_allowed"},
		{"POST", "/_backup", "{}", 403, "forbidden"},
		{"POST", "/logs-*/_doc", `{"a":1}`, 400, "pattern_read_only"},
	}

	for _, c := range cases {
		rec := call(h, c.method, c.path, c.body)
		where := c.method + " " + c.path

		if rec.Code != c.status {
			t.Errorf("%s %.40q: status %d, want %d: %s", where, c.body, rec.Code, c.status, rec.Body.String())

			continue
		}

		if code := errorBody(t, where, rec.Header(), rec.Body.String()); code != c.code {
			t.Errorf("%s %.40q: code %q, want %q", where, c.body, code, c.code)
		}
	}
}

// POST /_blobs stores bytes by content: the same bytes give the same
// reference, the size and the Content-Type are in the answer, and the
// blob limit is inclusive.
func TestContractBlobPut(t *testing.T) {
	c := unixServerWith(t, func(s *server.Server) { s.SetBlobLimit(8) })

	resp, first := do(t, c, "POST", "/_blobs", "12345678", map[string]string{"Content-Type": "audio/wav"})

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("put at the limit: %d %s", resp.StatusCode, first)
	}

	var ref struct {
		Blob string `json:"$blob"`
		Size int    `json:"size"`
		Type string `json:"type"`
	}

	decodeBody(t, first, &ref)

	if ref.Blob != sha256Ref("12345678") || ref.Size != 8 || ref.Type != "audio/wav" {
		t.Fatalf("ref = %+v", ref)
	}

	_, second := do(t, c, "POST", "/_blobs", "12345678", map[string]string{"Content-Type": "audio/wav"})

	if second != first {
		t.Fatalf("same bytes, other answer: %s vs %s", second, first)
	}

	resp, body := do(t, c, "POST", "/_blobs", "123456789", nil)

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("one byte over the limit: %d %s", resp.StatusCode, body)
	}

	if code := errorBody(t, "blob over limit", resp.Header, body); code != "request_too_large" {
		t.Fatalf("code %q", code)
	}

	_, page := do(t, c, "GET", "/_metrics", "", nil)

	if !strings.Contains(page, "ndb_blob_store_blobs 1\n") {
		t.Fatalf("the same bytes are stored twice:\n%s", page)
	}
}

// GET /_blobs/{ref}: the bytes, a part with Range (206), the type of
// ?type=, and invalid_blob_ref for any reference that is not
// sha256:<64 hex>.
func TestContractBlobGet(t *testing.T) {
	c := unixServer(t)

	_, body := do(t, c, "POST", "/_blobs", "0123456789", nil)

	ref := between(body, `"$blob":"`, `"`)

	resp, got := do(t, c, "GET", "/_blobs/"+ref, "", nil)

	if resp.StatusCode != http.StatusOK || got != "0123456789" || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("get: %d %q %v", resp.StatusCode, got, resp.Header)
	}

	resp, got = do(t, c, "GET", "/_blobs/"+ref+"?type=text/plain", "", map[string]string{"Range": "bytes=0-3"})

	if resp.StatusCode != http.StatusPartialContent || got != "0123" || resp.Header.Get("Content-Range") != "bytes 0-3/10" || resp.Header.Get("Content-Type") != "text/plain" {
		t.Fatalf("range: %d %q %v", resp.StatusCode, got, resp.Header)
	}

	bad := []string{
		"abc",
		"sha256:",
		"sha256:" + strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 65),
		"sha256:" + strings.Repeat("g", 64),
		"md5:" + strings.Repeat("a", 64),
	}

	for _, r := range bad {
		resp, body := do(t, c, "GET", "/_blobs/"+r, "", nil)

		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", r, resp.StatusCode, body)

			continue
		}

		if code := errorBody(t, r, resp.Header, body); code != "invalid_blob_ref" {
			t.Errorf("%s: code %q", r, code)
		}
	}

	resp, body = do(t, c, "GET", "/_blobs/"+sha256Ref("missing"), "", nil)

	if code := errorBody(t, "missing blob", resp.Header, body); resp.StatusCode != http.StatusNotFound || code != "not_found" {
		t.Fatalf("missing: %d %s", resp.StatusCode, body)
	}
}

// A Range out of the blob is an error, and "every error has the same
// body" (errors.md).
func TestContractBlobBadRangeHasErrorBody(t *testing.T) {
	c := unixServer(t)

	_, body := do(t, c, "POST", "/_blobs", "0123456789", nil)

	ref := between(body, `"$blob":"`, `"`)

	resp, body := do(t, c, "GET", "/_blobs/"+ref, "", map[string]string{"Range": "bytes=50-60"})

	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status %d, want 416", resp.StatusCode)
	}

	errorBody(t, "bad range", resp.Header, body)
}

// POST /_backup on the unix socket: an NDJSON stream; no_blobs leaves the
// blob contents out; an unknown key is invalid_request.
func TestContractBackupOnUnixSocket(t *testing.T) {
	c := unixServer(t)

	_, body := do(t, c, "POST", "/_blobs", "blob-bytes", nil)

	ref := between(body, `"$blob":"`, `"`)

	do(t, c, "POST", "/b/_doc", `{"_id":"1","file":{"$blob":"`+ref+`"}}`, nil)

	resp, full := do(t, c, "POST", "/_backup", "", nil)

	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("backup with an empty body: %d %s", resp.StatusCode, full)
	}

	if !strings.Contains(full, `"_section":"blob"`) {
		t.Fatalf("backup misses the blob:\n%s", full)
	}

	_, lean := do(t, c, "POST", "/_backup", `{"no_blobs":true}`, nil)

	if strings.Contains(lean, `"_section":"blob"`) || !strings.Contains(lean, `"_id":"1"`) {
		t.Fatalf("no_blobs backup:\n%s", lean)
	}

	for _, bad := range []string{`{"nope":1}`, `not json`, `{"no_blobs":"yes"}`} {
		resp, body := do(t, c, "POST", "/_backup", bad, nil)

		if code := errorBody(t, bad, resp.Header, body); resp.StatusCode != http.StatusBadRequest || code != "invalid_request" {
			t.Errorf("%s: %d %s", bad, resp.StatusCode, body)
		}
	}
}

// With -token-file, every TCP request needs the token, /_health and
// /_metrics included, and a known route or not: 401 unauthorized with the
// error body. The lockout is off here: the test makes 22 failures from one
// IP (TestLockout covers it).
func TestContractTokenOnEverySystemRoute(t *testing.T) {
	_, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetToken("s3cret")
		srv.SetSecurity(server.Security{})
	})

	routes := []struct{ method, path, body string }{
		{"GET", "/_health", ""},
		{"GET", "/_metrics", ""},
		{"GET", "/_collections", ""},
		{"GET", "/_stats", ""},
		{"GET", "/_size", ""},
		{"GET", "/_pitr", ""},
		{"POST", "/_backup", "{}"},
		{"POST", "/_restore", "{}"},
		{"POST", "/_blobs", "abc"},
		{"GET", "/_blobs/" + sha256Ref("abc"), ""},
		{"GET", "/_nope", ""},
	}

	for _, r := range routes {
		for _, token := range []string{"", "wrong"} {
			resp, body := tcpDo(t, tcp, addr, r.method, r.path, r.body, token)
			where := r.method + " " + r.path + " token=" + token

			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s: status %d, want 401", where, resp.StatusCode)

				continue
			}

			if code := errorBody(t, where, resp.Header, body); code != "unauthorized" {
				t.Errorf("%s: code %q", where, code)
			}
		}
	}

	resp, body := tcpDo(t, tcp, addr, "GET", "/_health", "", "s3cret")

	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Fatalf("health with the token: %d %s", resp.StatusCode, body)
	}

	resp, _ = tcpDo(t, tcp, addr, "GET", "/_metrics", "", "s3cret")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics with the token: %d", resp.StatusCode)
	}
}

// The scheme of an Authorization header is case-insensitive (RFC 9110
// 11.1), so "bearer <token>" is the same as "Bearer <token>".
func TestContractBearerSchemeIsCaseInsensitive(t *testing.T) {
	_, tcp, addr := tokenServer(t, "s3cret")

	for _, scheme := range []string{"bearer", "BEARER"} {
		req, _ := http.NewRequest("GET", "http://"+addr+"/_health", nil)
		req.Header.Set("Authorization", scheme+" s3cret")

		resp, err := tcp.Do(req)

		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", scheme, resp.StatusCode)
		}
	}
}

// The unix socket needs no token, even with scoped tokens; a wrong token
// on it is ignored.
func TestContractUnixSocketIgnoresTokens(t *testing.T) {
	unix, _, _ := authServer(t, func(srv *server.Server) {
		srv.SetToken("s3cret")
		srv.SetTokens([]server.Token{{Name: "dash", Secret: "dash-secret", Read: []string{"calls*"}}})
	})

	routes := []struct{ method, path, body string }{
		{"GET", "/_health", ""},
		{"GET", "/_collections", ""},
		{"POST", "/_blobs", "abc"},
		{"PUT", "/other", "{}"},
		{"POST", "/_backup", "{}"},
	}

	for _, r := range routes {
		for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer wrong"}} {
			resp, body := do(t, unix, r.method, r.path, r.body, hdr)

			if resp.StatusCode != http.StatusOK {
				t.Errorf("unix %s %s %v: %d %s", r.method, r.path, hdr, resp.StatusCode, body)
			}
		}
	}
}

// The rights of the system routes (configuration.md, Scoped tokens): the
// system reads with any read right, POST /_blobs with any write right,
// POST /_restore with admin on both collections, POST /_backup never over
// TCP. The token of -token-file keeps every right. A 403 names the token.
func TestContractScopedTokensOnSystemRoutes(t *testing.T) {
	unix, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetToken("full-secret")
		srv.SetTokens([]server.Token{
			{Name: "dash", Secret: "dash-secret", Read: []string{"calls*"}},
			{Name: "esl", Secret: "esl-secret", Write: []string{"outbox"}},
			{Name: "ops", Secret: "ops-secret", Admin: []string{"*"}},
			{Name: "callsadm", Secret: "callsadm-secret", Admin: []string{"calls*"}},
		})
	})

	_, body := do(t, unix, "POST", "/_blobs", "abc", nil)

	ref := between(body, `"$blob":"`, `"`)

	do(t, unix, "POST", "/calls/_doc", `{"_id":"1"}`, nil)

	time.Sleep(3 * time.Millisecond)

	at := time.Now().UTC().Format(time.RFC3339Nano)

	time.Sleep(3 * time.Millisecond)

	reads := []string{"/_health", "/_metrics", "/_collections", "/_stats", "/_size", "/_pitr", "/_blobs/" + ref}

	type step struct {
		token, method, path, body string
		status                    int
	}

	var cases []step

	for _, tok := range []string{"dash", "esl", "ops", "callsadm", "full"} {
		for _, p := range reads {
			cases = append(cases, step{tok, "GET", p, "", 200})
		}

		cases = append(cases, step{tok, "POST", "/_backup", "{}", 403})
	}

	cases = append(cases,
		step{"dash", "POST", "/_blobs", "x", 403},
		step{"esl", "POST", "/_blobs", "x", 200},
		step{"ops", "POST", "/_blobs", "x", 200},
		step{"full", "POST", "/_blobs", "x", 200},
		step{"dash", "POST", "/_restore", `{"collection":"calls","at":"` + at + `","to":"calls_a"}`, 403},
		step{"esl", "POST", "/_restore", `{"collection":"outbox","at":"` + at + `","to":"outbox_a"}`, 403},
		step{"callsadm", "POST", "/_restore", `{"collection":"calls","at":"` + at + `","to":"other"}`, 403},
		step{"callsadm", "POST", "/_restore", `{"collection":"calls","at":"` + at + `","to":"calls_b"}`, 200},
		step{"ops", "POST", "/_restore", `{"collection":"calls","at":"` + at + `","to":"calls_c"}`, 200},
		step{"full", "POST", "/_restore", `{"collection":"calls","at":"` + at + `","to":"calls_d"}`, 200},
	)

	for _, c := range cases {
		resp, body := tcpDo(t, tcp, addr, c.method, c.path, c.body, c.token+"-secret")
		where := c.token + " " + c.method + " " + c.path

		if resp.StatusCode != c.status {
			t.Errorf("%s: status %d, want %d: %s", where, resp.StatusCode, c.status, body)

			continue
		}

		if c.status != http.StatusForbidden {
			continue
		}

		if code := errorBody(t, where, resp.Header, body); code != "forbidden" {
			t.Errorf("%s: code %q", where, code)
		}

		if c.path != "/_backup" && !strings.Contains(body, c.token) {
			t.Errorf("%s: the 403 message does not name the token: %s", where, body)
		}
	}
}

// Connections are kept alive: a second request on the same client uses
// the same connection, after a success and after an error answer.
func TestContractKeepAlive(t *testing.T) {
	_, tcp, addr := tokenServer(t, "s3cret")

	steps := []struct {
		path, token string
		status      int
	}{
		{"/_health", "s3cret", 200},
		{"/_health", "s3cret", 200},
		{"/_nope", "s3cret", 404},
		{"/_health", "", 401},
		{"/_collections", "s3cret", 200},
	}

	for i, s := range steps {
		reused := false

		trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused }}

		req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(context.Background(), trace), "GET", "http://"+addr+s.path, nil)

		if s.token != "" {
			req.Header.Set("Authorization", "Bearer "+s.token)
		}

		resp, err := tcp.Do(req)

		if err != nil {
			t.Fatal(err)
		}

		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode != s.status {
			t.Fatalf("%s: status %d, want %d", s.path, resp.StatusCode, s.status)
		}

		if resp.ProtoMajor != 1 || resp.ProtoMinor != 1 {
			t.Fatalf("%s: proto %s, want HTTP/1.1", s.path, resp.Proto)
		}

		if i > 0 && !reused {
			t.Errorf("step %d %s: a new connection, want the kept-alive one", i, s.path)
		}
	}
}

// On a unix socket, the host name of the URL does not matter.
func TestContractUnixSocketAnyHost(t *testing.T) {
	c := unixServer(t)

	for _, host := range []string{"ndb", "localhost", "anything.example:9999"} {
		resp, err := c.Get("http://" + host + "/_health")

		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("host %s: status %d", host, resp.StatusCode)
		}
	}
}

// No CORS: the daemon sends no Access-Control headers, and a preflight
// OPTIONS is not a route.
func TestContractNoCORS(t *testing.T) {
	_, h := newHandler(t)

	for _, m := range []string{"GET", "OPTIONS"} {
		req := httptest.NewRequest(m, "/_health", nil)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Access-Control-Request-Method", "GET")

		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		for k := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: CORS header %s", m, k)
			}
		}
	}

	expectStatus(t, call(h, "OPTIONS", "/_health", ""), http.StatusMethodNotAllowed)
}
