package collection_test

import (
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

// history writes a history with updates, deletes and re-inserts that cross
// segments. seal puts the memtable in a segment after each round.
func history(t *testing.T, c *collection.Collection, seal bool) {
	t.Helper()

	cut := func() {
		if seal {
			if err := c.Seal(); err != nil {
				t.Fatal(err)
			}
		}
	}

	for round := range 6 {
		insertN(t, c, round*30, round*30+30)
		cut()

		for i := 0; i < round*30; i += 7 {
			if _, ok := c.Find(fmt.Sprintf("c%04d", i)); ok {
				update(t, c, fmt.Sprintf("c%04d", i), fmt.Sprintf("u%d", round))
			}
		}

		cut()

		for i := 0; i < round*30; i += 11 {
			if _, ok := c.Find(fmt.Sprintf("c%04d", i)); ok {
				remove(t, c, fmt.Sprintf("c%04d", i))
			}
		}

		cut()
	}

	for i := 0; i < 180; i += 22 {
		if _, ok := c.Find(fmt.Sprintf("c%04d", i)); !ok {
			d := fmt.Sprintf(`{"_id":"c%04d","status":"back","n":%d}`, i, i+1000)

			if _, err := c.Insert(ctx, doc(d)); err != nil {
				t.Fatal(err)
			}
		}
	}

	insertN(t, c, 500, 510)
}

func segmentFiles(t *testing.T, root string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(root, "segments"))
	if err != nil {
		t.Fatal(err)
	}

	var out []string

	parquet := map[string]bool{}

	for _, e := range entries {
		base, ext, _ := strings.Cut(e.Name(), ".")

		if ext == "parquet" {
			out = append(out, e.Name())
			parquet[base] = true
		}
	}

	for _, e := range entries {
		if base, _, _ := strings.Cut(e.Name(), "."); !parquet[base] {
			t.Fatalf("key file %s has no segment", e.Name())
		}
	}

	return out
}

func TestCompactKeepsTheResults(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour})
	twin := openMem(t)

	history(t, c, true)
	history(t, twin, false)

	if c.Segments() < 10 {
		t.Fatalf("the history made only %d segments", c.Segments())
	}

	before := rowsOf(t, c)

	if !reflect.DeepEqual(before, rowsOf(t, twin)) {
		t.Fatal("the sealed collection already differs from the memtable twin")
	}

	if err := c.Compact(); err != nil {
		t.Fatal(err)
	}

	if c.Segments() != 1 {
		t.Fatalf("segments after compaction = %d", c.Segments())
	}

	if got := rowsOf(t, c); !reflect.DeepEqual(got, before) {
		t.Fatalf("results changed:\n got %v\nwant %v", got, before)
	}

	if files := segmentFiles(t, root); len(files) != 1 {
		t.Fatalf("files left: %v", files)
	}

	if collection.SegmentRows(c) != c.Len()-collection.MemDocs(c) {
		t.Fatalf("the segment holds %d rows for %d sealed docs", collection.SegmentRows(c), c.Len()-collection.MemDocs(c))
	}

	c = reopen(t, c, root, collection.Config{SealAge: time.Hour})

	if got := rowsOf(t, c); !reflect.DeepEqual(got, before) {
		t.Fatal("results changed after a restart")
	}
}

func TestCompactARunKeepsTombstonesOfOlderSegments(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour})

	insertN(t, c, 0, 10)
	mustSeal(t, c)

	for i := range 5 {
		remove(t, c, fmt.Sprintf("c%04d", i))
	}

	mustSeal(t, c)
	insertN(t, c, 10, 12)
	mustSeal(t, c)
	insertN(t, c, 12, 14)
	mustSeal(t, c)

	before := rowsOf(t, c)

	if err := c.CompactRange(1, 4); err != nil {
		t.Fatal(err)
	}

	if c.Segments() != 2 {
		t.Fatalf("segments = %d", c.Segments())
	}

	c = reopen(t, c, root, collection.Config{SealAge: time.Hour})

	if got := rowsOf(t, c); !reflect.DeepEqual(got, before) {
		t.Fatalf("a deleted doc came back:\n got %v\nwant %v", got, before)
	}

	if c.Len() != 9 {
		t.Fatalf("len = %d, want 9", c.Len())
	}
}

