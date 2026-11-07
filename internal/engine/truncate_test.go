package engine_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
)

func fillPosts(t *testing.T, col *engine.Collection, n int) {
	t.Helper()

	for start := 0; start < n; start += 500 {
		var docs [][]byte

		for i := start; i < min(start+500, n); i++ {
			docs = append(docs, fmt.Appendf(nil, `{"_id":"p%d","n":%d}`, i, i))
		}

		if _, err := col.Insert(ctx, docs...); err != nil {
			t.Fatal(err)
		}
	}
}

func rows(t *testing.T, col *engine.Collection) int {
	t.Helper()

	page, err := col.Find(ctx, []byte(`{"limit":100000}`))
	if err != nil {
		t.Fatal(err)
	}

	return len(page.Rows)
}

func trashEmpty(t *testing.T, dir string) {
	t.Helper()

	for range 200 {
		entries, _ := os.ReadDir(filepath.Join(dir, "trash"))

		if len(entries) == 0 {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("trash is not empty")
}

func TestTruncateKeepsDefinitionAndIndexes(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))
	col := ensure(t, db, catalog.Def{Name: "posts", Indexes: []string{"n"}, Unique: []string{"slug"}})

	fillPosts(t, col, 2000)

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"mem","n":-1}`)); err != nil {
		t.Fatal(err)
	}

	if err := db.Truncate("posts", "posts"); err != nil {
		t.Fatal(err)
	}

	if n := rows(t, col); n != 0 {
		t.Fatalf("rows after truncate = %d", n)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"p1","n":1,"slug":"a"}`)); err != nil {
		t.Fatalf("insert after truncate: %v", err)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"p2","slug":"a"}`)); err == nil {
		t.Fatal("unique must hold after truncate")
	}

	page, err := col.Find(ctx, []byte(`{"query":{"n":1},"explain":true}`))
	if err != nil || len(page.Rows) != 1 || page.Plan != "index:n" {
		t.Fatalf("find by index: rows=%d plan=%q err=%v", len(page.Rows), page.Plan, err)
	}

	trashEmpty(t, dir)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db = open(t, cfg(dir, nil))
	col = mustGet(t, db, "posts")

	if n := rows(t, col); n != 1 {
		t.Fatalf("rows after reopen = %d, want 1", n)
	}
}

func TestTruncateAndDropCheckTheirInput(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	ensure(t, db, catalog.Def{Name: "posts"})

	for _, err := range []error{db.Truncate("posts", ""), db.Truncate("posts", "other"), db.Drop("posts", "")} {
		if !errors.Is(err, engine.ErrConfirm) {
			t.Fatalf("got %v, want ErrConfirm", err)
		}
	}

	if err := db.Truncate("nope", "nope"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("unknown: got %v", err)
	}
}

func TestWatchGetsOneTruncateEventAndGoesOn(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertIDs(t, db, 1, 3)

	early, err := db.Watch("posts", "early", "")
	if err != nil {
		t.Fatal(err)
	}

	var last string

	for range 3 {
		last = nextEvent(t, early).Pos
	}

	if err := db.Ack("posts", "early", last); err != nil {
		t.Fatal(err)
	}

	live, err := db.Watch("posts", "live", "now")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Truncate("posts", "posts"); err != nil {
		t.Fatal(err)
	}

	c, cancel := context2s()
	defer cancel()

	if _, err := live.Next(c); err == nil {
		t.Fatal("a live stream must end at a truncate")
	}

	early.Close()
	live.Close()

	insertIDs(t, db, 4, 4)

	for _, consumer := range []string{"early", "live", "fresh"} {
		s, err := db.Watch("posts", consumer, "")
		if err != nil {
			t.Fatalf("%s: %v", consumer, err)
		}

		ev := nextEvent(t, s)

		if ev.Op != "truncate" || ev.ID != "" {
			t.Fatalf("%s: first event %+v, want truncate", consumer, ev)
		}

		ev = nextEvent(t, s)

		if ev.Op != "put" || ev.ID != "p4" {
			t.Fatalf("%s: second event %+v, want put p4", consumer, ev)
		}

		s.Close()
	}
}

func TestWatchWhereKeepsTheTruncateEvent(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertN(t, db, 1, 3)

	if err := db.Truncate("posts", "posts"); err != nil {
		t.Fatal(err)
	}

	s, err := db.WatchWhere("posts", "c", "", []byte(`{"n":{"$gt":100}}`))
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if ev := nextEvent(t, s); ev.Op != "truncate" {
		t.Fatalf("got %+v", ev)
	}
}

func TestDropRemovesCollectionCursorsAndFiles(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	insertIDs(t, db, 1, 5)

	s, err := db.Watch("posts", "c", "")
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Ack("posts", "c", nextEvent(t, s).Pos); err != nil {
		t.Fatal(err)
	}

	s.Close()

	old := mustGet(t, db, "posts")

	if err := db.Drop("posts", "posts"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Get("posts"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("get after drop: %v", err)
	}

	if _, err := old.Insert(ctx, []byte(`{"_id":"x"}`)); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("a held collection after drop: %v", err)
	}

	if _, ok := db.Definition("posts"); ok {
		t.Fatal("the definition is still in the catalog")
	}

	trashEmpty(t, dir)

	insertIDs(t, db, 9, 9)

	col := mustGet(t, db, "posts")

	if n := rows(t, col); n != 1 {
		t.Fatalf("a new posts has %d rows, want 1", n)
	}

	s, err = db.Watch("posts", "c", "")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if ev := nextEvent(t, s); ev.ID != "p9" {
		t.Fatalf("the cursor of c was not dropped: %+v", ev)
	}
}

func TestDirectCollectionsRefuseTruncateAndDrop(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	ensure(t, db, catalog.Def{Name: "logs", Write: catalog.Direct, Path: filepath.Join(t.TempDir(), "*.ndjson")})

	if err := db.Truncate("logs", "logs"); !errors.Is(err, engine.ErrDirect) {
		t.Fatalf("truncate: %v", err)
	}

	if err := db.Drop("logs", "logs"); !errors.Is(err, engine.ErrDirect) {
		t.Fatalf("drop: %v", err)
	}
}

// Writes that run during a truncate are before it (gone) or after it
// (kept), and none fails.
func TestTruncateWithConcurrentWrites(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "posts"})

	var wg sync.WaitGroup

	errs := make(chan error, 4)

	for w := range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := range 200 {
				if _, err := col.Insert(ctx, fmt.Appendf(nil, `{"_id":"w%d-%d"}`, w, i)); err != nil {
					errs <- err

					return
				}
			}
		}()
	}

	time.Sleep(5 * time.Millisecond)

	if err := db.Truncate("posts", "posts"); err != nil {
		t.Fatal(err)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("write during truncate: %v", err)
	}

	if n := rows(t, col); n >= 800 {
		t.Fatalf("rows = %d, the truncate removed nothing", n)
	}
}

// A crash after the old dir went to the trash and before ~new got its
// name: the next start finishes the truncate.
func TestStartFinishesAStoppedTruncate(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	insertIDs(t, db, 1, 3)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	posts := filepath.Join(dir, "collections", "posts")

	if err := os.Rename(posts, posts+"~new"); err != nil {
		t.Fatal(err)
	}

	db = open(t, cfg(dir, nil))

	if n := rows(t, mustGet(t, db, "posts")); n != 3 {
		t.Fatalf("rows = %d, want 3 (the dir under ~new)", n)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(posts+"~new", 0o755); err != nil {
		t.Fatal(err)
	}

	db = open(t, cfg(dir, nil))

	if n := rows(t, mustGet(t, db, "posts")); n != 3 {
		t.Fatalf("rows = %d, want 3 (~new next to posts is dropped)", n)
	}

	if _, err := os.Stat(posts + "~new"); !os.IsNotExist(err) {
		t.Fatalf("~new is still there: %v", err)
	}
}

func TestBackupAfterTruncateRestores(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insertIDs(t, db, 1, 50)

	if err := db.Truncate("posts", "posts"); err != nil {
		t.Fatal(err)
	}

	insertIDs(t, db, 60, 62)

	var buf bytes.Buffer

	if err := db.Backup(ctx, &buf, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	stage := filepath.Join(t.TempDir(), "stage")

	if err := backup.Extract(&buf, stage); err != nil {
		t.Fatal(err)
	}

	restored, err := engine.Open(cfg(stage, nil), slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	defer restored.Close()

	col := mustGet(t, restored, "posts")

	if n := rows(t, col); n != 3 {
		t.Fatalf("restored rows = %d, want 3", n)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"p99"}`)); err != nil {
		t.Fatalf("insert after restore: %v", err)
	}
}

func context2s() (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 2*time.Second)
}
