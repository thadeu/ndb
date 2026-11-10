package server_test

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/logging"
	"github.com/thadeu/ndb/internal/server"
)

// The browser guards (CSRF, DNS rebinding), the token lockout and the
// limits. See docs/dev/security.md.

// hreq is one request with headers and an optional Host. It gives the
// status, the headers and the body.
type hreq struct {
	method, url, body string
	hdr               map[string]string
	host              string
}

func (c hreq) do(t *testing.T, client *http.Client) (int, http.Header, string) {
	t.Helper()

	req, err := http.NewRequest(c.method, c.url, strings.NewReader(c.body))

	if err != nil {
		t.Fatal(err)
	}

	for k, v := range c.hdr {
		req.Header.Set(k, v)
	}

	if c.host != "" {
		req.Host = c.host
	}

	resp, err := client.Do(req)

	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)

	if err != nil {
		t.Fatal(err)
	}

	return resp.StatusCode, resp.Header, string(data)
}

// openServer is a server with no token on a unix socket and on TCP, as a
// user install (127.0.0.1:7070).
func openServer(t *testing.T, sec server.Security) (unix, tcp *http.Client, base string) {
	t.Helper()

	unix, tcp, addr := authServer(t, func(srv *server.Server) { srv.SetSecurity(sec) })

	return unix, tcp, "http://" + addr
}

// countDocs is the count of documents in coll, over the unix socket; a
// missing collection is 0.
func countDocs(t *testing.T, unix *http.Client, coll string) string {
	t.Helper()

	status, _, body := hreq{method: "POST", url: "http://ndb/" + coll + "/_count", body: `{}`}.do(t, unix)

	if status == http.StatusNotFound {
		return "0"
	}

	return strings.TrimSpace(body)
}

// The attack of the report: a web page posts text/plain to 127.0.0.1:7070.
// The browser adds Origin. Each write route is refused, and nothing is
// written.
func TestCSRFSimpleRequestIsRefused(t *testing.T) {
	unix, tcp, base := openServer(t, server.DefaultSecurity())

	seed := hreq{method: "POST", url: base + "/victim/_doc", body: `{"_id":"keep","a":1}`}

	if status, _, body := seed.do(t, tcp); status != http.StatusOK {
		t.Fatalf("seed: %d %s", status, body)
	}

	attacks := []hreq{
		{method: "POST", url: base + "/victim/_doc", body: `{"a":2}`},
		{method: "POST", url: base + "/victim/_bulk", body: `{"docs":[{"a":3}]}`},
		{method: "POST", url: base + "/victim/_update/keep", body: `{"update":{"$set":{"a":9}}}`},
		{method: "POST", url: base + "/victim/_delete_by_query", body: `{"query":{},"many":true}`},
		{method: "POST", url: base + "/victim/_truncate?confirm=victim", body: ``},
		{method: "GET", url: base + "/victim/_doc/keep"},
	}

	for _, a := range attacks {
		a.hdr = map[string]string{"Content-Type": "text/plain", "Origin": "https://evil.example"}

		status, _, body := a.do(t, tcp)

		if status != http.StatusForbidden || !strings.Contains(body, `"code":"forbidden"`) {
			t.Fatalf("%s %s: %d %s, want 403 forbidden", a.method, a.url, status, body)
		}

		if !strings.Contains(body, "https://evil.example") || !strings.Contains(body, "cors_origins") {
			t.Fatalf("the message names neither the origin nor the setting: %s", body)
		}
	}

	if got := countDocs(t, unix, "victim"); !strings.Contains(got, `"count":1`) {
		t.Fatalf("victim after the attacks: %s, want the one seed document", got)
	}

	if status, _, body := (hreq{method: "GET", url: "http://ndb/victim/_doc/keep"}).do(t, unix); !strings.Contains(body, `"a":1`) {
		t.Fatalf("the seed changed: %d %s", status, body)
	}
}

// A browser request without Origin (an <img>, a link) has Sec-Fetch-Site.
// cross-site and same-site are refused; none (a URL typed in the address
// bar) and same-origin pass.
func TestSecFetchSite(t *testing.T) {
	_, tcp, base := openServer(t, server.DefaultSecurity())

	cases := map[string]int{"cross-site": 403, "same-site": 403, "none": 200, "same-origin": 200}

	for site, want := range cases {
		c := hreq{method: "GET", url: base + "/_health", hdr: map[string]string{"Sec-Fetch-Site": site}}

		if status, _, body := c.do(t, tcp); status != want {
			t.Errorf("Sec-Fetch-Site %s: %d %s, want %d", site, status, body, want)
		}
	}
}

