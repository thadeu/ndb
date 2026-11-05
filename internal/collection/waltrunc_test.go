package collection_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/fs"
)

func inTwoHours() time.Time {
	return time.Now().Add(2 * time.Hour)
}

// openTrunc opens a sealed collection with small WAL files, so a few
// inserts make several files. Nothing seals or truncates by itself.
func openTrunc(t *testing.T, root string, retention string, cfg collection.Config) *collection.Collection {
	t.Helper()

	def := sealDef
	def.WALRetention = retention

	cfg.Dir = filepath.Join(root, "wal")
	cfg.Data = root
	cfg.FS = fs.OS{}
	cfg.Def = def
	cfg.CompactMin = 1 << 20
	cfg.WALSegmentSize = 400
	cfg.SealBytes = 1 << 40
	cfg.SealAge = 1000 * time.Hour

	if cfg.Now == nil {
		cfg.Now = inTwoHours
	}

	c, err := collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	return c
}

func walFiles(t *testing.T, root string) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(root, "wal", "*.ndjson"))
	if err != nil {
		t.Fatal(err)
	}

	return files
}

func TestWALKeptForeverWithoutRetention(t *testing.T) {
	root := t.TempDir()
	c := openTrunc(t, root, "", collection.Config{})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	before := len(walFiles(t, root))

	n, err := c.TruncateWAL()
	if err != nil || n != 0 {
		t.Fatalf("truncate = %d, %v", n, err)
	}

	if before < 3 || len(walFiles(t, root)) != before {
		t.Fatalf("files: %d before, %d after", before, len(walFiles(t, root)))
	}
}

func TestWALTruncateDeletesSealedOldFilesAndKeepsTheActiveOne(t *testing.T) {
	root := t.TempDir()
	c := openTrunc(t, root, "1h", collection.Config{})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	before := walFiles(t, root)

	n, err := c.TruncateWAL()
	if err != nil {
		t.Fatal(err)
	}

	after := walFiles(t, root)

	if n == 0 || len(after) != len(before)-n || len(after) == 0 {
		t.Fatalf("deleted %d, files %d -> %d", n, len(before), len(after))
	}

	if after[len(after)-1] != before[len(before)-1] {
		t.Fatalf("the active file must stay: %v -> %v", before, after)
	}
}

func TestWALTruncateKeepsFilesThatAreNotSealed(t *testing.T) {
	root := t.TempDir()
	c := openTrunc(t, root, "1h", collection.Config{})

	insertN(t, c, 0, 30)

	n, err := c.TruncateWAL()
	if err != nil || n != 0 {
		t.Fatalf("no checkpoint yet, deleted %d, %v", n, err)
	}
}

func TestWALTruncateKeepsFilesYoungerThanRetention(t *testing.T) {
	root := t.TempDir()
	c := openTrunc(t, root, "1h", collection.Config{Now: time.Now})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	n, err := c.TruncateWAL()
	if err != nil || n != 0 {
		t.Fatalf("young files, deleted %d, %v", n, err)
	}
}

func TestWALTruncateKeepsFilesAConsumerStillNeeds(t *testing.T) {
	root := t.TempDir()
	cursors := []string{"e3.1"}
	c := openTrunc(t, root, "1h", collection.Config{Cursors: func() ([]string, error) { return cursors, nil }})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	if _, err := c.TruncateWAL(); err != nil {
		t.Fatal(err)
	}

	s, err := c.Feed().Open("e3.1")
	if err != nil {
		t.Fatalf("a stored cursor must stay readable: %v", err)
	}

	defer s.Close()

	for i := 4; i <= 30; i++ {
		if ev := nextEv(t, s); ev.ID != fmt.Sprintf("c%04d", i-1) {
			t.Fatalf("event %d = %s", i, ev.ID)
		}
	}

	cursors = []string{"e25.1"}

	if n, err := c.TruncateWAL(); err != nil || n == 0 {
		t.Fatalf("cursor moved on, deleted %d, %v", n, err)
	}
}

func TestWatchFromTruncatedHistoryFailsWithAStableCode(t *testing.T) {
	root := t.TempDir()
	c := openTrunc(t, root, "1h", collection.Config{})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	if n, _ := c.TruncateWAL(); n == 0 {
		t.Fatal("nothing was truncated")
	}

	for _, from := range []string{"", "e2.1"} {
		_, err := c.Feed().Open(from)

		var fe *feed.Error

		if !errors.As(err, &fe) || fe.Code() != "history_truncated" {
			t.Fatalf("from %q: err = %v", from, err)
		}
	}

	s, err := c.Feed().Open(c.Feed().Now())
	if err != nil {
		t.Fatalf("now must work: %v", err)
	}

	s.Close()
}

func TestWALTruncateSparesFilesAnOpenStreamReads(t *testing.T) {
	root := t.TempDir()
	c := openTrunc(t, root, "1h", collection.Config{})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	s, err := c.Feed().Open("")
	if err != nil {
		t.Fatal(err)
	}

	if n, err := c.TruncateWAL(); err != nil || n != 0 {
		t.Fatalf("a reading stream pins the history, deleted %d, %v", n, err)
	}

	for i := 0; i < 30; i++ {
		if ev := nextEv(t, s); ev.ID != fmt.Sprintf("c%04d", i) {
			t.Fatalf("event %d = %s", i, ev.ID)
		}
	}

	s.Close()

	if n, err := c.TruncateWAL(); err != nil || n == 0 {
		t.Fatalf("stream closed, deleted %d, %v", n, err)
	}
}

func TestRecoveryWorksAfterTruncation(t *testing.T) {
	root := t.TempDir()
	c := openTrunc(t, root, "1h", collection.Config{})

	insertN(t, c, 0, 30)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	insertN(t, c, 30, 40)

	if n, _ := c.TruncateWAL(); n == 0 {
		t.Fatal("nothing was truncated")
	}

	want := rowsOf(t, c)

	c = reopenTrunc(t, c, root)

	sameRows(t, want, rowsOf(t, c))

	insertN(t, c, 40, 45)

	want = rowsOf(t, c)

	c = reopenTrunc(t, c, root)

	sameRows(t, want, rowsOf(t, c))
}

func reopenTrunc(t *testing.T, c *collection.Collection, root string) *collection.Collection {
	t.Helper()

	closeOnce(c)

	return openTrunc(t, root, "1h", collection.Config{})
}
