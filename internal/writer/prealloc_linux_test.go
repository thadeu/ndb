package writer_test

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

// diskKB is the disk a file holds (its blocks), not its size.
func diskKB(t *testing.T, path string) int64 {
	t.Helper()

	var st syscall.Stat_t

	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}

	return st.Blocks * 512 / 1024
}

func lastSegment(t *testing.T, dir string) string {
	t.Helper()

	files, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))

	return files[len(files)-1]
}

// A segment reserves disk a step ahead of its data, as big as the data
// (64 KiB to 4 MiB), never the whole segment size: 300 idle collections
// hold kilobytes each, not 4 MiB.
func TestSegmentReservesAStepAheadAndGivesTheRestBack(t *testing.T) {
	dir := t.TempDir()
	w, _ := open(t, writer.Config{Dir: dir})

	commit := func(id string, size int) {
		t.Helper()

		doc := []byte(`{"_id":"` + id + `","pad":"` + strings.Repeat("x", size) + `"}`)

		if _, _, err := w.Commit(context.Background(), []wal.Op{{Kind: wal.Put, ID: id, Doc: doc}}); err != nil {
			t.Fatal(err)
		}
	}

	commit("small", 10)

	if kb := diskKB(t, lastSegment(t, dir)); kb > 128 {
		t.Fatalf("a new small segment holds %d KB, want the minimum step (64 KiB)", kb)
	}

	for i := range 12 {
		commit(string(rune('a'+i)), 500<<10)
	}

	if kb := diskKB(t, lastSegment(t, dir)); kb < 6<<10 || kb > 11<<10 {
		t.Fatalf("after 6 MB of data the segment holds %d KB, want the data plus one step", kb)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	if kb := diskKB(t, lastSegment(t, dir)); kb > 6<<10+64 {
		t.Fatalf("a closed segment holds %d KB, want only its data (~6 MB)", kb)
	}
}

// A rotated segment gives back its reserve too.
func TestRotatedSegmentGivesItsReserveBack(t *testing.T) {
	dir := t.TempDir()
	w, _ := open(t, writer.Config{Dir: dir, SegmentSize: 8 << 20})

	for i := range 20 {
		doc := []byte(`{"pad":"` + strings.Repeat("y", 500<<10) + `"}`)

		if _, _, err := w.Commit(context.Background(), []wal.Op{{Kind: wal.Put, ID: string(rune('a' + i)), Doc: doc}}); err != nil {
			t.Fatal(err)
		}
	}

	files, _ := filepath.Glob(filepath.Join(dir, "*.ndjson"))

	if len(files) < 2 {
		t.Fatalf("want a rotation, got %d files", len(files))
	}

	if kb := diskKB(t, files[0]); kb > 8<<10+64 {
		t.Fatalf("the rotated segment holds %d KB, want at most its size (8 MiB)", kb)
	}
}
