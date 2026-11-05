package tailer_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/tailer"
)

// env is one data folder, one collection dir and the memtable they feed.
type env struct {
	t    *testing.T
	data string
	col  string
	def  catalog.Def
	d    *collection.Direct
}

func newEnv(t *testing.T, glob, id string) *env {
	t.Helper()

	data := t.TempDir()

	def := catalog.Def{
		Name:        "calls",
		ID:          id,
		Write:       catalog.Direct,
		Path:        filepath.Join(data, glob),
		MaxLineSize: 1024,
	}

	return &env{
		t:    t,
		data: data,
		col:  t.TempDir(),
		def:  def,
		d:    collection.NewDirect(collection.DirectConfig{Def: def}),
	}
}

func (e *env) open(mod func(*tailer.Config)) *tailer.Tailer {
	e.t.Helper()

	cfg := tailer.Config{Dir: e.col, Def: e.def, Sink: e.d, Poll: 50 * time.Millisecond}

	if mod != nil {
		mod(&cfg)
	}

	tl, err := tailer.Open(cfg)
	if err != nil {
		e.t.Fatal(err)
	}

	e.t.Cleanup(func() { tl.Close() })

	return tl
}

func (e *env) path(name string) string {
	return filepath.Join(e.data, name)
}

func (e *env) write(name, content string) {
	e.t.Helper()

	p := e.path(name)

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		e.t.Fatal(err)
	}

	defer f.Close()

	if _, err := f.WriteString(content); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) has(id string) bool {
	_, ok := e.d.Find(id)

	return ok
}

