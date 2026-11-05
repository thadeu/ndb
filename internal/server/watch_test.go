package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

type watchEnv struct {
	t      *testing.T
	client *http.Client
	srv    *server.Server
	db     *engine.DB
	dir    string
	sock   string
}

func newWatchEnv(t *testing.T, dir string, beat time.Duration) *watchEnv {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	sockDir, err := os.MkdirTemp("", "ndbw")
	if err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(sockDir, "ndb.sock")

	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()
	srv.Attach(db)
	srv.SetWatchHeartbeat(beat)

	go srv.Serve(l)

	env := &watchEnv{t: t, srv: srv, db: db, dir: dir, sock: sock}

	env.client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", sock)
		},
	}}

	t.Cleanup(func() {
		env.stop()
		os.RemoveAll(sockDir)
	})

	return env
}

func (e *watchEnv) stop() {
	if e.srv == nil {
		return
	}

	e.srv.Shutdown(context.Background())
	e.db.Close()

	e.srv = nil
}

func (e *watchEnv) post(ctx context.Context, path, body string) *http.Response {
	e.t.Helper()

	req, err := http.NewRequestWithContext(ctx, "POST", "http://ndb"+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}

	return resp
}

func (e *watchEnv) insert(coll string, from, to int) {
	e.t.Helper()

	for i := from; i <= to; i++ {
		resp := e.post(context.Background(), "/v1/c/"+coll+"/insert", fmt.Sprintf(`{"insert":[{"_id":"p%d","n":%d}]}`, i, i))

		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if resp.StatusCode != 200 {
			e.t.Fatalf("insert: %d", resp.StatusCode)
		}
	}
}

type lineReader struct {
	t  *testing.T
	sc *bufio.Scanner
}

func (e *watchEnv) watch(ctx context.Context, coll, body string) (*lineReader, *http.Response) {
	e.t.Helper()

	resp := e.post(ctx, "/v1/c/"+coll+"/watch", body)

	return &lineReader{t: e.t, sc: bufio.NewScanner(resp.Body)}, resp
}

// next returns the next line as a map, failing after 5 s.
func (r *lineReader) next() map[string]any {
	r.t.Helper()

	got := make(chan bool, 1)

	go func() { got <- r.sc.Scan() }()

	select {
	case ok := <-got:
		if !ok {
			r.t.Fatalf("stream ended: %v", r.sc.Err())
		}
	case <-time.After(5 * time.Second):
		r.t.Fatal("no line in 5s")
	}

	var m map[string]any

	if err := json.Unmarshal(r.sc.Bytes(), &m); err != nil {
		r.t.Fatalf("bad line %q", r.sc.Text())
	}

	return m
}

func (e *watchEnv) ack(coll, consumer, pos string) int {
	e.t.Helper()

	resp := e.post(context.Background(), "/v1/c/"+coll+"/ack", fmt.Sprintf(`{"consumer":%q,"pos":%q}`, consumer, pos))

	defer resp.Body.Close()

	io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}

func TestWatchStreamsHistoryThenLiveAndAckResumes(t *testing.T) {
	dir := t.TempDir()
	env := newWatchEnv(t, dir, time.Hour)

	env.insert("posts", 1, 3)

	ctx, cancel := context.WithCancel(context.Background())

	r, resp := env.watch(ctx, "posts", `{"consumer":"relay"}`)

	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	var third map[string]any

	for i := 1; i <= 3; i++ {
		third = r.next()

		if third["id"] != fmt.Sprintf("p%d", i) || third["op"] != "put" || third["v"] != float64(1) {
			t.Fatalf("event %d = %v", i, third)
		}

		if third["doc"].(map[string]any)["n"] != float64(i) {
			t.Fatalf("doc = %v", third["doc"])
		}
	}

	env.insert("posts", 4, 4)

	if ev := r.next(); ev["id"] != "p4" {
		t.Fatalf("live event = %v", ev)
	}

	if code := env.ack("posts", "relay", third["pos"].(string)); code != 200 {
		t.Fatalf("ack = %d", code)
	}

	cancel()
	resp.Body.Close()
	env.stop()

	env = newWatchEnv(t, dir, time.Hour)

	r, resp = env.watch(context.Background(), "posts", `{"consumer":"relay"}`)

	defer resp.Body.Close()

	if ev := r.next(); ev["id"] != "p4" {
		t.Fatalf("after restart got %v, want p4", ev)
	}
}

