package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The contract of _watch and _ack (docs/dev/http/watch.md, docs/dev/watch.md
// and the long-lived requests of http.md).

// cwDo sends one request to the in-process handler of env, for writes that
// must not wait on the socket.
func cwDo(t *testing.T, env *watchEnv, method, path, body string) {
	t.Helper()

	rec := call(env.srv.Handler(), method, path, body)

	if rec.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, path, rec.Code, rec.Body.String())
	}
}

// cwEnded tells if the stream ends (EOF) within 5 s.
func cwEnded(r *lineReader) bool {
	done := make(chan bool, 1)

	go func() { done <- !r.sc.Scan() }()

	select {
	case ended := <-done:
		return ended
	case <-time.After(5 * time.Second):
		return false
	}
}

// cwError reads the error body of a call that fails before the stream.
func cwError(t *testing.T, resp *http.Response) (int, string) {
	t.Helper()

	defer resp.Body.Close()

	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("error body: %v", err)
	}

	if body.Error.Message == "" {
		t.Fatalf("error without a message: %+v", body)
	}

	return resp.StatusCode, body.Error.Code
}

func TestWatchContractEventShapes(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	cwDo(t, env, "POST", "/calls/_bulk", `{"docs":[{"_id":"a","status":"failed"}]}`)
	cwDo(t, env, "POST", "/calls/_update_by_query", `{"query":{"_id":"a"},"update":{"$inc":{"tries":1}}}`)
	cwDo(t, env, "DELETE", "/calls/_doc/a", ``)

	r, resp := env.watch(context.Background(), "calls", `{"consumer":"c","limit":3}`)

	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content type = %q", ct)
	}

	put := r.next()
	doc, _ := put["doc"].(map[string]any)

	if put["op"] != "put" || put["_id"] != "a" || put["_v"] != 1.0 || doc["status"] != "failed" || put["pos"] == "" {
		t.Fatalf("insert event = %v", put)
	}

	upd := r.next()
	doc, _ = upd["doc"].(map[string]any)

	// An update sends the resulting document, never the operators.
	if upd["op"] != "put" || upd["_v"] != 2.0 || doc["tries"] != 1.0 || doc["status"] != "failed" || doc["$inc"] != nil {
		t.Fatalf("update event = %v", upd)
	}

	del := r.next()

	if _, ok := del["doc"]; ok || del["op"] != "del" || del["_id"] != "a" || del["_v"] != 3.0 {
		t.Fatalf("delete event = %v, want no doc", del)
	}

	for _, ev := range []map[string]any{put, upd, del} {
		if _, ok := ev["_expired"]; ok {
			t.Fatalf("only a delete by the TTL sweeper has _expired: %v", ev)
		}
	}

	if !cwEnded(r) {
		t.Fatal("the stream must end after limit events")
	}
}

// A del has no document: it is sent only if the last put of that id seen by
// the stream matched. A truncate is never left out.
func TestWatchContractQueryAndDeletes(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	cwDo(t, env, "POST", "/calls/_bulk", `{"docs":[{"_id":"a","status":"failed"},{"_id":"b","status":"ok"},{"_id":"c","status":"failed"}]}`)
	cwDo(t, env, "DELETE", "/calls/_doc/b", ``)
	cwDo(t, env, "POST", "/calls/_update_by_query", `{"query":{"_id":"c"},"update":{"$set":{"status":"ok"}}}`)
	cwDo(t, env, "DELETE", "/calls/_doc/c", ``)
	cwDo(t, env, "DELETE", "/calls/_doc/a", ``)

	r, resp := env.watch(context.Background(), "calls", `{"consumer":"relay","query":{"status":"failed"}}`)

	for i, w := range []string{"put a", "put c", "del a"} {
		ev := r.next()

		if got := fmt.Sprintf("%v %v", ev["op"], ev["_id"]); got != w {
			t.Fatalf("event %d = %v, want %s", i, ev, w)
		}

		env.ack("calls", "relay", ev["pos"].(string))
	}

	resp.Body.Close()

	cwDo(t, env, "POST", "/calls/_truncate?confirm=calls", ``)

	// The filter never leaves the truncate out.
	r, resp = env.watch(context.Background(), "calls", `{"consumer":"relay","query":{"status":"failed"},"limit":1}`)

	defer resp.Body.Close()

	if ev := r.next(); ev["op"] != "truncate" || ev["_id"] != nil {
		t.Fatalf("event = %v, want the truncate", ev)
	}
}

