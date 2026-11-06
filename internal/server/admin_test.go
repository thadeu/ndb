package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

// adminHandler serves one engine collection ("posts", 3 rows, 3 segments
// after three seals) and one direct collection ("calls", 1 bad line).
func adminHandler(t *testing.T) http.Handler {
	t.Helper()

	root := t.TempDir()
	files := filepath.Join(root, "calls")

	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := `{"_id":"c1","status":"answered"}` + "\n" + "{broken\n" + `{"_id":"c2","status":"busy"}` + "\n"

	if err := os.WriteFile(filepath.Join(files, "a.ndjson"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := catalog.Def{Name: "calls", Write: catalog.Direct, Path: filepath.Join(files, "*.ndjson"), Indexes: []string{"status"}}

	db, err := engine.Open(config.Config{
		DataDir:       filepath.Join(root, "data"),
		AllowImplicit: true,
		Collections:   map[string]catalog.Def{"calls": calls},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	if _, err := db.Ensure(catalog.Def{Name: "posts", Indexes: []string{"author"}}); err != nil {
		t.Fatal(err)
	}

	posts, err := db.Get("posts")
	if err != nil {
		t.Fatal(err)
	}

	for _, doc := range []string{`{"_id":"p1","author":"a"}`, `{"_id":"p2","author":"b"}`, `{"_id":"p3","author":"c"}`} {
		if _, err := posts.Insert(context.Background(), []byte(doc)); err != nil {
			t.Fatal(err)
		}

		if err := posts.Seal(); err != nil {
			t.Fatal(err)
		}
	}

	srv := server.New()

	srv.SetVersion("9.9.9")
	srv.Attach(db)

	return srv.Handler()
}

func decodeBody(t *testing.T, body string, v any) {
	t.Helper()

	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("not JSON: %v: %s", err, body)
	}
}

type collStats struct {
	Name        string `json:"name"`
	Write       string `json:"write"`
	Rows        int    `json:"rows"`
	Quarantined int64  `json:"quarantined"`
	Segments    int    `json:"segments"`
	WALFiles    int    `json:"wal_files"`
	Indexes     []struct {
		Field string `json:"field"`
		State string `json:"state"`
	} `json:"indexes"`
	Bytes struct {
		WAL      int64 `json:"wal"`
		Segments int64 `json:"segments"`
		Meta     int64 `json:"meta"`
	} `json:"bytes"`
	Total int64 `json:"total"`
}

func TestGlobalStatsListsEveryCollectionWithTotals(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "GET", "/v1/stats", "")

	expectStatus(t, rec, http.StatusOK)

	var st struct {
		Version       string      `json:"version"`
		UptimeSeconds float64     `json:"uptime_seconds"`
		Collections   []collStats `json:"collections"`
		Totals        struct {
			Collections int   `json:"collections"`
			Rows        int   `json:"rows"`
			Bytes       int64 `json:"bytes"`
			Quarantined int64 `json:"quarantined"`
		} `json:"totals"`
		BlobsBytes   *int64 `json:"blobs_bytes"`
		CursorsBytes *int64 `json:"cursors_bytes"`
	}

	decodeBody(t, rec.Body.String(), &st)

	if st.Version != "9.9.9" || st.UptimeSeconds < 0 || st.BlobsBytes == nil || st.CursorsBytes == nil {
		t.Fatalf("header fields: %s", rec.Body.String())
	}

	if len(st.Collections) != 2 || st.Collections[0].Name != "calls" || st.Collections[1].Name != "posts" {
		t.Fatalf("collections: %s", rec.Body.String())
	}

	calls, posts := st.Collections[0], st.Collections[1]

	if calls.Write != "direct" || calls.Rows != 2 || calls.Quarantined != 1 || len(calls.Indexes) != 1 || calls.Indexes[0].Field != "status" {
		t.Fatalf("calls = %+v", calls)
	}

	if posts.Write != "engine" || posts.Rows != 3 || posts.Segments != 3 || posts.Bytes.Segments <= 0 || posts.Bytes.Meta <= 0 {
		t.Fatalf("posts = %+v", posts)
	}

	if posts.Total != posts.Bytes.WAL+posts.Bytes.Segments+posts.Bytes.Meta {
		t.Fatalf("posts total %d != sum of parts %+v", posts.Total, posts.Bytes)
	}

	if st.Totals.Collections != 2 || st.Totals.Rows != 5 || st.Totals.Quarantined != 1 || st.Totals.Bytes <= posts.Total {
		t.Fatalf("totals = %+v", st.Totals)
	}
}

func TestCollectionStatsKeepsOldFieldsAndAddsTheNewOnes(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "GET", "/v1/c/posts/stats", "")

	expectStatus(t, rec, http.StatusOK)

	var st collStats

	decodeBody(t, rec.Body.String(), &st)

	if st.Name != "posts" || st.Write != "engine" || st.Rows != 3 || len(st.Indexes) != 1 || st.Indexes[0].State == "" {
		t.Fatalf("old fields: %s", rec.Body.String())
	}

	if st.Segments != 3 || st.WALFiles < 1 || st.Bytes.Segments <= 0 || st.Total <= 0 {
		t.Fatalf("new fields: %s", rec.Body.String())
	}

	expectStatus(t, call(h, "GET", "/v1/c/nope/stats", ""), http.StatusNotFound)
}

func TestSizeReportsPartsPerCollectionAndTotals(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "GET", "/v1/size", "")

	expectStatus(t, rec, http.StatusOK)

	var u struct {
		DataDir     string `json:"data_dir"`
		Total       int64  `json:"total"`
		Collections []struct {
			Name         string `json:"name"`
			Total        int64  `json:"total"`
			SegmentFiles int    `json:"segment_files"`
			Bytes        struct {
				Segments int64 `json:"segments"`
			} `json:"bytes"`
		} `json:"collections"`
		Shared struct {
			Meta int64 `json:"meta"`
		} `json:"shared"`
	}

	decodeBody(t, rec.Body.String(), &u)

	if u.DataDir == "" || u.Total <= 0 || len(u.Collections) != 2 || u.Shared.Meta <= 0 {
		t.Fatalf("size = %s", rec.Body.String())
	}

	if u.Collections[1].Name != "posts" || u.Collections[1].SegmentFiles != 3 || u.Collections[1].Bytes.Segments <= 0 {
		t.Fatalf("posts = %s", rec.Body.String())
	}
}