func syncOnce(t *testing.T, tl *tailer.Tailer) {
	t.Helper()

	if err := tl.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, within time.Duration, cond func() bool, msg string) time.Duration {
	t.Helper()

	start := time.Now()

	for time.Since(start) < within {
		if cond() {
			return time.Since(start)
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatalf("timeout: %s", msg)

	return 0
}

func run(t *testing.T, tl *tailer.Tailer) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = tl.Run(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func line(id string) string {
	return fmt.Sprintf(`{"id":%q}`+"\n", id)
}

// countSink counts the lines the sink accepts.
type sink interface {
	Apply(file string, off int64, line []byte) error
}

type countSink struct {
	inner sink
	mu    sync.Mutex
	n     int
}

func (c *countSink) Apply(file string, off int64, l []byte) error {
	if err := c.inner.Apply(file, off, l); err != nil {
		return err
	}

	c.mu.Lock()
	c.n++
	c.mu.Unlock()

	return nil
}

func (c *countSink) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.n
}

func TestDiscoveryAtStartNewFileNewFolder(t *testing.T) {
	e := newEnv(t, "**/*.ndjson", "id")

	e.write("a.ndjson", line("a1"))
	e.write("sub/deep/b.ndjson", line("b1"))
	e.write("skip.txt", line("nope"))

	tl := e.open(nil)
	syncOnce(t, tl)

	if !e.has("a1") || !e.has("b1") || e.has("nope") {
		t.Fatalf("start scan: len = %d", e.d.Len())
	}

	e.write("c.ndjson", line("c1"))
	e.write("newdir/x/d.ndjson", line("d1"))
	syncOnce(t, tl)

	if !e.has("c1") || !e.has("d1") {
		t.Fatalf("new file/folder: len = %d", e.d.Len())
	}
}

func TestSingleStarDoesNotDescend(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")

	e.write("a.ndjson", line("a1"))
	e.write("sub/b.ndjson", line("b1"))

	syncOnce(t, e.open(nil))

	if !e.has("a1") || e.has("b1") {
		t.Fatalf("len = %d", e.d.Len())
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"/d/**/*.ndjson", "/d/a.ndjson", true},
		{"/d/**/*.ndjson", "/d/x/y/a.ndjson", true},
		{"/d/**/*.ndjson", "/d/x/a.txt", false},
		{"/d/*.ndjson", "/d/x/a.ndjson", false},
		{"/d/calls-*.ndjson", "/d/calls-h1-42.ndjson", true},
		{"/d/**", "/d/x/y", true},
	}

	for _, c := range cases {
		if got := tailer.GlobMatch(c.pat, c.path); got != c.want {
			t.Errorf("%s ~ %s = %v", c.pat, c.path, got)
		}
	}
}

func TestWatchLatencyUnder100ms(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")

	e.write("a.ndjson", line("a0"))
	run(t, e.open(func(c *tailer.Config) { c.Poll = time.Hour }))
	eventually(t, 2*time.Second, func() bool { return e.has("a0") }, "initial line")

	time.Sleep(50 * time.Millisecond)

	e.write("a.ndjson", line("a1"))
	took := eventually(t, 2*time.Second, func() bool { return e.has("a1") }, "appended line")

	if took > 100*time.Millisecond {
		t.Fatalf("latency %s", took)
	}

	e.write("new.ndjson", line("n1"))
	took = eventually(t, 2*time.Second, func() bool { return e.has("n1") }, "new file")

	if took > 100*time.Millisecond {
		t.Fatalf("new file latency %s", took)
	}
}

func TestPollFallbackWithoutWatcher(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")

	run(t, e.open(func(c *tailer.Config) { c.NoWatch = true }))

	e.write("a.ndjson", line("a1"))
	eventually(t, 2*time.Second, func() bool { return e.has("a1") }, "poll must find the file")

	e.write("a.ndjson", line("a2"))
	eventually(t, 2*time.Second, func() bool { return e.has("a2") }, "poll must find the line")
}

func TestRestartResumesFromCursor(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	cs := &countSink{inner: e.d}

	e.write("a.ndjson", line("1")+line("2")+line("3"))

	tl := e.open(func(c *tailer.Config) { c.Sink = cs })
	syncOnce(t, tl)
	tl.Close()

	e.write("a.ndjson", line("4")+line("5"))

	tl2 := e.open(func(c *tailer.Config) { c.Sink = cs })
	syncOnce(t, tl2)

	if cs.count() != 5 || e.d.Len() != 5 {
		t.Fatalf("applied %d, len %d", cs.count(), e.d.Len())
	}
}

func TestRebuildReadsAgain(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	e.write("a.ndjson", line("1")+line("2"))

	tl := e.open(nil)
	syncOnce(t, tl)
	tl.Close()

	fresh := collection.NewDirect(collection.DirectConfig{Def: e.def})
	tl2 := e.open(func(c *tailer.Config) { c.Sink = fresh; c.Rebuild = true })
	syncOnce(t, tl2)

	if fresh.Len() != 2 {
		t.Fatalf("len = %d", fresh.Len())
	}
}

func TestPartialLineWaitsForNewline(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	e.write("a.ndjson", line("1")+`{"id":"2`)
	syncOnce(t, tl)

	if !e.has("1") || e.d.Len() != 1 {
		t.Fatalf("len = %d", e.d.Len())
	}

	e.write("a.ndjson", `x"}`+"\n")
	syncOnce(t, tl)

	if !e.has("2x") || e.d.Len() != 2 || tl.Quarantined() != 0 {
		t.Fatalf("len = %d, quarantined %d", e.d.Len(), tl.Quarantined())
	}
}

func TestRotationRenameAndNewFile(t *testing.T) {
	e := newEnv(t, "calls*.ndjson", "id")
	tl := e.open(nil)

	e.write("calls.ndjson", line("1"))
	syncOnce(t, tl)

	e.write("calls.ndjson", line("2"))

	if err := os.Rename(e.path("calls.ndjson"), e.path("calls.1.ndjson")); err != nil {
		t.Fatal(err)
	}

	e.write("calls.ndjson", line("3"))
	e.write("calls.1.ndjson", line("2b"))
	syncOnce(t, tl)

	for _, id := range []string{"1", "2", "2b", "3"} {
		if !e.has(id) {
			t.Errorf("missing %s", id)
		}
	}

	if e.d.Len() != 4 {
		t.Fatalf("len = %d", e.d.Len())
	}
}

func TestRotationOutOfGlobDrainsOldInode(t *testing.T) {
	e := newEnv(t, "calls.ndjson", "id")
	tl := e.open(nil)

	e.write("calls.ndjson", line("1"))
	syncOnce(t, tl)

	e.write("calls.ndjson", line("2"))

	if err := os.Rename(e.path("calls.ndjson"), e.path("old.bak")); err != nil {
		t.Fatal(err)
	}

	e.write("calls.ndjson", line("3"))
	syncOnce(t, tl)

	if !e.has("2") || !e.has("3") || e.d.Len() != 3 {
		t.Fatalf("len = %d", e.d.Len())
	}
}

func TestTruncateToZero(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	e.write("a.ndjson", line("aaaa")+line("bbbb"))
	syncOnce(t, tl)

	if err := os.Truncate(e.path("a.ndjson"), 0); err != nil {
		t.Fatal(err)
	}

	e.write("a.ndjson", line("c"))
	syncOnce(t, tl)

	if !e.has("c") {
		t.Fatal("did not read again from 0")
	}
}

func TestCopyTruncateRegrowsPastOffset(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	e.write("a.ndjson", line("a")+line("b"))
	syncOnce(t, tl)

	if err := os.Truncate(e.path("a.ndjson"), 0); err != nil {
		t.Fatal(err)
	}

	e.write("a.ndjson", line("x1")+line("x2")+line("x3")+line("x4"))
	syncOnce(t, tl)

	for _, id := range []string{"x1", "x2", "x3", "x4"} {
		if !e.has(id) {
			t.Errorf("missing %s: the fingerprint did not catch the truncation", id)
		}
	}
}

func TestDeletedFileIsDrained(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	e.write("a.ndjson", line("1"))
	syncOnce(t, tl)

	e.write("a.ndjson", line("2"))

	if err := os.Remove(e.path("a.ndjson")); err != nil {
		t.Fatal(err)
	}

	syncOnce(t, tl)

	if !e.has("2") {
		t.Fatal("lost the last lines of a deleted file")
	}

	e.write("a.ndjson", line("3"))
	syncOnce(t, tl)

	if !e.has("3") || e.d.Len() != 3 {
		t.Fatalf("len = %d", e.d.Len())
	}
}

func TestLineSemanticsThroughTailer(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")

	e.write("a.ndjson", `{"id":"a","n":1}`+"\n"+`{"id":"a","n":2}`+"\n"+`{"id":"b"}`+"\n"+`{"id":"b","_deleted":true}`+"\n"+`{"_batch":[{"id":"c"},{"id":"d"}]}`+"\n")
	syncOnce(t, e.open(nil))

	got, _ := e.d.Find("a")

	var m map[string]any
	_ = json.Unmarshal(got.Doc, &m)

	if m["n"] != float64(2) || e.has("b") || !e.has("c") || !e.has("d") || e.d.Len() != 3 {
		t.Fatalf("len = %d, a = %s", e.d.Len(), got.Doc)
	}
}

func TestNoIDCollectionUsesFileOffset(t *testing.T) {
	e := newEnv(t, "*.ndjson", "")

	e.write("a.ndjson", `{"x":1}`+"\n"+`{"x":2}`+"\n")
	syncOnce(t, e.open(nil))

	if !e.has(e.path("a.ndjson")+":0") || !e.has(e.path("a.ndjson")+":8") {
		t.Fatalf("len = %d", e.d.Len())
	}
}

func TestQuarantine(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	big := `{"id":"big","pad":"` + strings.Repeat("x", 5000) + `"}`

	e.write("a.ndjson", line("ok1")+"{not json\n"+big+"\n"+`{"id":"w1"}{"id":"w2"}`+"\n"+line("ok2"))
	syncOnce(t, tl)

	if !e.has("ok1") || !e.has("ok2") || e.d.Len() != 2 {
		t.Fatalf("len = %d", e.d.Len())
	}

	if tl.Quarantined() != 3 {
		t.Fatalf("quarantined = %d", tl.Quarantined())
	}

	data, err := os.ReadFile(filepath.Join(e.col, "quarantine", "calls.ndjson"))
	if err != nil {
		t.Fatal(err)
	}

	rows := strings.Split(strings.TrimSpace(string(data)), "\n")

	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}

	var row struct {
		File   string `json:"file"`
		Offset int64  `json:"offset"`
		Reason string `json:"reason"`
		Line   string `json:"line"`
	}

	if err := json.Unmarshal([]byte(rows[0]), &row); err != nil {
		t.Fatal(err)
	}

	if row.File != e.path("a.ndjson") || row.Offset != int64(len(line("ok1"))) || row.Reason == "" || row.Line != "{not json" {
		t.Fatalf("row = %+v", row)
	}

	if err := json.Unmarshal([]byte(rows[1]), &row); err != nil {
		t.Fatal(err)
	}

	if len(row.Line) != 4096 || !strings.Contains(row.Reason, "max_line_size") {
		t.Fatalf("large line: len %d, reason %q", len(row.Line), row.Reason)
	}
}