func mustSeal(t *testing.T, c *collection.Collection) {
	t.Helper()

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactKeepsOldSegmentsForARunningQuery(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour})

	insertN(t, c, 0, 10)
	mustSeal(t, c)
	insertN(t, c, 10, 20)
	mustSeal(t, c)
	insertN(t, c, 20, 30)
	mustSeal(t, c)

	release := collection.HoldSegments(c)

	if err := c.Compact(); err != nil {
		t.Fatal(err)
	}

	if files := segmentFiles(t, root); len(files) != 4 {
		t.Fatalf("the old files must stay while a query holds them: %v", files)
	}

	release()

	if files := segmentFiles(t, root); len(files) != 1 {
		t.Fatalf("the old files must go with the last query: %v", files)
	}
}

func TestSnapshotCursorSurvivesCompaction(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour})

	for i := range 4 {
		insertN(t, c, i*10, i*10+10)
		mustSeal(t, c)
	}

	body := `{"sort":{"n":1},"limit":15,"snapshot":true}`
	q, _ := c.ParseQuery([]byte(body))

	first, err := c.Query(ctx, q)
	if err != nil || first.Next == "" {
		t.Fatalf("first page: %v next=%q", err, first.Next)
	}

	if err := c.Compact(); err != nil {
		t.Fatal(err)
	}

	next := fmt.Sprintf(`{"sort":{"n":1},"limit":15,"snapshot":true,"after":%q}`, first.Next)
	q, _ = c.ParseQuery([]byte(next))

	second, err := c.Query(ctx, q)
	if err != nil || len(second.Rows) != 15 {
		t.Fatalf("second page: %d rows, %v", len(second.Rows), err)
	}

	if !strings.Contains(string(second.Rows[0]), `"n":15`) {
		t.Fatalf("second page starts at %s", second.Rows[0])
	}
}

// TestCompactCrashImages copies the data dir at each step of a compaction, as
// a process kill would leave it, and opens every copy.
func TestCompactCrashImages(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour})

	history(t, c, true)

	want := rowsOf(t, c)
	images := map[string]string{}

	collection.SetCompactHook(c, func(step string) {
		dst := filepath.Join(t.TempDir(), "image")

		if err := copyDir(root, dst); err != nil {
			t.Error(err)
		}

		images[step] = dst
	})

	if err := c.Compact(); err != nil {
		t.Fatal(err)
	}

	if len(images) < 3 {
		t.Fatalf("steps seen: %v", images)
	}

	for step, dir := range images {
		img := openSealedAt(t, dir)

		if got := rowsOf(t, img); !reflect.DeepEqual(got, want) {
			t.Errorf("image after %q differs", step)
		}

		if files := segmentFiles(t, dir); len(files) != img.Segments() {
			t.Errorf("image after %q: %d files for %d live segments (orphans must go at open)", step, len(files), img.Segments())
		}
	}
}

func openSealedAt(t *testing.T, dir string) *collection.Collection {
	t.Helper()

	c, err := collection.Open(collection.Config{Dir: filepath.Join(dir, "wal"), Data: dir, FS: fs.OS{}, Def: sealDef, SealAge: time.Hour})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	return c
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(target, data, 0o644)
	})
}

