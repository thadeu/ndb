package blob_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/fs"
)

func openStore(t *testing.T, fsys fs.FS) (*blob.Store, string) {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "blobs")

	s, err := blob.Open(fsys, dir)
	if err != nil {
		t.Fatal(err)
	}

	return s, dir
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)

	return hex.EncodeToString(sum[:])
}

func TestPutAndGet(t *testing.T) {
	s, dir := openStore(t, fs.OS{})
	data := []byte("hello blob")

	ref, err := s.Put(bytes.NewReader(data), "text/plain", 0)
	if err != nil {
		t.Fatal(err)
	}

	h := hashOf(data)

	if ref.Blob != "sha256:"+h || ref.Size != int64(len(data)) || ref.Type != "text/plain" {
		t.Fatalf("ref = %+v", ref)
	}

	want := filepath.Join(dir, "sha256", h[:2], h[2:4], h)

	if _, err := os.Stat(want); err != nil {
		t.Fatalf("blob file not at %s: %v", want, err)
	}

	r, err := s.Get(ref.Blob)
	if err != nil {
		t.Fatal(err)
	}

	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, data) || r.Size() != int64(len(data)) {
		t.Fatalf("got %q size %d", got, r.Size())
	}
}

func TestGetRange(t *testing.T) {
	s, _ := openStore(t, fs.OS{})

	ref, err := s.Put(strings.NewReader("0123456789"), "", 0)
	if err != nil {
		t.Fatal(err)
	}

	r, err := s.Get(ref.Blob)
	if err != nil {
		t.Fatal(err)
	}

	defer r.Close()

	if _, err := r.Seek(3, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	got := make([]byte, 4)

	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatal(err)
	}

	if string(got) != "3456" {
		t.Fatalf("got %q", got)
	}
}

func TestSameContentStoredOnce(t *testing.T) {
	s, _ := openStore(t, fs.OS{})

	for range 3 {
		if _, err := s.Put(strings.NewReader("same"), "", 0); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.Put(strings.NewReader("other"), "", 0); err != nil {
		t.Fatal(err)
	}

	count, size := s.Stats()

	if count != 2 || size != int64(len("same")+len("other")) {
		t.Fatalf("count %d size %d", count, size)
	}
}

func TestStatsSurviveReopen(t *testing.T) {
	s, dir := openStore(t, fs.OS{})

	if _, err := s.Put(strings.NewReader("abc"), "", 0); err != nil {
		t.Fatal(err)
	}

	again, err := blob.Open(fs.OS{}, dir)
	if err != nil {
		t.Fatal(err)
	}

	if count, size := again.Stats(); count != 1 || size != 3 {
		t.Fatalf("count %d size %d", count, size)
	}
}

func TestPutTooLarge(t *testing.T) {
	s, dir := openStore(t, fs.OS{})

	_, err := s.Put(strings.NewReader("0123456789"), "", 5)

	if !errors.Is(err, blob.ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}

	if count, _ := s.Stats(); count != 0 {
		t.Fatalf("count = %d", count)
	}

	if left, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(left) != 0 {
		t.Fatalf("tmp files left: %d", len(left))
	}
}

func TestBadRef(t *testing.T) {
	s, _ := openStore(t, fs.OS{})

	for _, ref := range []string{"", "sha256:zz", "md5:abcd", "../../etc/passwd", "sha256:" + strings.Repeat("A", 64)} {
		if _, err := s.Get(ref); !errors.Is(err, blob.ErrBadRef) {
			t.Errorf("Get(%q) = %v, want ErrBadRef", ref, err)
		}
	}

	if _, err := s.Get("sha256:" + strings.Repeat("a", 64)); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("missing blob = %v, want ErrNotFound", err)
	}
}

// A failed write step never leaves a blob at its final path.
func TestFaultsNeverPublishPartialBlob(t *testing.T) {
	for _, op := range []fs.Op{fs.OpWrite, fs.OpSync, fs.OpRename} {
		ff := fs.NewFault(fs.OS{}, fs.Fault{Op: op, At: 1, Partial: 2})
		s, dir := openStore(t, ff)

		if _, err := s.Put(strings.NewReader("payload"), "", 0); err == nil {
			t.Fatalf("op %v: Put succeeded", op)
		}

		if _, err := s.Get("sha256:" + hashOf([]byte("payload"))); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("op %v: Get = %v, want ErrNotFound", op, err)
		}

		again, err := blob.Open(fs.OS{}, dir)
		if err != nil {
			t.Fatal(err)
		}

		if left, _ := os.ReadDir(filepath.Join(dir, "tmp")); len(left) != 0 {
			t.Fatalf("op %v: tmp files after reopen: %d", op, len(left))
		}

		if count, _ := again.Stats(); count != 0 {
			t.Fatalf("op %v: count = %d", op, count)
		}
	}
}

