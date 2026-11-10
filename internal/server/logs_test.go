package server_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/logging"
	"github.com/thadeu/ndb/internal/server"
)

// logBuf is a buffer that the server goroutines may write at the same time.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(p)
}

func (b *logBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

// lines are the JSON log lines with this msg.
func (b *logBuf) lines(t *testing.T, msg string) []map[string]any {
	t.Helper()

	var out []map[string]any

	for raw := range strings.SplitSeq(strings.TrimSpace(b.String()), "\n") {
		if raw == "" {
			continue
		}

		var m map[string]any

		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", raw, err)
		}

		if m["msg"] == msg {
			out = append(out, m)
		}
	}

	return out
}

// logServer is a server with a JSON logger into a buffer.
func logServer(t *testing.T, o logging.Options, attach bool) (*server.Server, http.Handler, *logBuf) {
	t.Helper()

	buf := &logBuf{}
	o.Format = "json"

	srv := server.New()
	srv.SetLogger(logging.New(buf, o))

	if attach {
		db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.New(slog.DiscardHandler))
		if err != nil {
			t.Fatal(err)
		}

		t.Cleanup(func() { db.Close() })

		srv.Attach(db)
	}

	return srv, srv.Handler(), buf
}

func TestDebugWriteLogHasIDVersionAndCutBody(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelDebug, BodyMax: 40}, true)

	doc := `{"_id":"a1","status":"missed","note":"` + strings.Repeat("x", 100) + `"}`

	req := httptest.NewRequest("POST", "/calls/_doc", strings.NewReader(doc))
	req.Header.Set("Authorization", "Bearer s3cret-value")

	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	expectStatus(t, rec, http.StatusOK)

	writes := buf.lines(t, "write")

	if len(writes) != 1 {
		t.Fatalf("write lines = %d, want 1: %s", len(writes), buf)
	}

	w := writes[0]

	if w["op"] != "insert" || w["coll"] != "calls" || w["_id"] != "a1" || w["_v"] != float64(1) {
		t.Fatalf("write line = %v", w)
	}

	body, _ := w["doc"].(string)

	if !strings.HasPrefix(body, doc[:40]) || !strings.HasSuffix(body, "...[cut, "+itoa(len(doc))+" bytes]") {
		t.Fatalf("doc = %q, want the first 40 bytes and the size marker", body)
	}

	if w["request_id"] == "" || w["request_id"] != rec.Header().Get("X-Request-Id") {
		t.Fatalf("request_id = %v, header %q", w["request_id"], rec.Header().Get("X-Request-Id"))
	}

	if strings.Contains(buf.String(), "s3cret-value") || strings.Contains(buf.String(), "Authorization") {
		t.Fatalf("the log holds the Authorization header: %s", buf)
	}

	reqs := buf.lines(t, "request")

	if len(reqs) != 1 || reqs[0]["status"] != float64(200) || reqs[0]["body"] != nil {
		t.Fatalf("request lines = %v, want one, without the body that the write line has", reqs)
	}
}

func TestDebugBulkLogsTheFirstDocs(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelDebug, BodyMax: 2048}, true)

	var docs []string

	for i := range 10 {
		docs = append(docs, `{"_id":"d`+itoa(i)+`"}`)
	}

	rec := call(h, "POST", "/calls/_bulk", `{"docs":[`+strings.Join(docs, ",")+`]}`)

	expectStatus(t, rec, http.StatusOK)

	writes := buf.lines(t, "write")

	if len(writes) != 1 {
		t.Fatalf("write lines = %d, want 1 for the batch", len(writes))
	}

	w := writes[0]

	if w["count"] != float64(10) || len(w["ids"].([]any)) != 10 || len(w["docs"].([]any)) != 3 {
		t.Fatalf("bulk line = %v, want 10 ids and the first 3 docs", w)
	}
}

func TestReadLogHasQueryBodyAndRows(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelDebug, BodyMax: 2048}, true)

	call(h, "POST", "/calls/_doc", `{"status":"missed"}`)
	call(h, "POST", "/calls/_doc", `{"status":"ok"}`)

	expectStatus(t, call(h, "POST", "/calls/_search", `{"query":{"status":"missed"}}`), http.StatusOK)

	var found map[string]any

	for _, l := range buf.lines(t, "request") {
		if l["path"] == "/calls/_search" {
			found = l
		}
	}

	if found == nil || found["rows"] != float64(1) || found["body"] != `{"query":{"status":"missed"}}` {
		t.Fatalf("search line = %v", found)
	}
}

func TestNoBodyWhenBodyMaxIsZero(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelDebug, BodyMax: 0}, true)

	call(h, "POST", "/calls/_doc", `{"_id":"a","secret_field":"zzz"}`)

	if strings.Contains(buf.String(), "secret_field") {
		t.Fatalf("log_body_max 0 still logs bodies: %s", buf)
	}

	if len(buf.lines(t, "write")) != 1 {
		t.Fatalf("want the write line, with ids only: %s", buf)
	}
}

