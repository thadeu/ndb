package engine_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/feed"
)

func walCount(t *testing.T, dir string) int {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(dir, "collections", "posts", "*.ndjson"))
	if err != nil {
		t.Fatal(err)
	}

	return len(files)
}

func TestWALTruncationWaitsForTheSlowestStoredCursor(t *testing.T) {
	defer engine.SetWALSegmentSize(300)()

	dir := t.TempDir()
	db := open(t, cfg(dir, nil))
	col := ensure(t, db, catalog.Def{Name: "posts", WALRetention: "1ms"})

	insertIDs(t, db, 1, 30)

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	slow, err := db.Watch("posts", "slow", "")
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		nextEvent(t, slow)
	}

	third := nextEvent(t, slow)

	slow.Close()

	if err := db.Ack("posts", "slow", third.Pos); err != nil {
		t.Fatal(err)
	}

	before := walCount(t, dir)

	time.Sleep(5 * time.Millisecond)

	if _, err := col.TruncateWAL(); err != nil {
		t.Fatal(err)
	}

	mid := walCount(t, dir)

	if mid >= before || mid < 2 {
		t.Fatalf("files %d -> %d: only the files before tx 4 may go", before, mid)
	}

	resumed, err := db.Watch("posts", "slow", "")
	if err != nil {
		t.Fatalf("the slow consumer must resume: %v", err)
	}

	if ev := nextEvent(t, resumed); ev.ID != "p5" {
		t.Fatalf("resumed at %s", ev.ID)
	}

	resumed.Close()

	_, err = db.Watch("posts", "fresh", "beginning")

	var fe *feed.Error

	if !errors.As(err, &fe) || fe.Code() != "history_truncated" {
		t.Fatalf("a new consumer must get history_truncated, got %v", err)
	}
}

func TestWALTruncationKeepsEverythingWithoutRetention(t *testing.T) {
	defer engine.SetWALSegmentSize(300)()

	dir := t.TempDir()
	db := open(t, cfg(dir, nil))
	col := ensure(t, db, catalog.Def{Name: "posts"})

	insertIDs(t, db, 1, 30)

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	before := walCount(t, dir)

	if n, err := col.TruncateWAL(); err != nil || n != 0 {
		t.Fatalf("truncate = %d, %v", n, err)
	}

	if walCount(t, dir) != before {
		t.Fatal("files were deleted without wal_retention")
	}

}
