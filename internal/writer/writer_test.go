package writer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

func put(id string) []wal.Op {
	return []wal.Op{{Kind: wal.Put, ID: id, V: 1, Doc: json.RawMessage(`{"x":1}`)}}
}

type fatalSpy struct {
	mu  sync.Mutex
	err error
}

func (s *fatalSpy) fn(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.err = err
}

func (s *fatalSpy) get() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.err
}

func open(t *testing.T, cfg writer.Config) (*writer.Writer, *fatalSpy) {
	t.Helper()

	spy := &fatalSpy{}

	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}

	if cfg.FS == nil {
		cfg.FS = fs.OS{}
	}

	cfg.OnFatal = spy.fn

	w, err := writer.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { w.Close() })

	return w, spy
}

// walRecords decodes every line of every segment in dir, in order.
func walRecords(t *testing.T, dir string) []wal.Record {
	t.Helper()

	files, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))

	var recs []wal.Record

	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}

		lr := wal.NewLineReader(f, 0, 1<<20)

		for {
			line, _, err := lr.Next()
			if err != nil {
				break
			}

			rec, err := wal.Decode(line)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}

			recs = append(recs, rec)
		}

		f.Close()
	}

	return recs
}

func TestCommitStrictWritesDurableLines(t *testing.T) {
	dir := t.TempDir()
	w, _ := open(t, writer.Config{Dir: dir})

	for i := 1; i <= 3; i++ {
		tx, _, err := w.Commit(context.Background(), put(fmt.Sprint("id", i)))
		if err != nil {
			t.Fatal(err)
		}

		if tx != uint64(i) {
			t.Fatalf("tx = %d, want %d", tx, i)
		}
	}

	recs := walRecords(t, dir)

	if len(recs) != 3 || recs[2].Ops[0].ID != "id3" {
		t.Fatalf("got %+v", recs)
	}
}

func TestGroupCommitUsesFewerSyncs(t *testing.T) {
	dir := t.TempDir()
	ffs := fs.NewFault(fs.OS{})
	w, _ := open(t, writer.Config{Dir: dir, FS: ffs})

	const n = 500

	var wg sync.WaitGroup

	seen := sync.Map{}

	for i := range n {
		wg.Go(func() {
			tx, _, err := w.Commit(context.Background(), put(fmt.Sprint("id", i)))
			if err != nil {
				t.Error(err)

				return
			}

			if _, dup := seen.LoadOrStore(tx, true); dup {
				t.Errorf("duplicate tx %d", tx)
			}
		})
	}

	wg.Wait()

	if got := len(walRecords(t, dir)); got != n {
		t.Fatalf("%d records, want %d", got, n)
	}

	if syncs := ffs.Calls(fs.OpSync); syncs >= n {
		t.Fatalf("%d syncs for %d commits: no group commit", syncs, n)
	}
}

func TestSyncFailureIsFatal(t *testing.T) {
	ffs := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpSync, At: 1})
	w, spy := open(t, writer.Config{FS: ffs})

	_, _, err := w.Commit(context.Background(), put("a"))

	if err == nil {
		t.Fatal("commit returned no error after a failed sync")
	}

	if !errors.Is(spy.get(), fs.ErrInjected) {
		t.Fatalf("OnFatal got %v", spy.get())
	}

	if _, _, err := w.Commit(context.Background(), put("b")); !errors.Is(err, writer.ErrClosed) {
		t.Fatalf("writer still accepts commits: %v", err)
	}
}

func TestPartialWriteIsFatal(t *testing.T) {
	ffs := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpWrite, At: 1, Partial: 10})
	w, spy := open(t, writer.Config{FS: ffs})

	if _, _, err := w.Commit(context.Background(), put("a")); err == nil {
		t.Fatal("want error")
	}

	if spy.get() == nil {
		t.Fatal("OnFatal not called")
	}
}

func TestLineTooLargeRejectsOnlyThatCommit(t *testing.T) {
	dir := t.TempDir()
	w, spy := open(t, writer.Config{Dir: dir, MaxLine: 200})

	big := []wal.Op{{Kind: wal.Put, ID: "big", V: 1, Doc: json.RawMessage(fmt.Sprintf(`{"x":"%0300d"}`, 0))}}

	if _, _, err := w.Commit(context.Background(), big); !errors.Is(err, wal.ErrLineTooLarge) {
		t.Fatalf("want ErrLineTooLarge, got %v", err)
	}

	if _, _, err := w.Commit(context.Background(), put("ok")); err != nil {
		t.Fatal(err)
	}

	if spy.get() != nil || len(walRecords(t, dir)) != 1 {
		t.Fatal("large line affected other commits")
	}
}

