package engine_test

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
)

func ids(t *testing.T, col *engine.Collection) map[string]bool {
	t.Helper()

	page, err := col.Find(ctx, []byte(`{"limit":100000}`))
	if err != nil {
		t.Fatal(err)
	}

	out := map[string]bool{}

	for _, row := range page.Rows {
		out[between(string(row), `"k":"`, `"`)] = true
	}

	return out
}

func between(s, a, b string) string {
	_, rest, _ := strings.Cut(s, a)
	out, _, _ := strings.Cut(rest, b)

	return out
}

// A backup taken while writes go on is a prefix of the commit order: every
// write acked before it started is in, none is half in, and every blob that a
// copied document refers to is in the copy.
func TestOnlineBackupIsAConsistentPrefix(t *testing.T) {
	src := t.TempDir()
	db := open(t, cfg(src, nil))

	events := ensure(t, db, catalog.Def{Name: "events", ID: "k", Indexes: []string{"n"}})
	calls := ensure(t, db, catalog.Def{Name: "calls", ID: "k", BlobThreshold: 64})

	for i := range 100 {
		if _, err := events.Insert(ctx, []byte(fmt.Sprintf(`{"k":"e%04d","n":%d}`, i, i))); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := calls.Insert(ctx, []byte(`{"k":"c1","audio":"`+strings.Repeat("a", 300)+`"}`)); err != nil {
		t.Fatal(err)
	}

	var (
		acked atomic.Int64
		stop  atomic.Bool
		wg    sync.WaitGroup
	)

	acked.Store(100)
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 100; !stop.Load(); i++ {
			if _, err := events.Insert(ctx, []byte(fmt.Sprintf(`{"k":"e%04d","n":%d}`, i, i))); err != nil {
				return
			}

			acked.Store(int64(i + 1))
		}
	}()

	before := acked.Load()
	dest := filepath.Join(t.TempDir(), "bk")

	if _, err := db.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}

	stop.Store(true)
	wg.Wait()

	restored := open(t, cfg(dest, nil))
	got := ids(t, mustGet(t, restored, "events"))

	if int64(len(got)) < before {
		t.Fatalf("restored %d events, %d were acked before the backup", len(got), before)
	}

	for i := range len(got) {
		if !got[fmt.Sprintf("e%04d", i)] {
			t.Fatalf("gap: e%04d is missing in a backup of %d events", i, len(got))
		}
	}

	page, err := mustGet(t, restored, "calls").Find(ctx, []byte(`{}`))
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("calls: %v %v", page.Rows, err)
	}

	refs := blob.Refs(page.Rows[0])

	if len(refs) != 1 || !restored.Blobs().Has(refs[0]) {
		t.Fatalf("blob of the copied document is missing: %v", refs)
	}

	if def, ok := restored.Definition("events"); !ok || len(def.Indexes) != 1 {
		t.Fatalf("catalog not restored: %+v", def)
	}
}

func mustGet(t *testing.T, db *engine.DB, name string) *engine.Collection {
	t.Helper()

	col, err := db.Get(name)
	if err != nil {
		t.Fatal(err)
	}

	return col
}

func TestBackupRefusesANonEmptyDest(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	dest := t.TempDir()

	if _, err := db.Backup(ctx, dest); err != nil {
		t.Fatalf("empty dest: %v", err)
	}

	if _, err := db.Backup(ctx, dest); err == nil {
		t.Fatal("a second backup into the same dir must fail")
	}
}

// Offline copy of a data dir with sealed segments: the restore answers the
// same query with the same rows.
func TestOfflineRestoreGivesSameResults(t *testing.T) {
	src := t.TempDir()
	db, err := engine.Open(cfg(src, nil), slog.Default())
	if err != nil {
		t.Fatal(err)
	}

	col := ensure(t, db, catalog.Def{Name: "events", ID: "k", Indexes: []string{"n"}})

	for i := range 60 {
		if _, err := col.Insert(ctx, []byte(fmt.Sprintf(`{"k":"e%03d","n":%d}`, i, i%7))); err != nil {
			t.Fatal(err)
		}
	}

	if err := col.Seal(); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Update(ctx, "e001", collection.Change{Set: map[string]any{"n": 99}}, 0); err != nil {
		t.Fatal(err)
	}

	if err := col.Delete(ctx, "e002", 0); err != nil {
		t.Fatal(err)
	}

	query := []byte(`{"where":{"n":{"$gte":3}},"sort":{"k":1},"limit":1000}`)

	want, err := col.Find(ctx, query)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(t.TempDir(), "bk")

	if err := backup.Offline(src, dest); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dest, "collections", "events", "segments")); err != nil {
		t.Fatalf("segments not copied: %v", err)
	}

	restored := mustGet(t, open(t, cfg(dest, nil)), "events")

	got, err := restored.Find(ctx, query)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Rows) == 0 || len(got.Rows) != len(want.Rows) {
		t.Fatalf("restored %d rows, want %d", len(got.Rows), len(want.Rows))
	}

	for i := range want.Rows {
		if string(got.Rows[i]) != string(want.Rows[i]) {
			t.Fatalf("row %d: %s != %s", i, got.Rows[i], want.Rows[i])
		}
	}
}
