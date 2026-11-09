package engine_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
)

func asleep(t *testing.T, col *engine.Collection) bool {
	t.Helper()

	return col.Stats().Asleep
}

// An idle collection sleeps: Stats still tells its rows without opening it,
// and the next call opens it with every document.
func TestAnIdleCollectionSleepsAndWakesOnTheNextCall(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "calls", Indexes: []string{"n"}})

	insertRange(t, col, 0, 50)

	if n := db.SleepIdle(time.Hour); n != 0 {
		t.Fatalf("a collection used now slept (%d)", n)
	}

	if n := db.SleepIdle(0); n != 1 {
		t.Fatalf("slept %d collections, want 1", n)
	}

	st := col.Stats()

	if !st.Asleep || st.Rows != 50 || len(st.Indexes) != 1 || st.Segments == 0 {
		t.Fatalf("stats of a sleeping collection: %+v", st)
	}

	if _, ok := col.Get("p7"); !ok {
		t.Fatal("p7 is missing after the wake")
	}

	if asleep(t, col) {
		t.Fatal("a call did not wake the collection")
	}

	insertRange(t, col, 50, 60)

	if col.Stats().Rows != 60 {
		t.Fatalf("rows = %d after a write on the woken collection", col.Stats().Rows)
	}
}

// A collection with a watch consumer stays open: the stream lives in it.
func TestAWatchKeepsTheCollectionOpen(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "outbox"})

	insertRange(t, col, 0, 3)

	stream, err := db.Watch("outbox", "worker", "")
	if err != nil {
		t.Fatal(err)
	}

	if n := db.SleepIdle(0); n != 0 {
		t.Fatal("a collection with a watch slept")
	}

	stream.Close()

	if n := db.SleepIdle(0); n != 1 {
		t.Fatal("the collection did not sleep after the watch closed")
	}
}

// Truncate, drop and rename work on a sleeping collection.
func TestSwapsOfASleepingCollection(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	for _, name := range []string{"a", "b", "c"} {
		insertRange(t, ensure(t, db, catalog.Def{Name: name}), 0, 10)
	}

	db.SleepIdle(0)

	if err := db.Truncate("a", "a"); err != nil {
		t.Fatal(err)
	}

	if a, _ := db.Get("a"); a.Stats().Rows != 0 || a.Stats().Asleep {
		t.Fatalf("truncate of a sleeping collection: %+v", a.Stats())
	}

	if err := db.Drop("b", "b"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Get("b"); err == nil {
		t.Fatal("b is still there after the drop")
	}

	if err := db.Rename("c", "d", false); err != nil {
		t.Fatal(err)
	}

	d, err := db.Get("d")
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := d.Get("p3"); !ok {
		t.Fatal("the rename of a sleeping collection lost p3")
	}
}

// The daily PITR check does not wake a collection that was not written
// since its newest snapshot, and wakes one that was.
func TestPITRWakesOnlyACollectionWithNewWrites(t *testing.T) {
	dir := t.TempDir()
	db := open(t, pitrCfg(dir))
	quiet := ensure(t, db, catalog.Def{Name: "quiet"})
	busy := ensure(t, db, catalog.Def{Name: "busy"})
	now := time.Now()

	insertRange(t, quiet, 0, 5)
	insertRange(t, busy, 0, 5)

	if err := db.SnapshotPITR(now); err != nil {
		t.Fatal(err)
	}

	insertRange(t, busy, 5, 10)
	db.SleepIdle(0)

	if err := db.SnapshotPITR(now.Add(25 * time.Hour)); err != nil {
		t.Fatal(err)
	}

	if !asleep(t, quiet) {
		t.Fatal("the PITR check woke a collection with no new writes")
	}

	snaps, _ := os.ReadDir(filepath.Join(dir, "collections", "busy", "pitr"))

	if len(snaps) != 2 {
		t.Fatalf("busy has %d snapshots, want a new one for its writes", len(snaps))
	}
}

// Writes and reads that race with sleeps and wakes lose nothing, and a
// restart finds every acked write.
func TestSleepsRacingWithWritesLoseNothing(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	const cols, writes = 4, 200

	for i := range cols {
		ensure(t, db, catalog.Def{Name: fmt.Sprintf("c%d", i)})
	}

	stop := make(chan struct{})
	slept := make(chan int)

	go func() {
		n := 0

		for {
			select {
			case <-stop:
				slept <- n

				return
			default:
				n += db.SleepIdle(0)
			}
		}
	}()

	var wg sync.WaitGroup

	for i := range cols {
		wg.Add(1)

		go func() {
			defer wg.Done()

			col, _ := db.Get(fmt.Sprintf("c%d", i))

			for j := range writes {
				if _, err := col.Insert(ctx, fmt.Appendf(nil, `{"_id":"d%d"}`, j)); err != nil {
					t.Error(err)

					return
				}

				if _, ok := col.Get(fmt.Sprintf("d%d", j)); !ok {
					t.Errorf("c%d: d%d is missing right after its write", i, j)

					return
				}
			}
		}()
	}

	wg.Wait()
	close(stop)

	if n := <-slept; n == 0 {
		t.Fatal("no collection slept during the writes: the test did not race")
	}

	db.Close()

	again := open(t, cfg(dir, nil))

	for i := range cols {
		col, _ := again.Get(fmt.Sprintf("c%d", i))

		if rows := col.Stats().Rows; rows != writes {
			t.Fatalf("c%d has %d rows after a restart, want %d", i, rows, writes)
		}
	}
}

// idle_close off keeps every collection open: SleepIdle with the DB's own
// setting closes nothing, and the loop does not run.
func TestIdleCloseOffKeepsCollectionsOpen(t *testing.T) {
	c := cfg(t.TempDir(), nil)
	c.IdleClose = "off"

	db := open(t, c)
	col := ensure(t, db, catalog.Def{Name: "a"})

	insertRange(t, col, 0, 3)

	if db.IdleClose() != 0 {
		t.Fatalf("idle_close off is %s, want 0", db.IdleClose())
	}

	if asleep(t, col) {
		t.Fatal("a collection slept with idle_close off")
	}
}

// Stop, the first step of a shutdown, ends a compaction in progress: the
// request that runs it returns ErrStopped at once, not after the merge.
func TestStopEndsACompaction(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "big"})

	for b := 0; b < 200_000; b += 1000 {
		docs := make([][]byte, 0, 1000)

		for i := b; i < b+1000; i++ {
			docs = append(docs, fmt.Appendf(nil, `{"_id":"d%07d","n":%d,"pad":"%064d"}`, i, i, i))
		}

		if _, err := col.Insert(ctx, docs...); err != nil {
			t.Fatal(err)
		}

		if b%25_000 == 24_000 {
			if err := col.Seal(); err != nil {
				t.Fatal(err)
			}
		}
	}

	done := make(chan error, 1)

	go func() {
		_, err := col.Compact()
		done <- err
	}()

	time.Sleep(100 * time.Millisecond)

	stopped := time.Now()
	db.Stop()

	select {
	case err := <-done:
		if took := time.Since(stopped); took > 2*time.Second {
			t.Fatalf("the compaction returned %s after Stop", took)
		}

		if !errors.Is(err, collection.ErrStopped) {
			t.Fatalf("compaction: %v, want ErrStopped (it was still merging)", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the compaction did not stop")
	}
}
