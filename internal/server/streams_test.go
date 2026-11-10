package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

// openAt starts a server on dir; stop shuts it down and closes the engine.
func openAt(t *testing.T, dir string) (http.Handler, func()) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.Default())

	if err != nil {
		t.Fatal(err)
	}

	srv := server.New()
	srv.Attach(db)

	return srv.Handler(), func() {
		_ = srv.Shutdown(context.Background())
		_ = db.Close()
	}
}

func jsonOf(t *testing.T, body string, v any) {
	t.Helper()

	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("not JSON: %q", body)
	}
}

// A read with block_ms waits for the next append and gives it.
func TestStreamReadBlocksUntilAnAppend(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"$"}`), http.StatusCreated)

	got := make(chan string, 1)
	started := time.Now()

	go func() {
		rec := call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","block_ms":5000}`)
		got <- rec.Body.String()
	}()

	time.Sleep(150 * time.Millisecond)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1}}`), http.StatusCreated)

	select {
	case body := <-got:
		if !strings.Contains(body, `"n":1`) {
			t.Fatalf("the blocked read gave %q", body)
		}

		if waited := time.Since(started); waited > 2*time.Second {
			t.Fatalf("the read waited %s: the append did not wake it", waited)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the blocked read did not end after the append")
	}
}

// A read with block_ms and no append ends at the deadline with no rows.
func TestStreamReadBlockTimesOut(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"$"}`), http.StatusCreated)

	began := time.Now()
	rec := call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","block_ms":200}`)

	expectStatus(t, rec, http.StatusOK)

	if rec.Body.Len() != 0 || time.Since(began) < 200*time.Millisecond {
		t.Fatalf("body %q after %s", rec.Body.String(), time.Since(began))
	}
}

// After a restart, the IDs go on after the last one, the group keeps its
// PEL and its position, and entries_added is right.
func TestStreamSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	h, stop := openAt(t, dir)

	for range 3 {
		expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1}}`), http.StatusCreated)
	}

	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`), http.StatusCreated)
	expectStatus(t, call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","count":2}`), http.StatusOK)

	var before struct {
		LastID string `json:"last_id"`
	}

	jsonOf(t, call(h, "GET", "/jobs/_streams", "").Body.String(), &before)

	stop()

	h, stop = openAt(t, dir)

	defer stop()

	rec := call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":2}}`)

	expectStatus(t, rec, http.StatusCreated)

	var appended struct {
		ID string `json:"_id"`
	}

	jsonOf(t, rec.Body.String(), &appended)

	if appended.ID <= before.LastID && len(appended.ID) <= len(before.LastID) {
		t.Fatalf("ID after restart %s is not after %s", appended.ID, before.LastID)
	}

	var info struct {
		EntriesAdded int `json:"entries_added"`
		Length       int `json:"length"`
	}

	jsonOf(t, call(h, "GET", "/jobs/_streams", "").Body.String(), &info)

	if info.EntriesAdded != 4 || info.Length != 4 {
		t.Fatalf("after restart: entries_added %d, length %d; want 4, 4", info.EntriesAdded, info.Length)
	}

	var sum struct {
		Count int `json:"count"`
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/groups/g/pending", `{}`).Body.String(), &sum)

	if sum.Count != 2 {
		t.Fatalf("pending after restart %d, want 2", sum.Count)
	}

	rows := call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","count":10}`).Body.String()

	if n := strings.Count(rows, "\n"); n != 2 {
		t.Fatalf("the group read %d new entries after restart, want 2 (one old, one new): %q", n, rows)
	}
}

// A truncate empties the stream and removes its groups.
func TestStreamTruncateRemovesGroups(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1}}`), http.StatusCreated)
	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`), http.StatusCreated)

	expectStatus(t, call(h, "POST", "/jobs/_truncate?confirm=nope", ""), http.StatusBadRequest)

	if rec := call(h, "GET", "/jobs/_streams/groups", ""); !strings.Contains(rec.Body.String(), `"g"`) {
		t.Fatalf("a refused truncate removed the groups: %s", rec.Body.String())
	}

	expectStatus(t, call(h, "POST", "/jobs/_truncate?confirm=jobs", ""), http.StatusOK)

	if rec := call(h, "GET", "/jobs/_streams/groups", ""); strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("groups after truncate: %s", rec.Body.String())
	}

	if got := errorCode(t, call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c"}`)); got != "unknown_group" {
		t.Fatalf("read after truncate: %s", got)
	}
}