func TestGetCollectionReturnsTheDefinition(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "GET", "/v1/c/calls", "")

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Definition catalog.Def `json:"definition"`
		Indexes    []struct {
			Field string `json:"field"`
			State string `json:"state"`
		} `json:"indexes"`
	}

	decodeBody(t, rec.Body.String(), &body)

	if body.Definition.Name != "calls" || body.Definition.Write != catalog.Direct || !strings.HasSuffix(body.Definition.Path, "*.ndjson") {
		t.Fatalf("definition = %s", rec.Body.String())
	}

	if len(body.Indexes) != 1 || body.Indexes[0].Field != "status" {
		t.Fatalf("indexes = %s", rec.Body.String())
	}

	rec = call(h, "GET", "/v1/c/nope", "")

	expectStatus(t, rec, http.StatusNotFound)

	if code := errorCode(t, rec); code != "unknown_collection" {
		t.Fatalf("code = %q", code)
	}
}

func TestCompactEndpointMergesSegments(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "POST", "/v1/c/posts/compact", "")

	expectStatus(t, rec, http.StatusOK)

	var res struct {
		Collection string `json:"collection"`
		Before     struct {
			Segments int   `json:"segments"`
			Bytes    int64 `json:"bytes"`
		} `json:"before"`
		After struct {
			Segments int   `json:"segments"`
			Bytes    int64 `json:"bytes"`
		} `json:"after"`
	}

	decodeBody(t, rec.Body.String(), &res)

	if res.Collection != "posts" || res.Before.Segments != 3 || res.After.Segments != 1 || res.After.Bytes <= 0 {
		t.Fatalf("compact = %s", rec.Body.String())
	}
}

