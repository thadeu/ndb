package diskusage

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string, n int) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanSplitsPartsAndCountsFiles(t *testing.T) {
	dir := t.TempDir()

	write(t, filepath.Join(dir, "collections", "a", "0001.ndjson"), 100)
	write(t, filepath.Join(dir, "collections", "a", "0002.ndjson"), 50)
	write(t, filepath.Join(dir, "collections", "a", "segments", "s1.parquet"), 400)
	write(t, filepath.Join(dir, "collections", "a", "meta.bolt"), 30)
	write(t, filepath.Join(dir, "blobs", "x"), 7)
	write(t, filepath.Join(dir, "cursors.bolt"), 9)
	write(t, filepath.Join(dir, "catalog.bolt"), 11)

	u, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(u.Collections) != 1 {
		t.Fatalf("collections = %d", len(u.Collections))
	}

	a := u.Collections[0]

	if a.Bytes.WAL != 150 || a.Bytes.Segments != 400 || a.Bytes.Meta != 30 || a.WALFiles != 2 || a.SegmentFiles != 1 {
		t.Fatalf("collection a = %+v", a)
	}

	if u.Shared.Blobs != 7 || u.Shared.Cursors != 9 || u.Shared.Meta != 11 {
		t.Fatalf("shared = %+v", u.Shared)
	}

	if want := int64(150 + 400 + 30 + 7 + 9 + 11); u.Total != want || u.Sum().Total() != want {
		t.Fatalf("total = %d sum = %d, want %d", u.Total, u.Sum().Total(), want)
	}
}