// Appends from many goroutines get unique IDs, and the stream gives them in
// ID order.
func TestStreamConcurrentAppends(t *testing.T) {
	_, h := newHandler(t)

	var wg sync.WaitGroup

	for range 20 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 10 {
				call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1}}`)
			}
		}()
	}

	wg.Wait()

	rec := call(h, "POST", "/jobs/_search", `{"sort":{"_id":1},"limit":1000,"fields":["_id"]}`)
	seen := map[string]bool{}

	for line := range strings.Lines(rec.Body.String()) {
		var r struct {
			ID string `json:"_id"`
		}

		jsonOf(t, line, &r)

		if seen[r.ID] {
			t.Fatalf("ID %s twice", r.ID)
		}

		seen[r.ID] = true
	}

	if len(seen) != 200 {
		t.Fatalf("%d entries, want 200", len(seen))
	}
}

// The routes of every collection take and give IDs as <ms>-<seq>.
func TestStreamDocRoutesAndRanges(t *testing.T) {
	_, h := newHandler(t)

	for _, id := range []string{"7-1", "7-2", "8-0"} {
		expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1},"id":"`+id+`"}`), http.StatusCreated)
	}

	rec := call(h, "GET", "/jobs/_doc/7-2", "")

	expectStatus(t, rec, http.StatusOK)

	if !strings.Contains(rec.Body.String(), `"_id":"7-2"`) {
		t.Fatalf("get gave %s", rec.Body.String())
	}

	if got := strings.Count(call(h, "POST", "/jobs/_search", `{"query":{"_id":{"$gte":"-","$lte":"7"}}}`).Body.String(), "\n"); got != 2 {
		t.Fatalf("- to 7 gave %d entries, want 2 (7 as an end is 7-<max>)", got)
	}

	if got := strings.Count(call(h, "POST", "/jobs/_search", `{"query":{"_id":{"$gt":"7-1","$lte":"+"}}}`).Body.String(), "\n"); got != 2 {
		t.Fatalf("(7-1 to + gave %d entries, want 2", got)
	}

	expectStatus(t, call(h, "DELETE", "/jobs/_doc/8-0", ""), http.StatusOK)

	var info struct {
		MaxDeletedID string `json:"max_deleted_id"`
		LastID       string `json:"last_id"`
	}

	jsonOf(t, call(h, "GET", "/jobs/_streams", "").Body.String(), &info)

	if info.MaxDeletedID != "8-0" || info.LastID != "8-0" {
		t.Fatalf("after deleting the last entry: %+v; the last ID must not go back", info)
	}

	if got := errorCode(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1},"id":"8-0"}`)); got != "stream_id_too_small" {
		t.Fatalf("append of a deleted last ID: %s", got)
	}
}

func TestStreamMetrics(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"docs":[{"n":1},{"n":2}]}`), http.StatusCreated)
	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`), http.StatusCreated)
	expectStatus(t, call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c","count":1}`), http.StatusOK)

	page := call(h, "GET", "/_metrics", "").Body.String()

	for _, want := range []string{
		`ndb_stream_entries_added_total{stream="jobs"} 2`,
		`ndb_stream_group_pending{stream="jobs",group="g"} 1`,
		`ndb_stream_group_lag{stream="jobs",group="g"} 1`,
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("/_metrics has no %q:\n%s", want, page)
		}
	}
}

// A backup keeps the groups of a stream: after a restore, the group has its
// position and its pending entries.
func TestStreamGroupsSurviveBackupAndRestore(t *testing.T) {
	c := unixServer(t)

	do(t, c, "POST", "/jobs/_streams/append", `{"docs":[{"n":1},{"n":2},{"n":3}]}`, nil)
	do(t, c, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`, nil)
	do(t, c, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","count":2}`, nil)

	resp, body := do(t, c, "POST", "/_backup", `{}`, nil)

	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `{"_section":"stream","collection":"jobs"`) {
		t.Fatalf("backup %d has no stream section", resp.StatusCode)
	}

	stage := filepath.Join(t.TempDir(), "restored")

	if err := backup.Extract(strings.NewReader(body), stage); err != nil {
		t.Fatal(err)
	}

	h, stop := openAt(t, stage)

	defer stop()

	var sum struct {
		Count int `json:"count"`
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/groups/g/pending", `{}`).Body.String(), &sum)

	if sum.Count != 2 {
		t.Fatalf("pending after restore %d, want 2", sum.Count)
	}

	rows := call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","count":10}`).Body.String()

	if n := strings.Count(rows, "\n"); n != 1 {
		t.Fatalf("the group read %d new entries after restore, want 1: %q", n, rows)
	}
}