// A truncate ends the open streams. Each consumer gets the truncate event
// one time, then the new changes.
func TestWatchContractTruncateEndsTheStream(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 2)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c"}`)

	first := r.next()

	r.next()

	if code := env.ack("posts", "c", first["pos"].(string)); code != http.StatusOK {
		t.Fatalf("ack = %d", code)
	}

	cwDo(t, env, "POST", "/posts/_truncate?confirm=posts", ``)

	// The open stream ends; it may send the truncate event first.
	for range 2 {
		if cwEnded(r) {
			break
		}

		if !strings.Contains(r.sc.Text(), `"op":"truncate"`) {
			t.Fatalf("after a truncate the open stream sent %q", r.sc.Text())
		}
	}

	resp.Body.Close()

	env.insert("posts", 3, 3)

	// The consumer acked p1 only: it resumes with the truncate event, not p2.
	r, resp = env.watch(context.Background(), "posts", `{"consumer":"c"}`)

	trunc := r.next()

	if trunc["op"] != "truncate" || trunc["_id"] != nil || trunc["pos"] == nil {
		t.Fatalf("after reconnect = %v, want the truncate event (no _id)", trunc)
	}

	again := r.next()

	if again["_id"] != "p3" {
		t.Fatalf("after the truncate = %v, want p3", again)
	}

	resp.Body.Close()

	env.ack("posts", "c", again["pos"].(string))

	// A new consumer from the beginning gets the truncate one time too.
	r, resp = env.watch(context.Background(), "posts", `{"consumer":"new","limit":2}`)

	defer resp.Body.Close()

	if ev := r.next(); ev["op"] != "truncate" {
		t.Fatalf("new consumer = %v, want the truncate first", ev)
	}

	if ev := r.next(); ev["_id"] != "p3" {
		t.Fatalf("new consumer second = %v", ev)
	}
}

// A drop ends the open streams; then the collection is unknown.
func TestWatchContractDropEndsTheStream(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 1)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c"}`)

	defer resp.Body.Close()

	r.next()

	cwDo(t, env, "DELETE", "/posts?confirm=posts", ``)

	if !cwEnded(r) {
		t.Fatal("a drop must end the stream")
	}

	status, code := cwError(t, env.post(context.Background(), "/posts/_watch", `{"consumer":"c"}`))

	if status != http.StatusNotFound || code != "unknown_collection" {
		t.Fatalf("watch after drop = %d %s", status, code)
	}
}

// from is used only by a consumer with no saved position.
func TestWatchContractFromNowNeedsNoSavedPosition(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 3)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c","limit":1}`)
	first := r.next()

	resp.Body.Close()

	env.ack("posts", "c", first["pos"].(string))

	r, resp = env.watch(context.Background(), "posts", `{"consumer":"c","from":"now","limit":1}`)

	defer resp.Body.Close()

	if ev := r.next(); ev["_id"] != "p2" {
		t.Fatalf("a consumer with an ack resumes after it: %v, want p2", ev)
	}
}

// An older ack is ignored; the answer is {"ok":true}.
func TestWatchContractOlderAckIsIgnored(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	env.insert("posts", 1, 4)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c","limit":3}`)

	var pos []string

	for range 3 {
		pos = append(pos, r.next()["pos"].(string))
	}

	resp.Body.Close()

	for _, p := range []string{pos[2], pos[0]} {
		resp := env.post(context.Background(), "/posts/_ack", fmt.Sprintf(`{"consumer":"c","pos":%q}`, p))
		body, _ := io.ReadAll(resp.Body)

		resp.Body.Close()

		if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != `{"ok":true}` {
			t.Fatalf("ack %s = %d %s", p, resp.StatusCode, body)
		}
	}

	r, resp = env.watch(context.Background(), "posts", `{"consumer":"c","limit":1}`)

	defer resp.Body.Close()

	if ev := r.next(); ev["_id"] != "p4" {
		t.Fatalf("after an older ack = %v, want p4", ev)
	}
}

// Heartbeats are not events: they do not count for limit.
func TestWatchContractHeartbeatsDoNotCountForLimit(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), 20*time.Millisecond)

	env.insert("posts", 1, 1)

	r, resp := env.watch(context.Background(), "posts", `{"consumer":"c","from":"now","limit":1}`)

	defer resp.Body.Close()

	for range 3 {
		if hb := r.next(); hb["heartbeat"] != true || len(hb) != 1 {
			t.Fatalf("got %v, want {\"heartbeat\":true}", hb)
		}
	}

	env.insert("posts", 2, 2)

	for {
		ev := r.next()

		if ev["heartbeat"] == true {
			continue
		}

		if ev["_id"] != "p2" {
			t.Fatalf("event = %v, want p2", ev)
		}

		break
	}

	if !cwEnded(r) {
		t.Fatal("the stream must end after one event")
	}
}