func TestBackpressureWaitsAndDropsNothing(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	e.d.SetMaxBytes(200)

	var b strings.Builder

	for i := range 100 {
		b.WriteString(line(fmt.Sprintf("id%03d", i)))
	}

	e.write("a.ndjson", b.String())
	run(t, e.open(nil))

	time.Sleep(300 * time.Millisecond)

	stalled := e.d.Len()

	if stalled == 0 || stalled >= 100 {
		t.Fatalf("expected a stall, len = %d", stalled)
	}

	time.Sleep(200 * time.Millisecond)

	if e.d.Len() != stalled {
		t.Fatal("the tailer kept going while the memtable was full")
	}

	e.d.SetMaxBytes(0)
	eventually(t, 3*time.Second, func() bool { return e.d.Len() == 100 }, "all lines after room")

	for i := range 100 {
		if !e.has(fmt.Sprintf("id%03d", i)) {
			t.Fatalf("id%03d dropped", i)
		}
	}
}

func TestCursorsRecorded(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	e.write("a.ndjson", line("1")+line("2"))
	syncOnce(t, tl)
	tl.Close()

	cur, err := tailer.LoadCursors(e.col)
	if err != nil {
		t.Fatal(err)
	}

	if len(cur) != 1 {
		t.Fatalf("cursors = %v", cur)
	}

	for _, c := range cur {
		if c.Offset != int64(len(line("1")+line("2"))) || c.Inode == 0 || c.FPLen == 0 {
			t.Fatalf("cursor = %+v", c)
		}
	}
}