// An approximate trim deletes whole blocks of 1024 entries only: it keeps
// more than asked, never fewer.
func TestStreamApproxTrimDeletesWholeBlocks(t *testing.T) {
	_, h := newHandler(t)

	docs := strings.Repeat(`{"n":1},`, 2500)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"docs":[`+strings.TrimSuffix(docs, ",")+`]}`), http.StatusCreated)

	var res struct {
		Deleted int `json:"deleted"`
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/trim", `{"max_len":2000,"approx":true}`).Body.String(), &res)

	if res.Deleted != 0 {
		t.Fatalf("approx trim of 500 over max_len deleted %d, want 0 (less than a block)", res.Deleted)
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/trim", `{"max_len":100,"approx":true}`).Body.String(), &res)

	if res.Deleted != 2048 {
		t.Fatalf("approx trim of 2400 over max_len deleted %d, want 2048 (two blocks)", res.Deleted)
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/trim", `{"max_len":100}`).Body.String(), &res)

	if res.Deleted != 352 {
		t.Fatalf("exact trim deleted %d, want 352", res.Deleted)
	}
}

// A read without a group, from "$" with block_ms, waits for the next
// append and gives it; with no append it ends at the deadline.
func TestStreamTailBlocksUntilAnAppend(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1}}`), http.StatusCreated)

	began := time.Now()
	rec := call(h, "POST", "/jobs/_streams/read", `{"after":"$","block_ms":200}`)

	expectStatus(t, rec, http.StatusOK)

	if rec.Body.Len() != 0 || time.Since(began) < 200*time.Millisecond {
		t.Fatalf("body %q after %s", rec.Body.String(), time.Since(began))
	}

	got := make(chan string, 1)
	started := time.Now()

	go func() {
		rec := call(h, "POST", "/jobs/_streams/read", `{"after":"$","block_ms":5000}`)
		got <- rec.Body.String()
	}()

	time.Sleep(150 * time.Millisecond)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":2}}`), http.StatusCreated)

	select {
	case body := <-got:
		if !strings.Contains(body, `"n":2`) || strings.Contains(body, `"n":1`) {
			t.Fatalf("the blocked read gave %q", body)
		}

		if waited := time.Since(started); waited > 2*time.Second {
			t.Fatalf("the read waited %s: the append did not wake it", waited)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the blocked read did not end after the append")
	}
}

