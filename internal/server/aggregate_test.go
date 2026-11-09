package server_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

func seedPosts(t *testing.T, h http.Handler, n int) {
	t.Helper()

	call(h, "PUT", "/posts", `{}`)

	docs := make([]string, n)

	for i := range docs {
		docs[i] = fmt.Sprintf(`{"_id":"p%03d","k":%d,"n":%d}`, i, i%5, i)
	}

	expectStatus(t, call(h, "POST", "/posts/_bulk", `{"docs":[`+strings.Join(docs, ",")+`]}`), http.StatusOK)
}

func TestAggregateStreamsNDJSONWithMetadataHeaders(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 20)

	rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":"$k","n":{"$count":{}}}}],"limit":2,"count":true}`)

	expectStatus(t, rec, http.StatusOK)

	want := `{"_id":0,"n":4}` + "\n" + `{"_id":1,"n":4}` + "\n"

	if rec.Body.String() != want {
		t.Fatalf("body = %q, want %q", rec.Body.String(), want)
	}

	if rec.Header().Get("X-Ndb-Next") == "" {
		t.Fatal("X-Ndb-Next missing on a page that has more rows")
	}

	if got := rec.Header().Get("X-Ndb-Total"); got != "5" {
		t.Fatalf("X-Ndb-Total = %q, want 5", got)
	}

	if got := rec.Header().Get("X-Ndb-Plan"); got != "scan" {
		t.Fatalf("X-Ndb-Plan = %q, want scan", got)
	}

	if got := rec.Header().Get("X-Ndb-Examined"); got != "20" {
		t.Fatalf("X-Ndb-Examined = %q, want 20", got)
	}

	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content type = %q, want application/x-ndjson", ct)
	}
}

func TestAggregateLastPageHasNoNext(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 20)

	rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":"$k"}}],"limit":5}`)

	expectStatus(t, rec, http.StatusOK)

	if got := rec.Header().Get("X-Ndb-Next"); got != "" {
		t.Fatalf("X-Ndb-Next = %q on the last page", got)
	}

	if got := rec.Header().Get("X-Ndb-Total"); got != "" {
		t.Fatalf("X-Ndb-Total = %q without count", got)
	}
}

func TestAggregateExplainHeader(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 5)

	rec := call(h, "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":null}}],"explain":true}`)

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Header().Get("X-Ndb-Explain"), `"rows_matched":5`) {
		t.Fatalf("X-Ndb-Explain = %q", rec.Header().Get("X-Ndb-Explain"))
	}
}

func TestAggregateErrors(t *testing.T) {
	_, h := newHandler(t)

	seedPosts(t, h, 3)

	cases := map[string]struct {
		path, body, code string
		status           int
	}{
		"invalid pipeline": {"/posts/_aggregate", `{"pipeline":[{"$limit":1},{"$sort":{"n":1}}]}`, "invalid_pipeline", http.StatusBadRequest},
		"cursor":           {"/posts/_aggregate", `{"pipeline":[{"$group":{"_id":null}}],"after":"!!!"}`, "cursor_invalid", http.StatusBadRequest},
		"unknown coll":     {"/nope/_aggregate", `{"pipeline":[{"$group":{"_id":null}}]}`, "unknown_collection", http.StatusNotFound},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := call(h, "POST", c.path, c.body)

			expectStatus(t, rec, c.status)

			if got := errorCode(t, rec); got != c.code {
				t.Fatalf("code = %q, want %q", got, c.code)
			}
		})
	}
}

func TestAggregateGroupLimitIs422(t *testing.T) {
	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true, GroupLimit: 2048}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	srv := server.New()
	srv.Attach(db)

	seedPosts(t, srv.Handler(), 100)

	rec := call(srv.Handler(), "POST", "/posts/_aggregate", `{"pipeline":[{"$group":{"_id":"$_id"}}]}`)

	expectStatus(t, rec, http.StatusUnprocessableEntity)

	if got := errorCode(t, rec); got != "group_limit_exceeded" {
		t.Fatalf("code = %q, want group_limit_exceeded", got)
	}
}