func TestCompactFailureLeavesTheCollectionWorking(t *testing.T) {
	root := t.TempDir()
	// A seal renames 3 files (segment, ids, index of status): the 10th rename
	// is the first one of the compaction.
	faulty := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpRename, At: 10})
	c := openSealed(t, root, collection.Config{FS: faulty, SealAge: time.Hour})

	for i := range 3 {
		insertN(t, c, i*10, i*10+10)
		mustSeal(t, c)
	}

	before := rowsOf(t, c)

	if err := c.Compact(); err == nil {
		t.Fatal("the injected fault did not fail the compaction")
	}

	if got := rowsOf(t, c); !reflect.DeepEqual(got, before) {
		t.Fatal("a failed compaction changed the results")
	}

	if err := c.Compact(); err != nil {
		t.Fatalf("the retry failed: %v", err)
	}

	if got := rowsOf(t, c); !reflect.DeepEqual(got, before) {
		t.Fatal("results changed after the retry")
	}
}

func TestCompactRunsOnTheSealerTick(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour, SealTick: 5 * time.Millisecond, CompactMin: 3})

	for i := range 3 {
		insertN(t, c, i*5, i*5+5)
		mustSeal(t, c)
	}

	deadline := time.Now().Add(5 * time.Second)

	for c.Segments() > 1 {
		if time.Now().After(deadline) {
			t.Fatalf("segments = %d: the sealer did not compact", c.Segments())
		}

		time.Sleep(10 * time.Millisecond)
	}

	if c.Len() != 15 {
		t.Fatalf("len = %d", c.Len())
	}
}

func TestOnCompactSeesTheLiveDocs(t *testing.T) {
	root := t.TempDir()
	seen := map[string]bool{}

	hook := func(live iter.Seq[[]byte]) {
		for d := range live {
			seen[string(d)] = true
		}
	}

	c := openSealed(t, root, collection.Config{SealAge: time.Hour, OnCompact: hook})

	insertN(t, c, 0, 5)
	mustSeal(t, c)
	remove(t, c, "c0000")
	insertN(t, c, 5, 8)

	if err := c.Compact(); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 7 {
		t.Fatalf("the hook saw %d live docs, want 7 (sealed and memtable): %v", len(seen), seen)
	}
}