// The write routes of every collection are refused on a stream: they would
// add rows with an ID the groups cannot read.
func TestStreamRefusesTheWritesOfACollection(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1}}`), http.StatusCreated)

	writes := []struct{ method, path, body string }{
		{"POST", "/jobs/_doc", `{"n":2}`},
		{"POST", "/jobs/_bulk", `{"docs":[{"n":2}]}`},
		{"POST", "/jobs/_update_by_query", `{"query":{},"update":{"$set":{"n":3}}}`},
		{"POST", "/jobs/_delete_by_query", `{"query":{}}`},
		{"POST", "/jobs/_update/1-0", `{"update":{"$set":{"n":3}}}`},
		{"POST", "/jobs/_expire/1-0", `{"in":"1h"}`},
		{"POST", "/jobs/_persist/1-0", ``},
	}

	for _, w := range writes {
		rec := call(h, w.method, w.path, w.body)

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "stream_append_only") {
			t.Errorf("%s %s: %d %s", w.method, w.path, rec.Code, rec.Body.String())
		}
	}
}

// A group or a consumer may have the name of a route segment.
func TestStreamGroupNamedLikeASegment(t *testing.T) {
	_, h := newHandler(t)

	for _, g := range []string{"groups", "consumers", "read", "pending"} {
		expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/"+g, `{"start":"0"}`), http.StatusCreated)
		expectStatus(t, call(h, "POST", "/jobs/_streams/groups/"+g+"/read", `{"consumer":"groups"}`), http.StatusOK)
		expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/"+g+"/consumers/consumers", ``), http.StatusOK)
	}
}

// A truncate that runs while requests use the stream leaves no group: no
// request opens the old group store while the truncate holds the stream.
// The requests in flight never fail with an internal error.
func TestStreamTruncateDuringRequestsKeepsNoGroup(t *testing.T) {
	_, h := newHandler(t)

	for round := range 30 {
		expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1}}`), http.StatusCreated)

		if rec := call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`); rec.Code != http.StatusCreated && rec.Code != http.StatusConflict {
			t.Fatalf("round %d: create group: %d %s", round, rec.Code, rec.Body.String())
		}

		var (
			stop   atomic.Bool
			wg     sync.WaitGroup
			failed atomic.Value
		)

		requests := []struct{ method, path, body string }{
			{"GET", "/jobs/_streams/groups", ""},
			{"POST", "/jobs/_streams/append", `{"doc":{"n":2}}`},
			{"POST", "/jobs/_streams/groups/g/ack", `{"ids":["1-0"]}`},
		}

		for _, req := range requests {
			wg.Add(1)

			go func() {
				defer wg.Done()

				for !stop.Load() {
					if rec := call(h, req.method, req.path, req.body); rec.Code >= 500 {
						failed.Store(req.path + ": " + rec.Body.String())
					}
				}
			}()
		}

		expectStatus(t, call(h, "POST", "/jobs/_truncate?confirm=jobs", ""), http.StatusOK)

		stop.Store(true)
		wg.Wait()

		if msg := failed.Load(); msg != nil {
			t.Fatalf("round %d: a request failed during the truncate: %v", round, msg)
		}

		if body := call(h, "GET", "/jobs/_streams/groups", "").Body.String(); strings.Contains(body, `"name":"g"`) {
			t.Fatalf("round %d: group g survived the truncate: %s", round, body)
		}
	}
}

// A trim by min_id keeps to limit, as a trim by max_len does; limit needs
// approx, as LIMIT needs ~ in Redis.
func TestStreamMinIDTrimKeepsToTheLimit(t *testing.T) {
	_, h := newHandler(t)

	docs := strings.Repeat(`{"n":1},`, 3000)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"docs":[`+strings.TrimSuffix(docs, ",")+`]}`), http.StatusCreated)

	var res struct {
		Deleted int `json:"deleted"`
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/trim", `{"min_id":"+","approx":true,"limit":10}`).Body.String(), &res)

	if res.Deleted != 10 {
		t.Fatalf("min_id trim with limit 10 deleted %d", res.Deleted)
	}

	if got := errorCode(t, call(h, "POST", "/jobs/_streams/trim", `{"min_id":"+","limit":10}`)); got != "invalid_request" {
		t.Fatalf("limit without approx: %s", got)
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/trim", `{"min_id":"+","approx":true}`).Body.String(), &res)

	if res.Deleted != 2048 {
		t.Fatalf("approx min_id trim of 2990 entries deleted %d, want 2048 (two blocks)", res.Deleted)
	}
}

// _aggregate on a stream matches and gives the entry ID <ms>-<seq>. A
// pattern that matches a stream and a lookup into a stream are refused:
// the _id on disk is padded.
func TestStreamAggregateAndPatternReads(t *testing.T) {
	_, h := newHandler(t)

	for _, id := range []string{"5-1", "5-2", "6-0"} {
		expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1},"id":"`+id+`"}`), http.StatusCreated)
	}

	rec := call(h, "POST", "/jobs/_aggregate", `{"pipeline":[{"$match":{"_id":"5-1"}}]}`)

	if body := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || !strings.HasPrefix(body, `{"_id":"5-1"`) || strings.Count(body, "\n") != 0 {
		t.Fatalf("$match on 5-1: %d %q", rec.Code, body)
	}

	rec = call(h, "POST", "/jobs/_aggregate", `{"pipeline":[{"$sort":{"_id":-1}},{"$match":{"_id":{"$gt":"5"}}},{"$limit":5}]}`)

	if body := rec.Body.String(); strings.Count(body, "\n") != 1 || !strings.Contains(body, `"_id":"6-0"`) {
		t.Fatalf("$match on _id $gt 5 (all of ms 5): %q", body)
	}

	rec = call(h, "POST", "/jobs/_aggregate", `{"pipeline":[{"$group":{"_id":"$n","count":{"$sum":1}}},{"$match":{"_id":1}}]}`)

	if body := rec.Body.String(); !strings.Contains(body, `"count":3`) {
		t.Fatalf("$match after $group is on the group key: %d %q", rec.Code, body)
	}

	expectStatus(t, call(h, "POST", "/jobs-old/_doc", `{"n":1}`), http.StatusOK)

	for _, path := range []string{"/jobs*/_search", "/jobs*/_count", "/jobs*/_aggregate"} {
		body := `{}`

		if strings.HasSuffix(path, "_aggregate") {
			body = `{"pipeline":[{"$match":{}}]}`
		}

		if got := errorCode(t, call(h, "POST", path, body)); got != "invalid_request" {
			t.Fatalf("%s over a stream: %s", path, got)
		}
	}

	expectStatus(t, call(h, "POST", "/orders/_doc", `{"job_id":"5-1"}`), http.StatusOK)

	if got := errorCode(t, call(h, "POST", "/orders/_search", `{"lookup":{"job":{"belongs_to":"jobs","fk":"job_id"}}}`)); got != "invalid_request" {
		t.Fatalf("lookup into a stream: %s", got)
	}
}

// A watch of a stream gives the entry ID <ms>-<seq>, and its query matches
// it.
func TestStreamWatchGivesTheEntryID(t *testing.T) {
	env := newWatchEnv(t, t.TempDir(), time.Minute)

	for _, id := range []string{"5-1", "5-2"} {
		resp := env.post(context.Background(), "/jobs/_streams/append", `{"doc":{"n":1},"id":"`+id+`"}`)

		resp.Body.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())

	defer cancel()

	lines, resp := env.watch(ctx, "jobs", `{"consumer":"w","from":"beginning","query":{"_id":"5-2"}}`)

	defer resp.Body.Close()

	ev := lines.next()
	doc, _ := ev["doc"].(map[string]any)

	if ev["_id"] != "5-2" || doc["_id"] != "5-2" {
		t.Fatalf("watch event: %v", ev)
	}
}

// A pending read after the highest ID gives nothing: the ID after it does
// not wrap to 0-0.
func TestStreamPendingAfterTheHighestID(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1},"id":"5-1"}`), http.StatusCreated)
	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`), http.StatusCreated)
	expectStatus(t, call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c"}`), http.StatusOK)

	rec := call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c","pending":true,"after":"18446744073709551615-18446744073709551615"}`)

	expectStatus(t, rec, http.StatusOK)

	if rec.Body.Len() != 0 {
		t.Fatalf("pending after the highest ID: %q", rec.Body.String())
	}
}

