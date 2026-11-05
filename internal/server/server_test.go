package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

// newHandler starts a server with an attached engine on a temp data dir.
func newHandler(t *testing.T) (*server.Server, http.Handler) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	srv := server.New()
	srv.Attach(db)

	return srv, srv.Handler()
}

func call(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	return callCtx(context.Background(), h, method, path, body)
}

func callCtx(ctx context.Context, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	return rec
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %q", rec.Body.String())
	}

	if body.Error.Message == "" {
		t.Fatalf("error without message: %q", rec.Body.String())
	}

	return body.Error.Code
}

func expectStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()

	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, want, rec.Body.String())
	}
}

func TestHealthWaitsForRecovery(t *testing.T) {
	srv := server.New()

	expectStatus(t, call(srv.Handler(), "GET", "/health", ""), http.StatusServiceUnavailable)

	db, err := engine.Open(config.Config{DataDir: t.TempDir()}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	srv.Attach(db)

	expectStatus(t, call(srv.Handler(), "GET", "/health", ""), http.StatusOK)
}

func TestErrorBodyHasCodeAndMessage(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "POST", "/v1/c/nope/find", `{}`)

	expectStatus(t, rec, http.StatusNotFound)

	if got := errorCode(t, rec); got != "unknown_collection" {
		t.Fatalf("code = %q, want unknown_collection", got)
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content type = %q, want JSON", ct)
	}
}

func TestVersionConflictIs409(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/v1/c/posts", `{"id":"post_id"}`)
	call(h, "POST", "/v1/c/posts/insert", `{"insert":[{"post_id":"p1","n":1}]}`)

	rec := call(h, "POST", "/v1/c/posts/update", `{"update":{"id":"p1","set":{"n":2},"if_version":9}}`)

	expectStatus(t, rec, http.StatusConflict)

	if got := errorCode(t, rec); got != "version_conflict" {
		t.Fatalf("code = %q, want version_conflict", got)
	}
}

func TestBodyOverMaxLineSizeIs413(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/v1/c/posts", `{"id":"post_id","max_line_size":256}`)

	big := `{"insert":[{"post_id":"p1","text":"` + strings.Repeat("x", 1024) + `"}]}`
	rec := call(h, "POST", "/v1/c/posts/insert", big)

	expectStatus(t, rec, http.StatusRequestEntityTooLarge)

	if got := errorCode(t, rec); got != "request_too_large" {
		t.Fatalf("code = %q, want request_too_large", got)
	}
}

func TestClientCancelStopsTheQuery(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/v1/c/posts", `{"id":"post_id"}`)
	call(h, "POST", "/v1/c/posts/insert", `{"insert":[{"post_id":"p1"}]}`)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rec := callCtx(ctx, h, "POST", "/v1/c/posts/find", `{}`)

	if rec.Code != server.StatusClientClosed {
		t.Fatalf("status = %d, want %d (client closed)", rec.Code, server.StatusClientClosed)
	}

	if got := errorCode(t, rec); got != "canceled" {
		t.Fatalf("code = %q, want canceled", got)
	}
}

func TestFindStreamsNDJSONWithMetadataHeaders(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/v1/c/posts", `{"id":"post_id"}`)
	call(h, "POST", "/v1/c/posts/insert", `{"insert":[{"post_id":"p1","n":1},{"post_id":"p2","n":2},{"post_id":"p3","n":3}]}`)

	rec := call(h, "POST", "/v1/c/posts/find", `{"sort":{"n":1},"limit":2,"count":true}`)

	expectStatus(t, rec, http.StatusOK)

	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")

	if len(lines) != 2 {
		t.Fatalf("rows = %d, want 2: %q", len(lines), rec.Body.String())
	}

	if rec.Header().Get("X-Ndb-Next") == "" {
		t.Fatal("X-Ndb-Next missing on a page that has more rows")
	}

	if got := rec.Header().Get("X-Ndb-Total"); got != "3" {
		t.Fatalf("X-Ndb-Total = %q, want 3", got)
	}

	if got := rec.Header().Get("X-Ndb-Plan"); got != "scan" {
		t.Fatalf("X-Ndb-Plan = %q, want scan", got)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content type = %q, want application/x-ndjson", ct)
	}
}

