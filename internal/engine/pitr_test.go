package engine_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

func pitrCfg(dir string) config.Config {
	c := cfg(dir, nil)
	c.PITR = "1d"

	return c
}

// mark is an instant between two writes: a write before it has a record
// time before it, a write after it one after it.
func mark() time.Time {
	time.Sleep(3 * time.Millisecond)

	at := time.Now()

	time.Sleep(3 * time.Millisecond)

	return at
}

func restore(t *testing.T, db *engine.DB, name, into string, until time.Time) *engine.Collection {
	t.Helper()

	if err := db.Restore(name, into, until, time.Now()); err != nil {
		t.Fatalf("restore %s at %s: %v", name, until.Format(time.RFC3339Nano), err)
	}

	return mustGet(t, db, into)
}

func insertRange(t *testing.T, col *engine.Collection, from, to int) {
	t.Helper()

	for start := from; start < to; start += 500 {
		var docs [][]byte

		for i := start; i < min(start+500, to); i++ {
			docs = append(docs, fmt.Appendf(nil, `{"_id":"p%d","n":%d}`, i, i))
		}

		if _, err := col.Insert(ctx, docs...); err != nil {
			t.Fatal(err)
		}
	}
}

// The DynamoDB case: a delete by mistake at T. A restore to just before T
// has every document; one to just after has none of the deleted. A seal and
// a compaction between the snapshot and T change nothing.
func TestRestoreBeforeAndAfterAMistake(t *testing.T) {
	dir := t.TempDir()
	db := open(t, pitrCfg(dir))
	col := ensure(t, db, catalog.Def{Name: "calls", Indexes: []string{"n"}})

	insertRange(t, col, 0, 1000)

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	if err := db.SnapshotPITR(time.Now()); err != nil {
		t.Fatal(err)
	}

	insertRange(t, col, 1000, 1500)

	if _, err := col.Compact(); err != nil {
		t.Fatal(err)
	}

	insertRange(t, col, 1500, 1600)

	before := mark()

	if n, err := col.Delete(ctx, []byte(`{"n":{"$lt":800}}`), true); err != nil || n != 800 {
		t.Fatalf("delete = %d, %v", n, err)
	}

	after := mark()

	insertRange(t, col, 5000, 5010)

	if n := rows(t, restore(t, db, "calls", "calls_before", before)); n != 1600 {
		t.Fatalf("restore before the delete has %d rows, want 1600", n)
	}

	got := restore(t, db, "calls", "calls_after", after)

	if n := rows(t, got); n != 800 {
		t.Fatalf("restore after the delete has %d rows, want 800", n)
	}

	indexReady(t, got, "n")

	page, err := got.Find(ctx, []byte(`{"query":{"n":900},"explain":true}`))
	if err != nil || len(page.Rows) != 1 || page.Plan != "index:n" {
		t.Fatalf("find by index on the copy: rows=%d plan=%q err=%v", len(page.Rows), page.Plan, err)
	}

	if n := rows(t, col); n != 810 {
		t.Fatalf("the source changed: %d rows", n)
	}

	// The copy is a normal collection: it takes writes and survives a restart.
	insertRange(t, got, 9000, 9001)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = open(t, pitrCfg(dir))

	if n := rows(t, mustGet(t, db, "calls_after")); n != 801 {
		t.Fatalf("the copy after a restart has %d rows, want 801", n)
	}
}

// Then rename puts the copy in place; the replaced collection stays in the
// trash for the window, so the restore itself can be undone.
func TestRestoreThenRenameReplaceCanBeUndone(t *testing.T) {
	db := open(t, pitrCfg(t.TempDir()))
	col := ensure(t, db, catalog.Def{Name: "calls", Indexes: []string{"n"}})

	insertRange(t, col, 0, 100)

	before := mark()

	if _, err := col.Delete(ctx, []byte(`{"n":{"$lt":60}}`), true); err != nil {
		t.Fatal(err)
	}

	restore(t, db, "calls", "calls_restored", before)

	beforeSwap := mark()

	if err := db.Rename("calls_restored", "calls", true); err != nil {
		t.Fatal(err)
	}

	if n := rows(t, mustGet(t, db, "calls")); n != 100 {
		t.Fatalf("calls has %d rows after the swap, want 100", n)
	}

	if n := rows(t, restore(t, db, "calls", "calls_undo", beforeSwap)); n != 40 {
		t.Fatalf("the replaced calls has %d rows, want 40", n)
	}
}

func TestRestoreOfATruncatedAndADroppedCollection(t *testing.T) {
	db := open(t, pitrCfg(t.TempDir()))
	col := ensure(t, db, catalog.Def{Name: "calls"})

	insertRange(t, col, 0, 30)

	before := mark()

	if err := db.Truncate("calls", "calls"); err != nil {
		t.Fatal(err)
	}

	insertRange(t, col, 100, 105)

	if n := rows(t, restore(t, db, "calls", "calls_t", before)); n != 30 {
		t.Fatalf("restore before the truncate has %d rows, want 30", n)
	}

	beforeDrop := mark()

	if err := db.Drop("calls", "calls"); err != nil {
		t.Fatal(err)
	}

	if n := rows(t, restore(t, db, "calls", "calls_d", beforeDrop)); n != 5 {
		t.Fatalf("restore before the drop has %d rows, want 5", n)
	}

	info, err := db.PITR("calls", time.Now())
	if err != nil || !info.Dropped || info.Oldest.IsZero() {
		t.Fatalf("pitr info of a dropped collection = %+v, %v", info, err)
	}
}