func TestCompactEndpointRefusesDirectAndUnknownCollections(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "POST", "/v1/c/calls/compact", "")

	expectStatus(t, rec, http.StatusBadRequest)

	if code := errorCode(t, rec); code != "not_supported" {
		t.Fatalf("code = %q", code)
	}

	expectStatus(t, call(h, "POST", "/v1/c/nope/compact", ""), http.StatusNotFound)
}

func TestQuarantineEndpointListsBadLinesOfADirectCollection(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "GET", "/v1/c/calls/quarantine?limit=5", "")

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Collection string `json:"collection"`
		Total      int    `json:"total"`
		Rows       []struct {
			File      string `json:"file"`
			Offset    int64  `json:"offset"`
			Error     string `json:"error"`
			Line      string `json:"line"`
			Truncated bool   `json:"truncated"`
		} `json:"rows"`
	}

	decodeBody(t, rec.Body.String(), &body)

	if body.Collection != "calls" || body.Total != 1 || len(body.Rows) != 1 {
		t.Fatalf("quarantine = %s", rec.Body.String())
	}

	row := body.Rows[0]

	if row.Line != "{broken" || row.Error == "" || row.Offset == 0 || !strings.HasSuffix(row.File, "a.ndjson") || row.Truncated {
		t.Fatalf("row = %+v", row)
	}
}

func TestQuarantineEndpointEngineModeIsEmptyAndLimitIsChecked(t *testing.T) {
	h := adminHandler(t)

	rec := call(h, "GET", "/v1/c/posts/quarantine", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"rows":[]`) || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("engine quarantine = %s", rec.Body.String())
	}

	for _, bad := range []string{"limit=abc", "limit=-1", "limit=0"} {
		expectStatus(t, call(h, "GET", "/v1/c/calls/quarantine?"+bad, ""), http.StatusBadRequest)
	}

	expectStatus(t, call(h, "GET", "/v1/c/nope/quarantine", ""), http.StatusNotFound)
}

func TestQuarantineLineIsCutAtTwoKiB(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "calls")

	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}

	long := "{bad" + strings.Repeat("é", 3000) + "\n"

	if err := os.WriteFile(filepath.Join(files, "a.ndjson"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}

	calls := catalog.Def{Name: "calls", Write: catalog.Direct, Path: filepath.Join(files, "*.ndjson"), MaxLineSize: 1 << 20}

	db, err := engine.Open(config.Config{
		DataDir:     filepath.Join(root, "data"),
		Collections: map[string]catalog.Def{"calls": calls},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { db.Close() })

	srv := server.New()

	srv.Attach(db)

	rec := call(srv.Handler(), "GET", "/v1/c/calls/quarantine", "")

	expectStatus(t, rec, http.StatusOK)

	var body struct {
		Rows []struct {
			Line      string `json:"line"`
			Truncated bool   `json:"truncated"`
		} `json:"rows"`
	}

	decodeBody(t, rec.Body.String(), &body)

	if len(body.Rows) != 1 || len(body.Rows[0].Line) > 2048 || len(body.Rows[0].Line) < 2040 || !body.Rows[0].Truncated {
		t.Fatalf("line len = %d truncated = %v", len(body.Rows[0].Line), body.Rows[0].Truncated)
	}
}

func TestNewEndpointsKeepTheTokenRules(t *testing.T) {
	unix, tcp, addr := tokenServer(t, "s3cret")

	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/stats"},
		{"GET", "/v1/size"},
		{"GET", "/v1/c/x"},
		{"GET", "/v1/c/x/quarantine"},
		{"POST", "/v1/c/x/compact"},
	} {
		req, _ := http.NewRequest(c.method, "http://"+addr+c.path, nil)

		resp, err := tcp.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s over TCP without token: %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}

	resp, body := do(t, unix, "GET", "/v1/stats", "", nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unix without token: %d %s", resp.StatusCode, body)
	}
}