func TestPrepareRejectsOnlyThatCommit(t *testing.T) {
	dir := t.TempDir()
	errNo := errors.New("no")

	w, _ := open(t, writer.Config{Dir: dir, Prepare: func(req any) (writer.Prepared, error) {
		ops := req.([]wal.Op)

		if ops[0].ID == "bad" {
			return writer.Prepared{}, errNo
		}

		return writer.Prepared{Ops: ops}, nil
	}})

	if _, _, err := w.Commit(context.Background(), put("bad")); !errors.Is(err, errNo) {
		t.Fatalf("got %v", err)
	}

	tx, _, err := w.Commit(context.Background(), put("good"))
	if err != nil || tx != 1 {
		t.Fatalf("tx=%d err=%v: a rejected commit must not use a tx", tx, err)
	}
}

func TestAppliedRunsInCommitOrder(t *testing.T) {
	var got []uint64

	w, _ := open(t, writer.Config{Applied: func(rec wal.Record) { got = append(got, rec.Tx) }})

	for i := range 5 {
		if _, _, err := w.Commit(context.Background(), put(fmt.Sprint(i))); err != nil {
			t.Fatal(err)
		}
	}

	w.Close()

	for i, tx := range got {
		if tx != uint64(i+1) {
			t.Fatalf("applied order %v", got)
		}
	}
}

func TestRotationCreatesSegmentsAndSyncsDir(t *testing.T) {
	dir := t.TempDir()
	ffs := fs.NewFault(fs.OS{})
	w, _ := open(t, writer.Config{Dir: dir, FS: ffs, SegmentSize: 300})

	for i := range 10 {
		if _, _, err := w.Commit(context.Background(), put(fmt.Sprint("id", i))); err != nil {
			t.Fatal(err)
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))

	if len(files) < 3 {
		t.Fatalf("%d segments, want ≥ 3", len(files))
	}

	if ffs.Calls(fs.OpSyncDir) < len(files) {
		t.Fatalf("SyncDir %d times for %d files", ffs.Calls(fs.OpSyncDir), len(files))
	}

	if got := len(walRecords(t, dir)); got != 10 {
		t.Fatalf("%d records across segments, want 10", got)
	}
}

func TestRotationFailureIsFatal(t *testing.T) {
	ffs := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpSyncDir, At: 2})
	w, spy := open(t, writer.Config{FS: ffs, SegmentSize: 300})

	var err error

	for i := 0; i < 10 && err == nil; i++ {
		_, _, err = w.Commit(context.Background(), put(fmt.Sprint("id", i)))
	}

	if err == nil || spy.get() == nil {
		t.Fatalf("rotation failure was not fatal: err=%v fatal=%v", err, spy.get())
	}

	if _, _, err := w.Commit(context.Background(), put("after")); !errors.Is(err, writer.ErrClosed) {
		t.Fatalf("writer still accepts commits: %v", err)
	}
}

func TestRelaxedSyncsInBackground(t *testing.T) {
	ffs := fs.NewFault(fs.OS{})
	w, _ := open(t, writer.Config{FS: ffs, Durability: writer.Relaxed, FlushEvery: 20 * time.Millisecond})

	if _, _, err := w.Commit(context.Background(), put("a")); err != nil {
		t.Fatal(err)
	}

	before := ffs.Calls(fs.OpSync)

	deadline := time.Now().Add(time.Second)

	for ffs.Calls(fs.OpSync) == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if ffs.Calls(fs.OpSync) == before {
		t.Fatal("no background sync")
	}
}

func TestCloseRejectsNewCommits(t *testing.T) {
	w, _ := open(t, writer.Config{})

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if _, _, err := w.Commit(context.Background(), put("a")); !errors.Is(err, writer.ErrClosed) {
		t.Fatalf("got %v", err)
	}
}

func TestContextCancelWhileWaiting(t *testing.T) {
	var block atomic.Bool

	block.Store(true)

	w, _ := open(t, writer.Config{Prepare: func(req any) (writer.Prepared, error) {
		for block.Load() {
			time.Sleep(time.Millisecond)
		}

		return writer.Prepared{Ops: req.([]wal.Op)}, nil
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, _, err := w.Commit(ctx, put("a"))

	block.Store(false)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestStageRunsOnlyForAcceptedRecords(t *testing.T) {
	var staged []string

	w, _ := open(t, writer.Config{MaxLine: 200, Prepare: func(req any) (writer.Prepared, error) {
		ops := req.([]wal.Op)

		return writer.Prepared{Ops: ops, Stage: func() { staged = append(staged, ops[0].ID) }}, nil
	}})

	big := []wal.Op{{Kind: wal.Put, ID: "big", V: 1, Doc: json.RawMessage(fmt.Sprintf(`{"x":"%0300d"}`, 0))}}

	w.Commit(context.Background(), big)
	w.Commit(context.Background(), put("ok"))
	w.Close()

	if fmt.Sprint(staged) != "[ok]" {
		t.Fatalf("staged = %v, want [ok]", staged)
	}
}
