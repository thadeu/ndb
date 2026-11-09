package server_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
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
