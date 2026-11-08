package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
)

func TestCompactMergesSegmentsAndReportsBeforeAndAfter(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "posts"})

	for i := 0; i < 3; i++ {
		doc := fmt.Sprintf(`{"_id":"p%d"}`, i)

		if _, err := col.Insert(ctx, []byte(doc)); err != nil {
			t.Fatal(err)
		}

		if err := col.Seal(); err != nil {
			t.Fatal(err)
		}
	}

	res, err := col.Compact()
	if err != nil {
		t.Fatal(err)
	}

	if res.Before.Segments != 3 || res.After.Segments != 1 {
		t.Fatalf("segments before/after = %d/%d, want 3/1", res.Before.Segments, res.After.Segments)
	}

	if res.Before.Bytes <= 0 || res.After.Bytes <= 0 {
		t.Fatalf("bytes = %+v", res)
	}

	if col.Stats().Rows != 3 {
		t.Fatalf("rows = %d, want 3", col.Stats().Rows)
	}
}

func TestCompactSealsTheMemtableFirst(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "posts"})

	if _, err := col.Insert(ctx, []byte(`{"_id":"p1"}`)); err != nil {
		t.Fatal(err)
	}

	res, err := col.Compact()
	if err != nil {
		t.Fatal(err)
	}

	if res.After.Segments != 1 {
		t.Fatalf("segments after = %d, want 1", res.After.Segments)
	}
}

// A direct collection has segments too (disk first): compact seals and
// merges them.
func TestCompactOnDirectCollectionMergesItsSegments(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "calls")

	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}

	def := catalog.Def{Name: "calls", Write: catalog.Direct, Path: filepath.Join(files, "*.ndjson")}
	db := open(t, cfg(filepath.Join(root, "data"), map[string]catalog.Def{"calls": def}))

	col, err := db.Get("calls")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Compact(); err != nil {
		t.Fatalf("compact of a direct collection: %v", err)
	}
}

func TestQuarantineListsBadLinesOfADirectCollection(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "calls")

	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}

	data := `{"_id":"c1"}` + "\n" + "{not json\n" + `{"_id":"c2"}` + "\n"

	if err := os.WriteFile(filepath.Join(files, "a.ndjson"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	def := catalog.Def{Name: "calls", Write: catalog.Direct, Path: filepath.Join(files, "*.ndjson")}
	db := open(t, cfg(filepath.Join(root, "data"), map[string]catalog.Def{"calls": def}))

	col, err := db.Get("calls")
	if err != nil {
		t.Fatal(err)
	}

	rows, total, err := col.Quarantine(10)
	if err != nil {
		t.Fatal(err)
	}

	if total != 1 || len(rows) != 1 || rows[0].Line != "{not json" || !strings.HasSuffix(rows[0].File, "a.ndjson") {
		t.Fatalf("total = %d rows = %+v", total, rows)
	}
}

func TestQuarantineOfAnEngineCollectionIsEmpty(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "posts"})

	rows, total, err := col.Quarantine(10)
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("rows = %v total = %d err = %v", rows, total, err)
	}
}

func TestDirAndCollectionDir(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	if db.Dir() != dir {
		t.Fatalf("Dir = %q", db.Dir())
	}
}