// A read without a group, with block_ms, on a stream that does not exist
// waits for it (XREAD BLOCK on a missing key); with no append it ends at
// the deadline with no rows.
func TestStreamTailBlocksOnAMissingStream(t *testing.T) {
	_, h := newHandler(t)

	began := time.Now()
	rec := call(h, "POST", "/nope/_streams/read", `{"after":"$","block_ms":200}`)

	expectStatus(t, rec, http.StatusOK)

	if rec.Body.Len() != 0 || time.Since(began) < 200*time.Millisecond {
		t.Fatalf("body %q after %s", rec.Body.String(), time.Since(began))
	}

	expectStatus(t, call(h, "POST", "/nope/_streams/read", `{"after":"$"}`), http.StatusNotFound)

	got := make(chan string, 1)

	go func() {
		got <- call(h, "POST", "/jobs/_streams/read", `{"after":"$","block_ms":5000}`).Body.String()
	}()

	time.Sleep(150 * time.Millisecond)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":7}}`), http.StatusCreated)

	select {
	case body := <-got:
		if !strings.Contains(body, `"n":7`) {
			t.Fatalf("the blocked read gave %q", body)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("the blocked read did not end after the stream was made")
	}
}

// A nack survives a restart of the daemon: the entry waits with no owner,
// the restarted consumer does not get it in its pending read, and the
// metric counts it.
func TestStreamNackSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	h, stop := openAt(t, dir)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1},"id":"5-0"}`), http.StatusCreated)
	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":2},"id":"5-1"}`), http.StatusCreated)
	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`), http.StatusCreated)
	expectStatus(t, call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","count":10}`), http.StatusOK)

	rec := call(h, "POST", "/jobs/_streams/groups/g/nack", `{"consumer":"c1","ids":["5-0","5-1"],"delay_ms":3600000}`)

	if strings.TrimSpace(rec.Body.String()) != `{"nacked":2}` {
		t.Fatalf("nack: %d %s", rec.Code, rec.Body.String())
	}

	stop()

	h, stop = openAt(t, dir)

	defer stop()

	if rows := call(h, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","pending":true}`).Body.String(); strings.Contains(rows, "_id") {
		t.Fatalf("the pending read of c1 after restart gave %s", rows)
	}

	var list []struct {
		Consumer string `json:"consumer"`
		RetryAt  *int64 `json:"retry_at"`
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/groups/g/pending", `{"count":10}`).Body.String(), &list)

	if len(list) != 2 || list[0].Consumer != "" || list[0].RetryAt == nil || *list[0].RetryAt < time.Now().UnixMilli()+3_500_000 {
		t.Fatalf("pending after restart: %+v", list)
	}

	if body := call(h, "POST", "/jobs/_streams/groups/g/claim", `{"consumer":"c2","due":true}`).Body.String(); !strings.Contains(body, `"entries":[]`) {
		t.Fatalf("due claim before the retry time: %s", body)
	}

	if page := call(h, "GET", "/_metrics", "").Body.String(); !strings.Contains(page, `ndb_stream_group_waiting{stream="jobs",group="g"} 2`) {
		t.Fatalf("/_metrics has no waiting 2:\n%s", page)
	}
}