func TestRestoreChecksItsInput(t *testing.T) {
	c := pitrCfg(t.TempDir())
	c.Collections = map[string]catalog.Def{"quiet": {Name: "quiet", PITR: "off"}}

	db := open(t, c)

	insertRange(t, ensure(t, db, catalog.Def{Name: "calls"}), 0, 3)

	now := time.Now()

	cases := map[string]struct {
		name, into string
		until      time.Time
		want       error
	}{
		"unknown":  {"nope", "x", now, catalog.ErrUnknown},
		"bad into": {"calls", "Bad", now, catalog.ErrInvalid},
		"future":   {"calls", "x", now.Add(time.Hour), engine.ErrOutOfWindow},
		"too old":  {"calls", "x", now.Add(-25 * time.Hour), engine.ErrOutOfWindow},
		"taken":    {"calls", "quiet", now, engine.ErrExists},
		"off":      {"quiet", "x", now, engine.ErrPITROff},
	}

	for name, tc := range cases {
		if err := db.Restore(tc.name, tc.into, tc.until, now); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

// One snapshot when there is none; a new one a day later only with writes;
// the older ones go once a newer one covers the start of the window.
func TestSnapshotsFollowTheWindow(t *testing.T) {
	dir := t.TempDir()
	db := open(t, pitrCfg(dir))
	col := ensure(t, db, catalog.Def{Name: "calls"})
	snaps := func() int {
		entries, _ := os.ReadDir(filepath.Join(dir, "collections", "calls", "pitr"))

		return len(entries)
	}

	now := time.Now()

	if err := db.SnapshotPITR(now); err != nil || snaps() != 0 {
		t.Fatalf("an empty collection got a snapshot: %d, %v", snaps(), err)
	}

	insertRange(t, col, 0, 10)

	step := func(at time.Time, want int) {
		t.Helper()

		if err := db.SnapshotPITR(at); err != nil {
			t.Fatal(err)
		}

		if got := snaps(); got != want {
			t.Fatalf("at +%s: %d snapshots, want %d", at.Sub(now), got, want)
		}
	}

	step(now, 1)
	step(now.Add(25*time.Hour), 1)

	insertRange(t, col, 10, 20)

	step(now.Add(26*time.Hour), 2)
	step(now.Add(49*time.Hour), 2)

	insertRange(t, col, 20, 30)

	step(now.Add(51*time.Hour), 2)
}

func TestRestoreOfAPartitionedCollection(t *testing.T) {
	db := open(t, pitrCfg(t.TempDir()))

	def, err := db.EnsurePartitioned(catalog.Def{Name: "logs", Time: "ts", PartitionBy: "day"})
	if err != nil {
		t.Fatal(err)
	}

	today := time.Now().UTC()

	for i, at := range []time.Time{today.AddDate(0, 0, -1), today} {
		col, err := db.PartitionAt(def, at)
		if err != nil {
			t.Fatal(err)
		}

		for j := range 5 {
			if _, err := col.Insert(ctx, fmt.Appendf(nil, `{"_id":"d%d-%d","ts":%q}`, i, j, at.Format(time.RFC3339))); err != nil {
				t.Fatal(err)
			}
		}
	}

	before := mark()

	if err := db.DropPartitioned(def, true); err != nil {
		t.Fatal(err)
	}

	if err := db.Restore("logs", "logs_back", before, time.Now()); err != nil {
		t.Fatal(err)
	}

	back, ok := db.Partitioned("logs_back")
	if !ok {
		t.Fatal("logs_back is not partitioned")
	}

	parts := db.Partitions(back, false, time.Now())

	if len(parts) != 2 {
		t.Fatalf("partitions = %v", parts)
	}

	for _, p := range parts {
		if n := rows(t, mustGet(t, db, p)); n != 5 {
			t.Fatalf("%s has %d rows, want 5", p, n)
		}
	}
}

// With wal_retention, the WAL files from the oldest snapshot on stay.
func TestWALTruncationKeepsWhatTheOldestSnapshotNeeds(t *testing.T) {
	defer engine.SetWALSegmentSize(400)()

	dir := t.TempDir()
	db := open(t, pitrCfg(dir))
	col := ensure(t, db, catalog.Def{Name: "calls", WALRetention: "1s"})

	insertRange(t, col, 0, 20)

	if err := db.SnapshotPITR(time.Now()); err != nil {
		t.Fatal(err)
	}

	before := mark()

	for i := 20; i < 60; i++ {
		insertRange(t, col, i, i+1)
	}

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	time.Sleep(1100 * time.Millisecond)

	if _, err := col.TruncateWAL(); err != nil {
		t.Fatal(err)
	}

	if n := rows(t, restore(t, db, "calls", "calls_back", before)); n != 20 {
		t.Fatalf("restore has %d rows, want 20", n)
	}
}

// indexReady waits for the index build that an open starts.
func indexReady(t *testing.T, col *engine.Collection, field string) {
	t.Helper()

	for range 500 {
		for _, ix := range col.Stats().Indexes {
			if ix.Field == field && ix.State == "ready" {
				return
			}
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("index %s is not ready", field)
}
