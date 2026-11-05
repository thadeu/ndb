package engine_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/lock"
)

var ctx = context.Background()

func cfg(dir string, cols map[string]catalog.Def) config.Config {
	return config.Config{DataDir: dir, AllowImplicit: true, Collections: cols}
}

func open(t *testing.T, c config.Config) *engine.DB {
	t.Helper()

	db, err := engine.Open(c, slog.Default())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	t.Cleanup(func() { db.Close() })

	return db
}

func ensure(t *testing.T, db *engine.DB, def catalog.Def) *engine.Collection {
	t.Helper()

	if _, err := db.Ensure(def); err != nil {
		t.Fatalf("ensure %s: %v", def.Name, err)
	}

	col, err := db.Get(def.Name)
	if err != nil {
		t.Fatalf("get %s: %v", def.Name, err)
	}

	return col
}

func TestReopenRecoversDocuments(t *testing.T) {
	dir := t.TempDir()

	db, err := engine.Open(cfg(dir, nil), slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	col := ensure(t, db, catalog.Def{Name: "posts", ID: "post_id"})

	if _, err := col.Insert(ctx, []byte(`{"post_id":"p1","n":1}`), []byte(`{"post_id":"p2","n":2}`)); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2 := open(t, cfg(dir, nil))

	col2, err := db2.Get("posts")
	if err != nil {
		t.Fatalf("collection not recovered: %v", err)
	}

	if got := col2.Stats().Rows; got != 2 {
		t.Fatalf("rows after restart = %d, want 2", got)
	}
}

func TestSecondOpenOnSameDirIsLocked(t *testing.T) {
	dir := t.TempDir()

	open(t, cfg(dir, nil))

	_, err := engine.Open(cfg(dir, nil), slog.Default())
	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("second open: want ErrLocked, got %v", err)
	}
}

func TestCloseKeepsEveryAckedWrite(t *testing.T) {
	dir := t.TempDir()

	db, err := engine.Open(cfg(dir, nil), slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	col := ensure(t, db, catalog.Def{Name: "events", ID: "event_id"})

	var (
		acked atomic.Int64
		wg    sync.WaitGroup
	)

	for g := range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for i := 0; ; i++ {
				doc := fmt.Appendf(nil, `{"event_id":"g%d-%d"}`, g, i)

				if _, err := col.Insert(ctx, doc); err != nil {
					return
				}

				acked.Add(1)
			}
		}()
	}

	time.Sleep(30 * time.Millisecond)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	wg.Wait()

	db2 := open(t, cfg(dir, nil))
	col2, _ := db2.Get("events")

	if got, want := int64(col2.Stats().Rows), acked.Load(); got < want {
		t.Fatalf("rows = %d, but %d writes were acked", got, want)
	}
}

func TestEnsureMergesIndexesAndRejectsConflicts(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	ensure(t, db, catalog.Def{Name: "calls", ID: "call_id", Indexes: []string{"status"}})

	def, err := db.Ensure(catalog.Def{Name: "calls", ID: "call_id", Indexes: []string{"dnis"}})
	if err != nil {
		t.Fatal(err)
	}

	if len(def.Indexes) != 2 {
		t.Fatalf("indexes = %v, want both status and dnis", def.Indexes)
	}

	col, _ := db.Get("calls")

	if got := len(col.Stats().Indexes); got != 2 {
		t.Fatalf("live indexes = %d, want 2", got)
	}

	_, err = db.Ensure(catalog.Def{Name: "calls", ID: "other_id"})
	if !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("changed id: want catalog_conflict, got %v", err)
	}
}

func TestImplicitInsertCreatesCollection(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	if _, err := db.Get("logs"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("unknown collection: want unknown_collection, got %v", err)
	}

	col, err := db.GetOrCreate("logs")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, []byte(`{"msg":"hi"}`)); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Get("logs"); err != nil {
		t.Fatalf("implicit collection not in the catalog: %v", err)
	}
}