func TestWatchFromNowAndLimit(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 2)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c","from":"now","limit":2}`)

	defer resp.Body.Close()

	env.insert("posts", 3, 5)

	for _, id := range []string{"p3", "p4"} {
		if ev := r.next(); ev["id"] != id {
			t.Fatalf("got %v, want %s", ev, id)
		}
	}

	if r.sc.Scan() {
		t.Fatalf("stream must end after limit, got %q", r.sc.Text())
	}
}

func TestWatchSendsHeartbeats(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), 30*time.Millisecond)

	env.insert("posts", 1, 1)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c"}`)

	defer resp.Body.Close()

	r.next()

	for range 2 {
		if hb := r.next(); hb["heartbeat"] != true {
			t.Fatalf("got %v, want a heartbeat", hb)
		}
	}
}

func TestWatchClientDisconnectStopsTheStream(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 1)

	ctx, cancel := context.WithCancel(context.Background())

	r, resp := env.watch(ctx, "posts", `{"consumer":"c"}`)

	r.next()
	cancel()
	resp.Body.Close()

	done := make(chan struct{})

	go func() {
		env.srv.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
		env.srv = nil
		env.db.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown waits on a watcher whose client left")
	}
}

func TestShutdownEndsOpenWatchers(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 1)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c"}`)

	defer resp.Body.Close()

	r.next()

	done := make(chan struct{})

	go func() {
		env.srv.Shutdown(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hangs on an open watcher")
	}
}

func TestWatchTwoConsumersAreIndependent(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 2)

	a, ra := env.watch(context.Background(), "posts", `{"consumer":"a","limit":1}`)
	first := a.next()

	ra.Body.Close()
	env.ack("posts", "a", first["pos"].(string))

	b, rb := env.watch(context.Background(), "posts", `{"consumer":"b","limit":1}`)

	defer rb.Body.Close()

	if ev := b.next(); ev["id"] != "p1" {
		t.Fatalf("b = %v, want p1", ev)
	}

	a2, ra2 := env.watch(context.Background(), "posts", `{"consumer":"a","limit":1}`)

	defer ra2.Body.Close()

	if ev := a2.next(); ev["id"] != "p2" {
		t.Fatalf("a = %v, want p2", ev)
	}
}

func TestWatchAndAckErrors(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 1)

	cases := []struct {
		path, body string
		status     int
		code       string
	}{
		{"/v1/c/posts/watch", `{}`, 400, "invalid_request"},
		{"/v1/c/posts/watch", `{"consumer":"c","from":"later"}`, 400, "invalid_request"},
		{"/v1/c/posts/watch", `{"consumer":"c","limit":-1}`, 400, "invalid_request"},
		{"/v1/c/nope/watch", `{"consumer":"c"}`, 404, "unknown_collection"},
		{"/v1/c/posts/ack", `{"consumer":"c","pos":"garbage"}`, 400, "invalid_position"},
		{"/v1/c/posts/ack", `{"pos":"e1.1"}`, 400, "invalid_request"},
	}

	for _, c := range cases {
		resp := env.post(context.Background(), c.path, c.body)

		var body struct {
			Error struct{ Code string } `json:"error"`
		}

		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()

		if resp.StatusCode != c.status || body.Error.Code != c.code {
			t.Errorf("%s %s = %d %q, want %d %q", c.path, c.body, resp.StatusCode, body.Error.Code, c.status, c.code)
		}
	}
}

func TestWatchDirectModeOverHTTP(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "collections", "calls", "in")

	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}

	os.WriteFile(filepath.Join(in, "a.ndjson"), []byte("{\"event_id\":\"e1\"}\n{\"event_id\":\"e2\"}\n"), 0o644)

	env := newWatchEnv(t, dir, time.Hour)

	def := fmt.Sprintf(`{"id":"event_id","write":"direct","path":%q}`, in+"/*.ndjson")

	req, _ := http.NewRequest("PUT", "http://ndb/v1/c/calls", strings.NewReader(def))

	resp, err := env.client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("ensure: %v %v", err, resp)
	}

	resp.Body.Close()

	r, wresp := env.watch(context.Background(), "calls", `{"consumer":"c"}`)

	defer wresp.Body.Close()

	first := r.next()

	if first["id"] != "e1" || first["op"] != "put" {
		t.Fatalf("first = %v", first)
	}

	if code := env.ack("calls", "c", first["pos"].(string)); code != 200 {
		t.Fatalf("ack = %d", code)
	}

	if second := r.next(); second["id"] != "e2" {
		t.Fatalf("second = %v", second)
	}

	f, _ := os.OpenFile(filepath.Join(in, "a.ndjson"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{\"event_id\":\"e3\"}\n")
	f.Close()

	if third := r.next(); third["id"] != "e3" {
		t.Fatalf("third = %v (live line from the tailer)", third)
	}
}