func TestInfoLevelLogsNoRequestLine(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelInfo, BodyMax: 2048}, true)

	call(h, "POST", "/calls/_doc", `{"_id":"a"}`)
	call(h, "GET", "/calls/_doc/a", "")
	call(h, "GET", "/calls/_doc/nope", "")

	if len(buf.lines(t, "request")) != 0 || len(buf.lines(t, "write")) != 0 {
		t.Fatalf("info logs debug lines: %s", buf)
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	_, h, _ := logServer(t, logging.Options{Level: slog.LevelInfo}, true)

	send := func(id string) string {
		req := httptest.NewRequest("GET", "/_health", nil)

		if id != "" {
			req.Header.Set("X-Request-Id", id)
		}

		rec := httptest.NewRecorder()

		h.ServeHTTP(rec, req)

		return rec.Header().Get("X-Request-Id")
	}

	if got := send("job-42.a:b_c"); got != "job-42.a:b_c" {
		t.Fatalf("X-Request-Id = %q, want the id of the client", got)
	}

	for _, bad := range []string{"has space", "new\nline", strings.Repeat("a", 129)} {
		got := send(bad)

		if got == "" || got == bad {
			t.Fatalf("X-Request-Id for %q = %q, want a new id", bad, got)
		}
	}

	a, b := send(""), send("")

	if a == "" || a == b {
		t.Fatalf("generated ids %q and %q, want two different ids", a, b)
	}
}

func TestServerErrorLogsAtError(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelError}, false)

	rec := call(h, "POST", "/calls/_search", `{}`)

	expectStatus(t, rec, http.StatusServiceUnavailable)

	lines := buf.lines(t, "request failed")

	if len(lines) != 1 {
		t.Fatalf("error lines = %d, want 1: %s", len(lines), buf)
	}

	l := lines[0]

	if l["level"] != "ERROR" || l["code"] != "not_ready" || l["coll"] != "calls" || l["method"] != "POST" || l["request_id"] != rec.Header().Get("X-Request-Id") {
		t.Fatalf("error line = %v", l)
	}
}

func TestQuietLogsNothing(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: logging.LevelQuiet, BodyMax: 2048, SlowMs: 1}, false)

	call(h, "POST", "/calls/_search", `{}`)
	call(h, "POST", "/calls/_doc", `{}`)

	if buf.String() != "" {
		t.Fatalf("quiet wrote: %s", buf)
	}
}

func TestLogRouteChangesTheLevel(t *testing.T) {
	srv, h, _ := logServer(t, logging.Options{Level: slog.LevelInfo, BodyMax: 2048, SlowMs: 1000}, false)

	rec := call(h, "GET", "/_log", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"level":"info"`) || !strings.Contains(rec.Body.String(), `"format":"json"`) {
		t.Fatalf("GET /_log = %s", rec.Body)
	}

	rec = call(h, "PUT", "/_log", `{"level":"debug","body_max":100,"slow_ms":0}`)

	expectStatus(t, rec, http.StatusOK)

	if srv.Logger().Level() != slog.LevelDebug || srv.Logger().BodyMax() != 100 || srv.Logger().SlowMs() != 0 {
		t.Fatalf("after PUT: %s", rec.Body)
	}

	for _, bad := range []string{`{"level":"loud"}`, `{"level":""}`, `{"body_max":-1}`, `{"format":"json"}`} {
		if code := errorCode(t, call(h, "PUT", "/_log", bad)); code != "invalid_request" {
			t.Fatalf("PUT /_log %s = %s, want invalid_request", bad, code)
		}
	}
}

func TestLogRouteNeedsAdminOfEverything(t *testing.T) {
	_, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetLogger(logging.New(io.Discard, logging.Options{Level: slog.LevelInfo}))
		srv.SetTokens([]server.Token{
			{Name: "ops", Secret: "admin-secret", Admin: []string{"*"}},
			{Name: "app", Secret: "app-secret", Write: []string{"*"}, Admin: []string{"calls"}},
		})
	})

	send := func(method, token, body string) int {
		req, _ := http.NewRequest(method, "http://"+addr+"/_log", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := tcp.Do(req)

		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()

		return resp.StatusCode
	}

	if got := send("GET", "app-secret", ""); got != http.StatusForbidden {
		t.Fatalf("GET /_log with a scoped token = %d, want 403", got)
	}

	if got := send("PUT", "app-secret", `{"level":"debug"}`); got != http.StatusForbidden {
		t.Fatalf("PUT /_log with a scoped token = %d, want 403", got)
	}

	if got := send("GET", "admin-secret", ""); got != http.StatusOK {
		t.Fatalf("GET /_log with admin * = %d, want 200", got)
	}

	if got := send("PUT", "admin-secret", `{"level":"warn"}`); got != http.StatusOK {
		t.Fatalf("PUT /_log with admin * = %d, want 200", got)
	}
}

func TestTokenNameNotSecretOnTheLine(t *testing.T) {
	buf := &logBuf{}

	_, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetLogger(logging.New(buf, logging.Options{Level: slog.LevelDebug, Format: "json", BodyMax: 2048}))
		srv.SetTokens([]server.Token{{Name: "app", Secret: "app-secret", Write: []string{"*"}}})
	})

	req, _ := http.NewRequest("POST", "http://"+addr+"/calls/_doc", strings.NewReader(`{"_id":"x"}`))
	req.Header.Set("Authorization", "Bearer app-secret")

	resp, err := tcp.Do(req)

	if err != nil {
		t.Fatal(err)
	}

	resp.Body.Close()

	lines := buf.lines(t, "request")

	if len(lines) != 1 || lines[0]["token"] != "app" {
		t.Fatalf("request lines = %v, want token app", lines)
	}

	if strings.Contains(buf.String(), "app-secret") {
		t.Fatalf("the log holds the token secret: %s", buf)
	}
}

func itoa(n int) string {
	b, _ := json.Marshal(n)

	return string(b)
}