func age(t *testing.T, dir, hash string, by time.Duration) {
	t.Helper()

	p := filepath.Join(dir, "sha256", hash[:2], hash[2:4], hash)
	old := time.Now().Add(-by)

	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

func seq(hashes ...string) iter.Seq[string] {
	return slices.Values(hashes)
}

func TestGCDeletesOnlyOldUnreferenced(t *testing.T) {
	s, dir := openStore(t, fs.OS{})

	put := func(content string) string {
		ref, err := s.Put(strings.NewReader(content), "", 0)
		if err != nil {
			t.Fatal(err)
		}

		return strings.TrimPrefix(ref.Blob, "sha256:")
	}

	kept := put("referenced")
	fresh := put("fresh but unreferenced")
	stale := put("stale and unreferenced")

	age(t, dir, kept, 48*time.Hour)
	age(t, dir, stale, 48*time.Hour)

	res, err := s.GC(seq(kept), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if res.Deleted != 1 || res.Bytes != int64(len("stale and unreferenced")) {
		t.Fatalf("result = %+v", res)
	}

	if !s.Has(kept) || !s.Has(fresh) || s.Has(stale) {
		t.Fatalf("kept=%v fresh=%v stale=%v", s.Has(kept), s.Has(fresh), s.Has(stale))
	}

	if count, _ := s.Stats(); count != 2 {
		t.Fatalf("count = %d", count)
	}
}

// Storing the content again renews its age, so a GC that scanned the
// documents before the new reference was written does not delete it.
func TestPutAgainRenewsGraceForGC(t *testing.T) {
	s, dir := openStore(t, fs.OS{})

	ref, err := s.Put(strings.NewReader("again"), "", 0)
	if err != nil {
		t.Fatal(err)
	}

	h := strings.TrimPrefix(ref.Blob, "sha256:")

	age(t, dir, h, 48*time.Hour)

	if _, err := s.Put(strings.NewReader("again"), "", 0); err != nil {
		t.Fatal(err)
	}

	res, err := s.GC(seq(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if res.Deleted != 0 || !s.Has(h) {
		t.Fatalf("result = %+v, has = %v", res, s.Has(h))
	}
}

func TestRefsFindsBlobsAnywhere(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("b", 64)
	doc := `{"id":"1","rec":{"$blob":"sha256:` + a + `","size":3},"deep":{"list":[{"x":{"$blob":"sha256:` + b + `"}}]},` +
		`"note":"$blob","bad":{"$blob":"sha256:xyz"},"num":{"$blob":5}}`

	got := blob.Refs([]byte(doc))

	slices.Sort(got)

	if !slices.Equal(got, []string{a, b}) {
		t.Fatalf("refs = %v", got)
	}

	if len(blob.Refs([]byte(`{"a":1}`))) != 0 || len(blob.Refs([]byte(`not json $blob`))) != 0 {
		t.Fatal("refs found in a document without blobs")
	}
}

func TestExternalize(t *testing.T) {
	s, _ := openStore(t, fs.OS{})
	big := strings.Repeat("x", 100)
	put := func(v string) (blob.Ref, error) { return s.Put(strings.NewReader(v), "text/plain", 0) }

	small := []byte(`{"id":"1","n":"short"}`)

	out, err := blob.Externalize(small, 50, put)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(out, small) {
		t.Fatalf("small doc changed: %s", out)
	}

	doc := []byte(`{"id":"1","audio":"` + big + `","nested":{"list":["` + big + `","ok"]},"` + big + `":1}`)

	out, err = blob.Externalize(doc, 50, put)
	if err != nil {
		t.Fatal(err)
	}

	refs := blob.Refs(out)

	if len(refs) != 2 || refs[0] != hashOf([]byte(big)) {
		t.Fatalf("refs = %v in %s", refs, out)
	}

	if !strings.Contains(string(out), `"`+big+`":1`) || !strings.Contains(string(out), `"ok"`) {
		t.Fatalf("keys and short values must stay: %s", out)
	}

	if count, _ := s.Stats(); count != 1 {
		t.Fatalf("same content must be stored once, count = %d", count)
	}
}

// With a hold (PITR), an old unreferenced blob goes only on a GC that runs
// the hold after the GC that first found it unreferenced.
func TestGCHoldKeepsANewOrphanForTheHold(t *testing.T) {
	s, dir := openStore(t, fs.OS{})

	ref, err := s.Put(strings.NewReader("orphan"), "", 0)
	if err != nil {
		t.Fatal(err)
	}

	h := strings.TrimPrefix(ref.Blob, "sha256:")

	age(t, dir, h, 48*time.Hour)
	s.SetHold(20 * time.Millisecond)

	if res, err := s.GC(seq(), 24*time.Hour); err != nil || res.Deleted != 0 {
		t.Fatalf("first gc = %+v, %v (the orphan must wait for the hold)", res, err)
	}

	time.Sleep(30 * time.Millisecond)

	if res, err := s.GC(seq(), 24*time.Hour); err != nil || res.Deleted != 1 {
		t.Fatalf("gc after the hold = %+v, %v", res, err)
	}
}