// A backup keeps the retry time of a nacked entry; a restore after that
// time makes it due at once.
func TestStreamNackSurvivesBackupAndRestore(t *testing.T) {
	c := unixServer(t)

	do(t, c, "POST", "/jobs/_streams/append", `{"doc":{"n":1},"id":"5-0"}`, nil)
	do(t, c, "POST", "/jobs/_streams/append", `{"doc":{"n":2},"id":"5-1"}`, nil)
	do(t, c, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`, nil)
	do(t, c, "POST", "/jobs/_streams/groups/g/read", `{"consumer":"c1","count":10}`, nil)

	if _, body := do(t, c, "POST", "/jobs/_streams/groups/g/nack", `{"consumer":"c1","ids":["5-0"],"delay_ms":200}`, nil); !strings.Contains(body, `"nacked":1`) {
		t.Fatalf("nack: %s", body)
	}

	resp, body := do(t, c, "POST", "/_backup", `{}`, nil)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("backup %d", resp.StatusCode)
	}

	stage := filepath.Join(t.TempDir(), "restored")

	if err := backup.Extract(strings.NewReader(body), stage); err != nil {
		t.Fatal(err)
	}

	time.Sleep(250 * time.Millisecond)

	h, stop := openAt(t, stage)

	defer stop()

	got := call(h, "POST", "/jobs/_streams/groups/g/claim", `{"consumer":"c2","due":true,"min_idle_ms":3600000,"just_ids":true}`).Body.String()

	if !strings.Contains(got, `"entries":["5-0"]`) {
		t.Fatalf("due claim after restore: %s", got)
	}

	var list []struct {
		ID       string `json:"_id"`
		Consumer string `json:"consumer"`
	}

	jsonOf(t, call(h, "POST", "/jobs/_streams/groups/g/pending", `{"count":10}`).Body.String(), &list)

	if len(list) != 2 || list[0].Consumer != "c2" || list[1].Consumer != "c1" {
		t.Fatalf("pending after restore and claim: %+v", list)
	}
}

// A truncate, a drop or a rename can close the store while nacks run:
// each nack opens the stream again and none fails with a closed store.
func TestStreamNackRacesATruncate(t *testing.T) {
	_, h := newHandler(t)

	expectStatus(t, call(h, "POST", "/jobs/_streams/append", `{"doc":{"n":1},"id":"5-0"}`), http.StatusCreated)
	expectStatus(t, call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`), http.StatusCreated)

	var wg sync.WaitGroup

	var bad atomic.Int64

	for range 4 {
		wg.Go(func() {
			for range 50 {
				rec := call(h, "POST", "/jobs/_streams/groups/g/nack", `{"consumer":"c1","ids":["5-0"],"delay_ms":0}`)

				if rec.Code != http.StatusOK && errorCode(t, rec) != "unknown_group" {
					bad.Add(1)
				}
			}
		})
	}

	wg.Go(func() {
		for range 5 {
			call(h, "POST", "/jobs/_truncate", "")
			call(h, "PUT", "/jobs/_streams/groups/g", `{"start":"0"}`)
		}
	})

	wg.Wait()

	if n := bad.Load(); n != 0 {
		t.Fatalf("%d nacks failed with an error other than unknown_group", n)
	}
}
