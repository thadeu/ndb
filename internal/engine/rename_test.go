package engine_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
)

func TestRenameMovesDocumentsIndexesAndCursors(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))
	col := ensure(t, db, catalog.Def{Name: "posts", Indexes: []string{"n"}})

	fillPosts(t, col, 1000)

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	for i := range 200 {
		if _, err := col.Insert(ctx, fmt.Appendf(nil, `{"_id":"m%d"}`, i)); err != nil {
			t.Fatal(err)
		}
	}

	s, err := db.Watch("posts", "c", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Ack("posts", "c", nextEvent(t, s).Pos); err != nil {
		t.Fatal(err)
	}

	s.Close()

	if err := db.Rename("posts", "articles", false); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Get("posts"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("get old name: %v", err)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"x"}`)); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("a held collection after rename: %v", err)
	}

	renamed := mustGet(t, db, "articles")

	if n := rows(t, renamed); n != 1200 {
		t.Fatalf("rows = %d, want 1200", n)
	}

	indexReady(t, renamed, "n")

	page, err := renamed.Find(ctx, []byte(`{"query":{"n":7},"explain":true}`))
	if err != nil || len(page.Rows) != 1 || page.Plan != "index:n" {
		t.Fatalf("find by index: rows=%d plan=%q err=%v", len(page.Rows), page.Plan, err)
	}

	s, err = db.Watch("articles", "c", "")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if ev := nextEvent(t, s); ev.ID != "p1" {
		t.Fatalf("the cursor did not follow the rename: %+v", ev)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = open(t, cfg(dir, nil))

	if n := rows(t, mustGet(t, db, "articles")); n != 1200 {
		t.Fatalf("rows after reopen = %d", n)
	}

	if _, ok := db.Definition("posts"); ok {
		t.Fatal("the old definition is back")
	}
}

func TestRenameChecksItsInput(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	ensure(t, db, catalog.Def{Name: "posts"})
	ensure(t, db, catalog.Def{Name: "other"})

	cases := map[string]struct {
		from, to string
		want     error
	}{
		"unknown":  {"nope", "x", catalog.ErrUnknown},
		"bad name": {"posts", "Bad Name", catalog.ErrInvalid},
		"pattern":  {"posts", "post-*", catalog.ErrInvalid},
		"same":     {"posts", "posts", catalog.ErrInvalid},
		"taken":    {"posts", "other", engine.ErrExists},
	}

	for name, tc := range cases {
		if err := db.Rename(tc.from, tc.to, false); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v, want %v", name, err, tc.want)
		}
	}
}

func TestRenameWithReplacePutsTheSourceInPlace(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	fillPosts(t, ensure(t, db, catalog.Def{Name: "calls_restored"}), 30)
	fillPosts(t, ensure(t, db, catalog.Def{Name: "calls"}), 5)

	if err := db.Rename("calls_restored", "calls", true); err != nil {
		t.Fatal(err)
	}

	if n := rows(t, mustGet(t, db, "calls")); n != 30 {
		t.Fatalf("calls has %d rows, want 30", n)
	}

	if _, err := db.Get("calls_restored"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("the source name is still there: %v", err)
	}

	trashEmpty(t, dir)
}

func TestRenameOfAPartitionedCollectionMovesItsPartitions(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	def, err := db.EnsurePartitioned(catalog.Def{Name: "logs", Time: "ts", PartitionBy: "day"})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{now.AddDate(0, 0, -1), now} {
		col, err := db.PartitionAt(def, at)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := col.Insert(ctx, []byte(`{"ts":"`+at.Format(time.RFC3339)+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.Rename("logs-2026-11-07", "x", false); !errors.Is(err, catalog.ErrInvalid) {
		t.Fatalf("rename of a partition: %v", err)
	}

	if err := db.Rename("logs", "events", false); err != nil {
		t.Fatal(err)
	}

	moved, ok := db.Partitioned("events")
	if !ok {
		t.Fatal("events is not partitioned")
	}

	want := []string{"events-2026-11-06", "events-2026-11-07"}

	if got := db.Partitions(moved, false, now); !slices.Equal(got, want) {
		t.Fatalf("partitions = %v, want %v", got, want)
	}

	if _, ok := db.Definition("logs"); ok {
		t.Fatal("the old logical definition is still there")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = open(t, cfg(dir, nil))

	for _, name := range want {
		if n := rows(t, mustGet(t, db, name)); n != 1 {
			t.Fatalf("%s has %d rows after reopen", name, n)
		}
	}
}

// A crash leaves rename.json behind: the next start finishes the rename,
// from either stage, whatever was already moved.
func TestStartFinishesAStoppedRename(t *testing.T) {
	for _, stage := range []string{"aside", "move", "moved-dir"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			db := open(t, cfg(dir, nil))

			fillPosts(t, ensure(t, db, catalog.Def{Name: "restored"}), 7)
			fillPosts(t, ensure(t, db, catalog.Def{Name: "calls"}), 2)

			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			op := map[string]any{"from": "restored", "to": "calls", "replace": true, "stage": "aside"}
			root := filepath.Join(dir, "collections")

			switch stage {
			case "move", "moved-dir":
				op["stage"] = "move"

				if err := os.MkdirAll(filepath.Join(dir, "trash"), 0o755); err != nil {
					t.Fatal(err)
				}

				if err := os.Rename(filepath.Join(root, "calls"), filepath.Join(dir, "trash", "calls.1")); err != nil {
					t.Fatal(err)
				}
			}

			if stage == "moved-dir" {
				if err := os.Rename(filepath.Join(root, "restored"), filepath.Join(root, "calls")); err != nil {
					t.Fatal(err)
				}
			}

			data, _ := json.Marshal(op)

			if err := os.WriteFile(filepath.Join(dir, "rename.json"), data, 0o644); err != nil {
				t.Fatal(err)
			}

			db = open(t, cfg(dir, nil))

			if n := rows(t, mustGet(t, db, "calls")); n != 7 {
				t.Fatalf("calls has %d rows, want 7", n)
			}

			if _, ok := db.Definition("restored"); ok {
				t.Fatal("restored is still in the catalog")
			}

			if _, err := os.Stat(filepath.Join(dir, "rename.json")); !os.IsNotExist(err) {
				t.Fatalf("rename.json is still there: %v", err)
			}
		})
	}
}
