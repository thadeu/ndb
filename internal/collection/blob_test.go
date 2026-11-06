package collection_test

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

var blobDef = catalog.Def{Name: "calls", BlobThreshold: 64}

func openBlobs(t *testing.T, fsys fs.FS, dir string) *blob.Store {
	t.Helper()

	s, err := blob.Open(fsys, filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}

	return s
}

func openWithBlobs(t *testing.T, walFS fs.FS, dir string, blobs *blob.Store) *collection.Collection {
	t.Helper()

	c, err := collection.Open(collection.Config{
		Dir:     filepath.Join(dir, "col"),
		FS:      walFS,
		Def:     blobDef,
		Blobs:   blobs,
		OnFatal: func(error) {},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { c.Close() })

	return c
}

func readBlob(t *testing.T, s *blob.Store, ref string) string {
	t.Helper()

	r, err := s.Get(ref)
	if err != nil {
		t.Fatal(err)
	}

	defer r.Close()

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func TestInsertAndUpdateExternalizeLargeStrings(t *testing.T) {
	dir := t.TempDir()
	blobs := openBlobs(t, fs.OS{}, dir)
	c := openWithBlobs(t, fs.OS{}, dir, blobs)

	big := strings.Repeat("a", 500)
	other := strings.Repeat("b", 700)

	if _, err := c.Insert(ctx, doc(`{"_id":"c1","note":"short","audio":"`+big+`"}`)); err != nil {
		t.Fatal(err)
	}

	got, ok := c.Find("c1")
	if !ok {
		t.Fatal("c1 missing")
	}

	if strings.Contains(string(got.Doc), big) || field(t, got.Doc, "note") != "short" {
		t.Fatalf("doc = %s", got.Doc)
	}

	refs := blob.Refs(got.Doc)

	if len(refs) != 1 || readBlob(t, blobs, refs[0]) != big {
		t.Fatalf("refs = %v", refs)
	}

	if _, err := c.UpdateByID(ctx, "c1", collection.Change{Set: map[string]any{"audio": other}}, 0); err != nil {
		t.Fatal(err)
	}

	got, _ = c.Find("c1")
	refs = blob.Refs(got.Doc)

	if len(refs) != 1 || readBlob(t, blobs, refs[0]) != other {
		t.Fatalf("after update refs = %v in %s", refs, got.Doc)
	}
}

func TestNoBlobsWithoutThreshold(t *testing.T) {
	dir := t.TempDir()
	blobs := openBlobs(t, fs.OS{}, dir)

	c, err := collection.Open(collection.Config{Dir: filepath.Join(dir, "col"), FS: fs.OS{}, Def: catalog.Def{Name: "p"}, Blobs: blobs})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	big := strings.Repeat("a", 500)

	if _, err := c.Insert(ctx, doc(`{"_id":"1","v":"`+big+`"}`)); err != nil {
		t.Fatal(err)
	}

	if count, _ := blobs.Stats(); count != 0 {
		t.Fatalf("blobs = %d", count)
	}
}

// The blob is durable before the WAL line is written. A crash in between
// leaves an unused blob and no document, so no reference can dangle.
func TestCrashBetweenBlobAndWALLeavesNoDanglingRef(t *testing.T) {
	dir := t.TempDir()
	blobs := openBlobs(t, fs.OS{}, dir)

	walFS := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpWrite, At: 1, Partial: 10})
	c := openWithBlobs(t, walFS, dir, blobs)

	big := strings.Repeat("z", 300)

	if _, err := c.Insert(ctx, doc(`{"_id":"c1","audio":"`+big+`"}`)); err == nil {
		t.Fatal("insert succeeded although the WAL write failed")
	}

	if count, _ := blobs.Stats(); count != 1 {
		t.Fatalf("blob must be durable before the WAL write: count = %d", count)
	}

	c.Close()

	again := openWithBlobs(t, fs.OS{}, dir, openBlobs(t, fs.OS{}, dir))

	if _, ok := again.Find("c1"); ok {
		t.Fatal("document survived a failed WAL write")
	}
}

// If the blob cannot be made durable, nothing reaches the WAL.
func TestBlobFailureWritesNothingToWAL(t *testing.T) {
	dir := t.TempDir()
	blobs := openBlobs(t, fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpSync, At: 1}), dir)

	walFS := fs.NewFault(fs.OS{})
	c := openWithBlobs(t, walFS, dir, blobs)

	big := strings.Repeat("z", 300)

	if _, err := c.Insert(ctx, doc(`{"_id":"c1","audio":"`+big+`"}`)); err == nil {
		t.Fatal("insert succeeded although the blob was not durable")
	}

	if n := walFS.Calls(fs.OpWrite); n != 0 {
		t.Fatalf("WAL writes = %d, want 0", n)
	}

	if _, ok := c.Find("c1"); ok {
		t.Fatal("document exists")
	}
}

func TestWALMarkIsARecordBoundaryAcrossRotation(t *testing.T) {
	dir := t.TempDir()

	c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: catalog.Def{Name: "p"}, WALSegmentSize: 512})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	for i := range 20 {
		if _, err := c.Insert(ctx, doc(`{"_id":"`+strings.Repeat("x", i+1)+`","pad":"`+strings.Repeat("p", 100)+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	m, err := c.WALMark()
	if err != nil {
		t.Fatal(err)
	}

	if m.Seq < 2 || m.Tx != 20 {
		t.Fatalf("mark = %+v, want a rotated WAL and tx 20", m)
	}

	info, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%06d.ndjson", m.Seq)))
	if err != nil {
		t.Fatal(err)
	}

	if info.Size() != m.Offset {
		t.Fatalf("offset %d, file size %d", m.Offset, info.Size())
	}
}
