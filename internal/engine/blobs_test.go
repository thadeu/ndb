package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
)

func ageAll(t *testing.T, dir string) {
	t.Helper()

	old := time.Now().Add(-48 * time.Hour)

	err := filepath.WalkDir(filepath.Join(dir, "blobs", "sha256"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		return os.Chtimes(p, old, old)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGCBlobsKeepsLiveDocumentsBlobs(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))
	col := ensure(t, db, catalog.Def{Name: "calls", BlobThreshold: 64})

	keep := strings.Repeat("k", 200)
	drop := strings.Repeat("d", 200)

	if _, err := col.Insert(ctx, []byte(`{"_id":"keep","audio":"`+keep+`"}`), []byte(`{"_id":"drop","audio":"`+drop+`"}`)); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Delete(ctx, []byte(`{"_id":"drop"}`), false); err != nil {
		t.Fatal(err)
	}

	orphan, err := db.Blobs().Put(strings.NewReader("never referenced"), "", 0)
	if err != nil {
		t.Fatal(err)
	}

	if count, _ := db.Blobs().Stats(); count != 3 {
		t.Fatalf("blobs before = %d", count)
	}

	young, err := db.GCBlobs(ctx, blob.DefaultGrace)
	if err != nil || young.Deleted != 0 {
		t.Fatalf("young blobs must stay: %+v %v", young, err)
	}

	ageAll(t, dir)

	res, err := db.GCBlobs(ctx, blob.DefaultGrace)
	if err != nil {
		t.Fatal(err)
	}

	if res.Deleted != 2 || db.Blobs().Has(orphan.Hash()) {
		t.Fatalf("result = %+v", res)
	}

	page, err := col.Find(ctx, []byte(`{"where":{"_id":"keep"}}`))
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("find: %v %v", page.Rows, err)
	}

	refs := blob.Refs(page.Rows[0])

	if len(refs) != 1 || !db.Blobs().Has(refs[0]) {
		t.Fatalf("the live document lost its blob: %v", refs)
	}
}

func TestStatsCarryMetricsFields(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "posts"})

	if _, err := col.Insert(ctx, []byte(`{"_id":"p1"}`)); err != nil {
		t.Fatal(err)
	}

	st := col.Stats()

	if st.Writer == nil || st.Writer.Records.Load() != 1 {
		t.Fatalf("writer metrics = %+v", st.Writer)
	}

	if st.MemBytes <= 0 {
		t.Fatalf("mem bytes = %d", st.MemBytes)
	}
}

func TestGCBlobsKeepsBlobsOfKeptWALRecords(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))
	col := ensure(t, db, catalog.Def{Name: "calls", BlobThreshold: 64, WALRetention: "7d"})

	if _, err := col.Insert(ctx, []byte(`{"_id":"gone","audio":"`+strings.Repeat("g", 200)+`"}`)); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Delete(ctx, []byte(`{"_id":"gone"}`), false); err != nil {
		t.Fatal(err)
	}

	orphan, err := db.Blobs().Put(strings.NewReader("never referenced"), "", 0)
	if err != nil {
		t.Fatal(err)
	}

	ageAll(t, dir)

	res, err := db.GCBlobs(ctx, blob.DefaultGrace)
	if err != nil {
		t.Fatal(err)
	}

	if res.Deleted != 1 || db.Blobs().Has(orphan.Hash()) {
		t.Fatalf("only the unreferenced blob goes: %+v", res)
	}

	if count, _ := db.Blobs().Stats(); count != 1 {
		t.Fatalf("the blob of the kept WAL record must stay, blobs = %d", count)
	}
}