// The Origin guard runs on the unix socket too.
func TestOriginRefusedOnUnixSocket(t *testing.T) {
	unix, _, _ := openServer(t, server.DefaultSecurity())

	c := hreq{method: "POST", url: "http://ndb/victim/_doc", body: `{"a":1}`, hdr: map[string]string{"Origin": "https://evil.example"}}

	if status, _, body := c.do(t, unix); status != http.StatusForbidden {
		t.Fatalf("unix with Origin: %d %s", status, body)
	}
}

// curl, the SDKs and the console server send no Origin: nothing changes
// for them.
func TestNoOriginUnchanged(t *testing.T) {
	unix, tcp, base := openServer(t, server.DefaultSecurity())

	c := hreq{method: "POST", url: base + "/calls/_doc", body: `{"a":1}`, hdr: map[string]string{"Content-Type": "text/plain"}}

	if status, hdr, body := c.do(t, tcp); status != http.StatusOK || hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("curl-like write: %d %v %s", status, hdr, body)
	}

	if got := countDocs(t, unix, "calls"); !strings.Contains(got, `"count":1`) {
		t.Fatalf("count: %s", got)
	}

	opt := hreq{method: "OPTIONS", url: base + "/calls/_doc"}

	if status, _, body := opt.do(t, tcp); status != http.StatusMethodNotAllowed {
		t.Fatalf("OPTIONS without Origin: %d %s, want 405", status, body)
	}
}

// An origin of cors_origins gets the CORS headers and a preflight answer,
// before the token check (a preflight carries no token).
func TestAllowedOriginGetsCORS(t *testing.T) {
	const app = "https://app.example.com"

	_, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetToken("s3cret")
		srv.SetSecurity(server.Security{CORSOrigins: []string{"https://App.example.com/"}})
	})

	base := "http://" + addr

	pre := hreq{method: "OPTIONS", url: base + "/calls/_doc", hdr: map[string]string{
		"Origin":                         app,
		"Access-Control-Request-Method":  "POST",
		"Access-Control-Request-Headers": "authorization, content-type",
	}}

	status, hdr, body := pre.do(t, tcp)

	if status != http.StatusNoContent {
		t.Fatalf("preflight: %d %s", status, body)
	}

	if hdr.Get("Access-Control-Allow-Origin") != app || !strings.Contains(hdr.Get("Vary"), "Origin") {
		t.Fatalf("preflight headers: %v", hdr)
	}

	for _, want := range []string{"Authorization", "Content-Type"} {
		if !strings.Contains(hdr.Get("Access-Control-Allow-Headers"), want) {
			t.Fatalf("Allow-Headers %q has no %s", hdr.Get("Access-Control-Allow-Headers"), want)
		}
	}

	if !strings.Contains(hdr.Get("Access-Control-Allow-Methods"), "POST") {
		t.Fatalf("Allow-Methods: %q", hdr.Get("Access-Control-Allow-Methods"))
	}

	write := hreq{method: "POST", url: base + "/calls/_doc", body: `{"a":1}`, hdr: map[string]string{
		"Origin":        app,
		"Content-Type":  "application/json",
		"Authorization": "Bearer s3cret",
	}}

	status, hdr, body = write.do(t, tcp)

	if status != http.StatusOK || hdr.Get("Access-Control-Allow-Origin") != app || !strings.Contains(hdr.Get("Access-Control-Expose-Headers"), "X-Request-Id") {
		t.Fatalf("allowed write: %d %v %s", status, hdr, body)
	}

	write.hdr["Authorization"] = ""

	if status, hdr, _ = write.do(t, tcp); status != http.StatusUnauthorized || hdr.Get("Access-Control-Allow-Origin") != app {
		t.Fatalf("allowed origin, no token: %d %v, want 401 with CORS (so the page can read it)", status, hdr)
	}

	other := hreq{method: "OPTIONS", url: base + "/calls/_doc", hdr: map[string]string{"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"}}

	if status, hdr, _ = other.do(t, tcp); status != http.StatusForbidden || hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("preflight of another origin: %d %v", status, hdr)
	}
}

// DNS rebinding: without a token, a TCP request must name an IP,
// localhost, a name with no dot or a name of allowed_hosts.
func TestHostGuard(t *testing.T) {
	unix, tcp, base := openServer(t, server.Security{AllowedHosts: []string{"ndb.internal"}})

	cases := map[string]int{
		"evil.example:7070":  403,
		"evil.example":       403,
		"127.0.0.1.nip.io":   403,
		"127.0.0.1:7070":     200,
		"localhost:7070":     200,
		"LOCALHOST":          200,
		"[::1]:7070":         200,
		"10.0.0.5":           200,
		"ndb:7070":           200,
		"ndb.internal:7070":  200,
		"NDB.Internal":       200,
		"ndb.internal.other": 403,
	}

	for host, want := range cases {
		status, _, body := hreq{method: "GET", url: base + "/_collections", host: host}.do(t, tcp)

		if status != want {
			t.Errorf("Host %s: %d %s, want %d", host, status, body, want)
		}

		if want == 403 && (!strings.Contains(body, `"code":"forbidden"`) || !strings.Contains(body, "allowed_hosts")) {
			t.Errorf("Host %s: body %s", host, body)
		}
	}

	if status, _, body := (hreq{method: "GET", url: "http://ndb/_collections", host: "evil.example"}).do(t, unix); status != 200 {
		t.Fatalf("unix with Host evil.example: %d %s", status, body)
	}
}