func TestCompactDropsExpiredAndOutOfWindowRows(t *testing.T) {
	clk := newClock()
	root := t.TempDir()
	def := catalog.Def{Name: "logs", Time: "at", Retention: "7d", Indexes: []string{"status"}}
	cfg := collection.Config{Dir: filepath.Join(root, "wal"), Data: root, FS: fs.OS{}, Def: def, Now: clk.now, SealAge: time.Hour}

	c, err := collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	for i := range 3 {
		d := fmt.Sprintf(`{"_id":"old%d","at":%q}`, i, rfc(clk.now().Add(-10*24*time.Hour)))

		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	mustSeal(t, c)

	for i := range 2 {
		d := fmt.Sprintf(`{"_id":"new%d","at":%q}`, i, rfc(clk.now().Add(-time.Hour)))

		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	mustSeal(t, c)

	if c.Len() != 5 {
		t.Fatalf("len = %d", c.Len())
	}

	if err := c.Compact(); err != nil {
		t.Fatal(err)
	}

	if c.Len() != 2 || collection.SegmentRows(c) != 2 {
		t.Fatalf("len = %d, segment rows = %d: the old rows must leave the derived data", c.Len(), collection.SegmentRows(c))
	}

	if n := countOf(t, c, `{}`); n != 2 {
		t.Fatalf("find = %d", n)
	}

	c.Close()

	c, err = collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	if c.Len() != 2 {
		t.Fatalf("after a restart len = %d", c.Len())
	}
}

func TestSealerDropsAWholeSegmentPastTheRetention(t *testing.T) {
	clk := newClock()
	root := t.TempDir()
	def := catalog.Def{Name: "logs", Time: "at", Retention: "1d"}

	c, err := collection.Open(collection.Config{
		Dir: filepath.Join(root, "wal"), Data: root, FS: fs.OS{}, Def: def, Now: clk.now, SealAge: time.Hour, SealTick: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	for i := range 2 {
		d := fmt.Sprintf(`{"_id":"a%d","at":%q}`, i, rfc(clk.now().Add(-time.Hour)))

		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	mustSeal(t, c)
	clk.advance(48 * time.Hour)

	deadline := time.Now().Add(5 * time.Second)

	for c.Segments() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("segments = %d: the retention did not drop the segment", c.Segments())
		}

		time.Sleep(10 * time.Millisecond)
	}

	if c.Len() != 0 {
		t.Fatalf("len = %d", c.Len())
	}
}

func TestExpiredRowsAreDroppedByCompaction(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, true)

	exp := rfc(clk.now().Add(time.Minute))

	for _, d := range []string{
		fmt.Sprintf(`{"_id":"soon","exp":%q}`, exp),
		`{"_id":"never"}`,
	} {
		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	mustSeal(t, c)
	clk.advance(time.Hour)

	if err := c.Compact(); err != nil {
		t.Fatal(err)
	}

	if collection.SegmentRows(c) != 1 || c.Len() != 1 {
		t.Fatalf("segment rows = %d, len = %d", collection.SegmentRows(c), c.Len())
	}
}

func TestCompactWhileReadingAndWriting(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour})

	insertN(t, c, 0, 60)
	mustSeal(t, c)

	stop := make(chan struct{})
	done := make(chan error, 2)

	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				done <- nil

				return
			default:
			}

			ch := collection.Change{Set: map[string]any{"status": fmt.Sprintf("w%d", i)}}

			if _, err := c.UpdateByID(ctx, fmt.Sprintf("c%04d", i%60), ch, 0); err != nil {
				done <- err

				return
			}
		}
	}()

	go func() {
		for {
			select {
			case <-stop:
				done <- nil

				return
			default:
			}

			q, _ := c.ParseQuery([]byte(`{"query":{"n":{"$gte":0}}}`))

			page, err := c.Query(ctx, q)
			if err != nil || len(page.Rows) != 60 {
				done <- fmt.Errorf("query: %d rows, %v", len(page.Rows), err)

				return
			}
		}
	}()

	for range 12 {
		mustSeal(t, c)

		if err := c.Compact(); err != nil {
			t.Fatal(err)
		}
	}

	close(stop)

	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	if c.Len() != 60 {
		t.Fatalf("len = %d", c.Len())
	}

	mustSeal(t, c)

	before := rowsOf(t, c)
	c = reopen(t, c, root, collection.Config{SealAge: time.Hour})

	if after := rowsOf(t, c); !reflect.DeepEqual(after, before) {
		for i := range before {
			if i < len(after) && before[i] != after[i] {
				t.Logf("probe %d\nbefore %.400s\nafter  %.400s", i, before[i], after[i])
			}
		}

		t.Fatal("results changed after a restart")
	}
}

// A seal does not wait for a compaction: it commits while the compaction is
// still building its segment, and both end with the right rows.
func TestSealRunsWhileACompactionBuilds(t *testing.T) {
	root := t.TempDir()
	c := openSealed(t, root, collection.Config{SealAge: time.Hour})

	for i := range 4 {
		insertN(t, c, i*20, i*20+20)
		mustSeal(t, c)
	}

	paused, resume := make(chan struct{}), make(chan struct{})

	var once sync.Once

	collection.SetCompactHook(c, func(step string) {
		if step == "published" {
			once.Do(func() {
				close(paused)
				<-resume
			})
		}
	})

	done := make(chan error, 1)

	go func() { done <- c.Compact() }()

	<-paused

	insertN(t, c, 80, 100)
	update(t, c, "c0005", "moved")
	remove(t, c, "c0010")

	sealed := make(chan error, 1)

	go func() { sealed <- c.Seal() }()

	select {
	case err := <-sealed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the seal waited for the compaction")
	}

	close(resume)

	if err := <-done; err != nil {
		t.Fatal(err)
	}

	want := rowsOf(t, c)

	if n := c.Len(); n != 99 {
		t.Fatalf("len = %d, want 99", n)
	}

	if _, ok := c.Get("c0010"); ok {
		t.Fatal("a deleted row came back")
	}

	c = reopen(t, c, root, collection.Config{SealAge: time.Hour})

	sameRows(t, want, rowsOf(t, c))
}