func TestWatchContractErrorsBeforeTheStream(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Hour)

	cwDo(t, env, "PUT", "/events", `{"time":"at"}`)
	cwDo(t, env, "PUT", "/logs-2026-11", `{}`)

	cases := []struct {
		path, body string
		status     int
		code       string
	}{
		{"/events/_watch", `{"consumer":"c","query":{"at":{"$gte":"now-1x"}}}`, 400, "invalid_query"},
		{"/events/_watch", `{"consumer":"c","query":{"at":{"$bogus":1}}}`, 400, "invalid_query"},
		{"/events/_watch", `{"consumer":"c","query":[1]}`, 400, "invalid_request"},
		{"/events/_watch", `{"consumer":"c","filter":{}}`, 400, "invalid_request"},
		{"/events/_watch", `{"consumer":"c","limit":"1"}`, 400, "invalid_request"},
		{"/events/_watch", `not json`, 400, "invalid_request"},
		{"/logs-*/_watch", `{"consumer":"c"}`, 400, "pattern_read_only"},
		{"/logs-2026-11,events/_watch", `{"consumer":"c"}`, 400, "pattern_read_only"},
		{"/logs-*/_ack", `{"consumer":"c","pos":"e1.1"}`, 400, "pattern_read_only"},
		{"/nope/_ack", `{"consumer":"c","pos":"e1.1"}`, 404, "unknown_collection"},
		{"/events/_ack", `{"consumer":"c"}`, 400, "invalid_request"},
		{"/events/_ack", `{"consumer":"c","pos":"e1.1","extra":1}`, 400, "invalid_request"},
		{"/events/_ack", `{"consumer":"c","pos":"e1"}`, 400, "invalid_position"},
		{"/events/_ack", `{"consumer":"c","pos":"e1.-1"}`, 400, "invalid_position"},
	}

	for _, c := range cases {
		t.Run(c.path+" "+c.body, func(t *testing.T) {
			status, code := cwError(t, env.post(context.Background(), c.path, c.body))

			if status != c.status || code != c.code {
				t.Fatalf("= %d %s, want %d %s", status, code, c.status, c.code)
			}
		})
	}

	// Date math works in the query of a watch.
	cwDo(t, env, "POST", "/events/_bulk", `{"docs":[{"_id":"old","at":"2020-01-01T00:00:00Z"},{"_id":"fut","at":"2099-01-01T00:00:00Z"}]}`)

	r, resp := env.watch(context.Background(), "events", `{"consumer":"dm","query":{"at":{"$gte":"now-1d"}},"limit":1}`)

	defer resp.Body.Close()

	if ev := r.next(); ev["_id"] != "fut" {
		t.Fatalf("date math event = %v, want fut", ev)
	}
}

// A consumer that does not read is cut with an error line, watch_lagged,
// after the response started; then the stream ends. It resumes after its
// last ack.
func TestWatchContractLaggedIsAnErrorLine(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 6000 records")
	}

	env := newWatchEnv(t, t.TempDir(), time.Hour)

	cwDo(t, env, "PUT", "/posts", `{"durability":"relaxed"}`)
	cwDo(t, env, "POST", "/posts/_bulk", `{"docs":[{"_id":"first"}]}`)

	resp := env.post(context.Background(), "/posts/_watch", `{"consumer":"slow"}`)

	defer resp.Body.Close()

	sc := bufio.NewScanner(resp.Body)

	if !sc.Scan() || !strings.Contains(sc.Text(), `"first"`) {
		t.Fatalf("first line = %q", sc.Text())
	}

	var first map[string]any

	json.Unmarshal(sc.Bytes(), &first)

	env.ack("posts", "slow", first["pos"].(string))

	pad := strings.Repeat("x", 1024)

	for i := range 6000 {
		cwDo(t, env, "POST", "/posts/_bulk", fmt.Sprintf(`{"docs":[{"_id":"p%04d","pad":%q}]}`, i, pad))
	}

	var last string

	lines := 0

	for sc.Scan() {
		last = sc.Text()
		lines++
	}

	var end struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(last), &end); err != nil || end.Error.Code != "watch_lagged" || end.Error.Message == "" {
		t.Fatalf("after %d lines the last is %q, want a watch_lagged error line", lines, last)
	}

	if lines >= 6000 {
		t.Fatalf("%d lines: the slow consumer was not cut", lines)
	}

	r, again := env.watch(context.Background(), "posts", `{"consumer":"slow","limit":1}`)

	defer again.Body.Close()

	if ev := r.next(); ev["_id"] != "p0000" {
		t.Fatalf("after watch_lagged = %v, want p0000 (after the last ack)", ev)
	}
}