func TestImplicitCreateCanBeTurnedOff(t *testing.T) {
	c := cfg(t.TempDir(), nil)
	c.AllowImplicit = false

	db := open(t, c)

	if _, err := db.GetOrCreate("logs"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("want unknown_collection, got %v", err)
	}
}

func TestDropIndexRemovesItFromTheLiveCollection(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	ensure(t, db, catalog.Def{Name: "calls", ID: "call_id", Indexes: []string{"status", "dnis"}})

	if _, err := db.DropIndex("calls", "dnis"); err != nil {
		t.Fatal(err)
	}

	col, _ := db.Get("calls")

	for _, ix := range col.Stats().Indexes {
		if ix.Field == "dnis" {
			t.Fatal("dropped index still live")
		}
	}
}

func TestDirectCollectionReadsAppFilesAndRefusesWrites(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "calls")

	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}

	line := `{"call_id":"c1","status":"answered"}` + "\n"

	if err := os.WriteFile(filepath.Join(files, "a.ndjson"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	def := catalog.Def{
		Name:    "calls",
		Write:   catalog.Direct,
		Path:    filepath.Join(files, "*.ndjson"),
		ID:      "call_id",
		Indexes: []string{"status"},
	}

	c := cfg(filepath.Join(root, "data"), map[string]catalog.Def{"calls": def})

	db := open(t, c)

	col, err := db.Get("calls")
	if err != nil {
		t.Fatal(err)
	}

	page, err := col.Find(ctx, []byte(`{"where":{"status":"answered"}}`))
	if err != nil {
		t.Fatal(err)
	}

	if len(page.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(page.Rows))
	}

	if _, err := col.Insert(ctx, []byte(`{"call_id":"c2"}`)); !errors.Is(err, engine.ErrDirect) {
		t.Fatalf("insert into direct collection: want ErrDirect, got %v", err)
	}
}

func TestDaemonCollectionsSealAndRecoverFromSegments(t *testing.T) {
	dir := t.TempDir()

	db, err := engine.Open(cfg(dir, nil), slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	col := ensure(t, db, catalog.Def{Name: "posts", ID: "post_id"})

	if _, err := col.Insert(ctx, []byte(`{"post_id":"p1"}`), []byte(`{"post_id":"p2"}`)); err != nil {
		t.Fatal(err)
	}

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	segs, _ := filepath.Glob(filepath.Join(dir, "collections", "posts", "segments", "*.parquet"))

	if len(segs) != 1 {
		t.Fatalf("%d segment files, want 1: sealing is not wired in the daemon", len(segs))
	}

	db.Close()

	col2, err := open(t, cfg(dir, nil)).Get("posts")
	if err != nil || col2.Stats().Rows != 2 {
		t.Fatalf("after restart from a segment: err=%v", err)
	}
}

func TestDirectCollectionHidesExpiredRowsAndKeepsTheFiles(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "sessions")

	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := `{"sid":"gone","at":"2020-01-01T00:00:00Z","exp":"2020-01-02T00:00:00Z"}` + "\n" +
		`{"sid":"kept","at":"2999-01-01T00:00:00Z","exp":"2999-01-02T00:00:00Z"}` + "\n"
	file := filepath.Join(files, "a.ndjson")

	if err := os.WriteFile(file, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	def := catalog.Def{
		Name: "sessions", Write: catalog.Direct, Path: filepath.Join(files, "*.ndjson"),
		ID: "sid", Time: "at", Retention: "30d", ExpiresField: "exp",
	}

	db := open(t, cfg(filepath.Join(root, "data"), map[string]catalog.Def{"sessions": def}))

	col, err := db.Get("sessions")
	if err != nil {
		t.Fatal(err)
	}

	page, err := col.Find(ctx, []byte(`{}`))
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("rows = %d err %v, want only the live one", len(page.Rows), err)
	}

	after, err := os.ReadFile(file)
	if err != nil || string(after) != lines {
		t.Fatalf("the source file changed: %q err %v", after, err)
	}
}