// With a token, a rebound page cannot send it: the Host is not checked.
func TestHostGuardSkippedWithAToken(t *testing.T) {
	_, tcp, addr := tokenServer(t, "s3cret")

	c := hreq{method: "GET", url: "http://" + addr + "/_collections", host: "ndb.example.com", hdr: map[string]string{"Authorization": "Bearer s3cret"}}

	if status, _, body := c.do(t, tcp); status != 200 {
		t.Fatalf("token and a public Host: %d %s", status, body)
	}
}

// lockoutServer is a token server with a small lockout and a JSON log.
func lockoutServer(t *testing.T, failures int) (unix, tcp *http.Client, base string, logs *logBuf) {
	t.Helper()

	logs = &logBuf{}

	unix, tcp, addr := authServer(t, func(srv *server.Server) {
		srv.SetToken("s3cret")
		srv.SetLogger(logging.New(logs, logging.Options{Level: slog.LevelInfo, Format: "json"}))
		srv.SetSecurity(server.Security{AuthFailures: failures, AuthWindow: time.Minute, AuthLockout: time.Minute})
	})

	return unix, tcp, "http://" + addr, logs
}

func TestTokenLockout(t *testing.T) {
	unix, tcp, base, logs := lockoutServer(t, 3)

	bad := hreq{method: "GET", url: base + "/_health", hdr: map[string]string{"Authorization": "Bearer nope"}}
	good := hreq{method: "GET", url: base + "/_health", hdr: map[string]string{"Authorization": "Bearer s3cret"}}

	for i := range 3 {
		if status, _, body := bad.do(t, tcp); status != http.StatusUnauthorized {
			t.Fatalf("failure %d: %d %s, want 401", i+1, status, body)
		}
	}

	status, hdr, body := bad.do(t, tcp)

	if status != http.StatusTooManyRequests || !strings.Contains(body, `"code":"too_many_requests"`) {
		t.Fatalf("after 3 failures: %d %s, want 429 too_many_requests", status, body)
	}

	if hdr.Get("Retry-After") == "" || hdr.Get("Retry-After") == "0" {
		t.Fatalf("Retry-After = %q", hdr.Get("Retry-After"))
	}

	// The right token is refused too while the lockout runs, so a guess
	// cannot be tested.
	if status, _, _ := good.do(t, tcp); status != http.StatusTooManyRequests {
		t.Fatalf("right token during the lockout: %d, want 429", status)
	}

	if lines := logs.lines(t, "token lockout"); len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["ip"] != "127.0.0.1" {
		t.Fatalf("lockout lines: %v", lines)
	}

	if status, _, body := (hreq{method: "GET", url: "http://ndb/_health"}).do(t, unix); status != 200 {
		t.Fatalf("unix during the lockout: %d %s", status, body)
	}
}

// A request with the right token never counts: only failures lock out.
func TestTokenLockoutCountsOnlyFailures(t *testing.T) {
	_, tcp, base, _ := lockoutServer(t, 2)

	bad := hreq{method: "GET", url: base + "/_health", hdr: map[string]string{"Authorization": "Bearer nope"}}
	good := hreq{method: "GET", url: base + "/_health", hdr: map[string]string{"Authorization": "Bearer s3cret"}}

	for range 50 {
		if status, _, body := good.do(t, tcp); status != 200 {
			t.Fatalf("success: %d %s", status, body)
		}
	}

	if status, _, _ := bad.do(t, tcp); status != http.StatusUnauthorized {
		t.Fatalf("first failure: %d", status)
	}

	if status, _, _ := good.do(t, tcp); status != 200 {
		t.Fatalf("success after one failure: %d", status)
	}
}

// The unix socket has no token check, so no lockout.
func TestUnixSocketNeverLockedOut(t *testing.T) {
	unix, _, _, _ := lockoutServer(t, 1)

	for range 20 {
		c := hreq{method: "GET", url: "http://ndb/_health", hdr: map[string]string{"Authorization": "Bearer nope"}}

		if status, _, body := c.do(t, unix); status != 200 {
			t.Fatalf("unix: %d %s", status, body)
		}
	}
}