func TestLagIsTheUnreadTail(t *testing.T) {
	e := newEnv(t, "*.ndjson", "call_id")
	tl := e.open(nil)

	e.write("a.ndjson", `{"call_id":"1"}`+"\n"+`{"call_id":"2"`)

	if err := tl.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got, want := tl.Lag(), int64(len(`{"call_id":"2"`)); got != want {
		t.Fatalf("lag = %d, want %d (the partial line)", got, want)
	}

	e.write("a.ndjson", "}\n")

	if err := tl.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if tl.Lag() != 0 {
		t.Fatalf("lag = %d after the line completed", tl.Lag())
	}
}

func TestQuarantineRowsReturnsTheLatestWithoutDuplicates(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	e.write("a.ndjson", line("ok1")+"{bad one\n"+"{bad two\n"+line("ok2")+"{bad three\n")
	syncOnce(t, tl)

	rows, total, err := tl.QuarantineRows(2)
	if err != nil {
		t.Fatal(err)
	}

	if total != 3 || len(rows) != 2 {
		t.Fatalf("total = %d rows = %d", total, len(rows))
	}

	if rows[0].Line != "{bad two" || rows[1].Line != "{bad three" || rows[1].Reason == "" || rows[1].File != e.path("a.ndjson") {
		t.Fatalf("rows = %+v", rows)
	}

	if err := tl.Close(); err != nil {
		t.Fatal(err)
	}

	again := e.open(nil)

	syncOnce(t, again)

	if _, total, err = again.QuarantineRows(10); err != nil || total != 3 {
		t.Fatalf("after reopen: total = %d err = %v, want the same 3 lines once", total, err)
	}
}

func TestQuarantineRowsWithoutFileIsEmpty(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	tl := e.open(nil)

	rows, total, err := tl.QuarantineRows(10)
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("rows = %v total = %d err = %v", rows, total, err)
	}
}

func TestQuarantineFileKeepsOneEntryPerBadLineAcrossRebuilds(t *testing.T) {
	e := newEnv(t, "*.ndjson", "id")
	e.write("a.ndjson", line("ok1")+"{bad one\n"+line("ok2")+"{bad two\n")

	for run := 0; run < 3; run++ {
		fresh := collection.NewDirect(collection.DirectConfig{Def: e.def})
		tl := e.open(func(c *tailer.Config) { c.Sink = fresh; c.Rebuild = true })

		syncOnce(t, tl)

		if got := tl.Quarantined(); got != 2 {
			t.Fatalf("run %d: quarantined = %d, want 2 distinct lines", run, got)
		}

		if err := tl.Close(); err != nil {
			t.Fatal(err)
		}
	}

	data, err := os.ReadFile(filepath.Join(e.col, "quarantine", "calls.ndjson"))
	if err != nil {
		t.Fatal(err)
	}

	if n := strings.Count(string(data), "\n"); n != 2 {
		t.Fatalf("quarantine file has %d entries, want 2:\n%s", n, data)
	}
}
