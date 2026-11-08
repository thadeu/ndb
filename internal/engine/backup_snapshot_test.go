package engine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/feed"
)

// retained builds a data dir whose WAL lost its first files to
// wal_retention, with 31 docs, an update (p2 is _v 2), a delete (p3), a
// "late" consumer at the last tx and an "early" consumer at tx 3.
func retained(t *testing.T, dir string) (*engine.DB, map[string]uint64) {
	t.Helper()

	db := open(t, cfg(dir, nil))
	col := ensure(t, db, catalog.Def{Name: "posts", WALRetention: "1ms", Unique: []string{"slug"}})

	for i := 1; i <= 30; i++ {
		doc := fmt.Sprintf(`{"_id":"p%d","slug":"s%d"}`, i, i)

		if _, err := col.Insert(ctx, []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	before := walCount(t, dir)

	time.Sleep(5 * time.Millisecond)

	if n, err := col.TruncateWAL(); err != nil || n == 0 {
		t.Fatalf("truncate: %d %v (files %d)", n, err, before)
	}

	if _, err := col.UpdateID(ctx, "p2", []byte(`{"$set":{"n":1}}`), collection.UpdateOpts{}); err != nil {
		t.Fatal(err)
	}

	if _, err := col.DeleteID(ctx, "p3"); err != nil {
		t.Fatal(err)
	}

	live, err := db.Watch("posts", "late", "now")
	if err != nil {
		t.Fatal(err)
	}

	defer live.Close()

	if _, err := col.Insert(ctx, []byte(`{"_id":"p31","slug":"s31"}`)); err != nil {
		t.Fatal(err)
	}

	// The feed publishes a commit after its fsync, so "now" can be before the
	// delete of p3, and the stream then gives that delete first. Read up to
	// p31, so "late" is at the last tx.
	ev := nextEvent(t, live)

	for ev.ID != "p31" {
		ev = nextEvent(t, live)
	}

	if err := db.Ack("posts", "late", ev.Pos); err != nil {
		t.Fatal(err)
	}

	if err := db.Ack("posts", "early", "e3.1"); err != nil {
		t.Fatal(err)
	}

	want := map[string]uint64{}

	for i := 1; i <= 31; i++ {
		id := fmt.Sprintf("p%d", i)

		if d, ok := col.Get(id); ok {
			want[id] = d.V
		}
	}

	if len(want) != 30 || want["p2"] != 2 {
		t.Fatalf("setup: %v", want)
	}

	return db, want
}

func checkSnapshotRestore(t *testing.T, buf *bytes.Buffer, want map[string]uint64) {
	t.Helper()

	if !strings.Contains(buf.String(), `{"_section":"snapshot","collection":"posts","tx":33}`) {
		t.Fatalf("no snapshot section:\n%.600s", buf.String())
	}

	stage := filepath.Join(t.TempDir(), "stage")

	if err := backup.Extract(buf, stage); err != nil {
		t.Fatal(err)
	}

	db := open(t, cfg(stage, nil))
	col := mustGet(t, db, "posts")

	for id, v := range want {
		if d, ok := col.Get(id); !ok || d.V != v {
			t.Errorf("%s: got %+v ok=%v, want _v %d", id, d, ok, v)
		}
	}

	if _, ok := col.Get("p3"); ok {
		t.Error("p3 was deleted")
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"x","slug":"s1"}`)); err == nil {
		t.Error("unique must hold after the restore")
	}

	late, err := db.Watch("posts", "late", "")
	if err != nil {
		t.Fatalf("late consumer: %v", err)
	}

	defer late.Close()

	if _, err := col.Insert(ctx, []byte(`{"_id":"p32","slug":"s32"}`)); err != nil {
		t.Fatal(err)
	}

	if ev := nextEvent(t, late); ev.ID != "p32" || !strings.HasPrefix(ev.Pos, "e34.") {
		t.Errorf("the next tx must continue after the snapshot: %+v", ev)
	}

	early, err := db.Watch("posts", "early", "")

	if err == nil {
		defer early.Close()

		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		_, err = early.Next(c)
	}

	if !errors.Is(err, feed.ErrHistoryTruncated) {
		t.Errorf("early consumer: err = %v, want history_truncated", err)
	}
}

func TestOnlineBackupOfATruncatedWALUsesASnapshot(t *testing.T) {
	defer engine.SetWALSegmentSize(300)()

	db, want := retained(t, t.TempDir())

	var buf bytes.Buffer

	if err := db.Backup(ctx, &buf, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	checkSnapshotRestore(t, &buf, want)
}

func TestOfflineBackupOfATruncatedWALUsesASnapshot(t *testing.T) {
	defer engine.SetWALSegmentSize(300)()

	dir := t.TempDir()
	db, want := retained(t, dir)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer

	if err := engine.Offline(dir, &buf, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	checkSnapshotRestore(t, &buf, want)
}

func TestBackupOfAWholeWALHasNoSnapshot(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertIDs(t, db, 1, 3)

	var buf bytes.Buffer

	if err := db.Backup(ctx, &buf, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(buf.String(), `"_section":"snapshot"`) {
		t.Fatal("a WAL that starts at tx 1 needs no snapshot")
	}
}