// max_conns: a second connection waits until the first one closes.
func TestMaxConns(t *testing.T) {
	db, err := engine.Open(config.Config{DataDir: t.TempDir(), AllowImplicit: true}, slog.New(slog.DiscardHandler))

	if err != nil {
		t.Fatal(err)
	}

	tl, err := net.Listen("tcp", "127.0.0.1:0")

	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()
	srv.Attach(db)

	go srv.Serve(server.LimitListener(tl, 1))

	t.Cleanup(func() {
		srv.Shutdown(context.Background())
		db.Close()
	})

	addr := tl.Addr().String()

	first := rawConn(t, addr)

	if line := rawHealth(t, first, time.Second); !strings.Contains(line, "200") {
		t.Fatalf("first connection: %q", line)
	}

	second := rawConn(t, addr)

	if line := rawHealth(t, second, 300*time.Millisecond); line != "" {
		t.Fatalf("second connection was served while the first is open: %q", line)
	}

	first.Close()

	if line := readStatus(t, second, 2*time.Second); !strings.Contains(line, "200") {
		t.Fatalf("second connection after the first closed: %q", line)
	}
}

func rawConn(t *testing.T, addr string) net.Conn {
	t.Helper()

	c, err := net.Dial("tcp", addr)

	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { c.Close() })

	return c
}

// rawHealth sends GET /_health and gives the status line, or "" when no
// answer comes in wait.
func rawHealth(t *testing.T, c net.Conn, wait time.Duration) string {
	t.Helper()

	if _, err := io.WriteString(c, "GET /_health HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	return readStatus(t, c, wait)
}

func readStatus(t *testing.T, c net.Conn, wait time.Duration) string {
	t.Helper()

	_ = c.SetReadDeadline(time.Now().Add(wait))

	line, err := bufio.NewReader(c).ReadString('\n')

	if err != nil {
		return ""
	}

	return line
}

// max_watches: a _watch over the limit is 429 before the stream starts,
// and /_metrics shows the open streams.
func TestMaxWatches(t *testing.T) {
	unix, _, _ := openServer(t, server.Security{MaxWatches: 1})

	if status, _, body := (hreq{method: "POST", url: "http://ndb/posts/_doc", body: `{"a":1}`}).do(t, unix); status != http.StatusOK {
		t.Fatalf("seed: %d %s", status, body)
	}

	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "POST", "http://ndb/posts/_watch", strings.NewReader(`{"consumer":"a"}`))

	resp, err := unix.Do(req)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != 200 {
		t.Fatalf("first watch: %d", resp.StatusCode)
	}

	if got := watchGauge(t, unix); got != "1" {
		t.Fatalf("ndb_watch_streams = %q, want 1", got)
	}

	second := hreq{method: "POST", url: "http://ndb/posts/_watch", body: `{"consumer":"b","limit":1}`}

	status, hdr, body := second.do(t, unix)

	if status != http.StatusTooManyRequests || !strings.Contains(body, `"code":"too_many_requests"`) || hdr.Get("Retry-After") == "" {
		t.Fatalf("second watch: %d %v %s, want 429 too_many_requests", status, hdr, body)
	}

	cancel()
	resp.Body.Close()

	deadline := time.Now().Add(3 * time.Second)

	for watchGauge(t, unix) != "0" {
		if time.Now().After(deadline) {
			t.Fatal("the closed watch is still counted")
		}

		time.Sleep(10 * time.Millisecond)
	}

	if status, _, body := second.do(t, unix); status != 200 {
		t.Fatalf("watch after the first closed: %d %s", status, body)
	}
}

// watchGauge is the value of ndb_watch_streams in /_metrics.
func watchGauge(t *testing.T, c *http.Client) string {
	t.Helper()

	_, _, body := hreq{method: "GET", url: "http://ndb/_metrics"}.do(t, c)

	for line := range strings.Lines(body) {
		if v, ok := strings.CutPrefix(line, "ndb_watch_streams "); ok {
			return strings.TrimSpace(v)
		}
	}

	return ""
}

// The request line has the User-Agent, so the SDK of a call shows.
func TestRequestLineHasUserAgent(t *testing.T) {
	_, h, buf := logServer(t, logging.Options{Level: slog.LevelDebug}, true)

	req, _ := http.NewRequest("GET", "/_health", nil)
	req.Header.Set("User-Agent", "ndb-go/1.18.0 (go1.26)")

	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	lines := buf.lines(t, "request")

	if len(lines) != 1 || lines[0]["user_agent"] != "ndb-go/1.18.0 (go1.26)" {
		t.Fatalf("request lines: %v", lines)
	}
}