func TestInsertUpdateDeleteRoundTrip(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/v1/c/posts", `{"id":"post_id"}`)

	rec := call(h, "POST", "/v1/c/posts/insert", `{"insert":[{"post_id":"p1","status":"draft"}]}`)

	expectStatus(t, rec, http.StatusOK)

	rec = call(h, "POST", "/v1/c/posts/update", `{"update":{"id":"p1","set":{"status":"live"},"if_version":1}}`)

	expectStatus(t, rec, http.StatusOK)

	rec = call(h, "POST", "/v1/c/posts/find", `{"where":{"status":"live"}}`)

	if !strings.Contains(rec.Body.String(), `"p1"`) {
		t.Fatalf("updated row not found: %s", rec.Body.String())
	}

	rec = call(h, "POST", "/v1/c/posts/delete", `{"delete":{"id":"p1"}}`)

	expectStatus(t, rec, http.StatusOK)

	rec = call(h, "POST", "/v1/c/posts/find", `{}`)

	if strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("deleted row still returned: %s", rec.Body.String())
	}
}

func TestEnsureReturnsDefinitionAndIndexStates(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "PUT", "/v1/c/calls", `{"id":"call_id","indexes":["status"]}`)

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Definition struct {
			Name string `json:"name"`
		} `json:"definition"`
		Indexes []struct {
			Field string `json:"field"`
			State string `json:"state"`
		} `json:"indexes"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}

	if body.Definition.Name != "calls" || len(body.Indexes) != 1 || body.Indexes[0].Field != "status" {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
}

func TestEnsureConflictIs409(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/v1/c/calls", `{"id":"call_id"}`)

	rec := call(h, "PUT", "/v1/c/calls", `{"id":"other_id"}`)

	expectStatus(t, rec, http.StatusConflict)

	if got := errorCode(t, rec); got != "catalog_conflict" {
		t.Fatalf("code = %q, want catalog_conflict", got)
	}
}

func TestListStatsAndDropIndex(t *testing.T) {
	_, h := newHandler(t)

	call(h, "PUT", "/v1/c/calls", `{"id":"call_id","indexes":["status","dnis"]}`)

	rec := call(h, "GET", "/v1/c", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"calls"`) {
		t.Fatalf("catalog list misses calls: %s", rec.Body.String())
	}

	rec = call(h, "GET", "/v1/c/calls/stats", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"rows":0`) {
		t.Fatalf("stats: %s", rec.Body.String())
	}

	rec = call(h, "DELETE", "/v1/c/calls/indexes/dnis", "")

	expectStatus(t, rec, http.StatusOK)

	rec = call(h, "GET", "/v1/c/calls/stats", "")

	if strings.Contains(rec.Body.String(), `"field":"dnis"`) {
		t.Fatalf("dropped index still in stats: %s", rec.Body.String())
	}
}

func TestInsertIntoUnknownCollectionCreatesIt(t *testing.T) {
	_, h := newHandler(t)

	rec := call(h, "POST", "/v1/c/logs/insert", `{"insert":[{"_id":"a","msg":"hi"}]}`)

	expectStatus(t, rec, http.StatusOK)

	rec = call(h, "GET", "/v1/c", "")

	if !strings.Contains(rec.Body.String(), `"logs"`) {
		t.Fatalf("implicit collection not listed: %s", rec.Body.String())
	}
}

func TestMetricsExposesBasicCounters(t *testing.T) {
	_, h := newHandler(t)

	call(h, "POST", "/v1/c/nope/find", `{}`)

	rec := call(h, "GET", "/metrics", "")

	expectStatus(t, rec, http.StatusOK)

	for _, name := range []string{"ndb_requests_total", "ndb_request_errors_total", "ndb_collections"} {
		if !strings.Contains(rec.Body.String(), name) {
			t.Fatalf("metrics misses %s:\n%s", name, rec.Body.String())
		}
	}
}
