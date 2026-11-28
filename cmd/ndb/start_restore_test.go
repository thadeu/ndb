package main

import (
	"bytes"
	"compress/gzip"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

// backupOf is a gzip backup of a data dir with one document in "calls".
func backupOf(t *testing.T, id string) []byte {
	t.Helper()

	dir := t.TempDir()
	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	col := mustEnsure(t, db, catalog.Def{Name: "calls"})

	if _, err := col.Insert(t.Context(), []byte(`{"_id":"`+id+`"}`)); err != nil {
		t.Fatal(err)
	}

	var plain bytes.Buffer

	if err := db.Backup(t.Context(), &plain, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer

	gz := gzip.NewWriter(&out)
	gz.Write(plain.Bytes())
	gz.Close()

	return out.Bytes()
}

func callIDs(t *testing.T, dir string) string {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	col, err := db.Get("calls")
	if err != nil {
		return ""
	}

	page, err := col.Find(t.Context(), []byte(`{"limit":10}`))
	if err != nil {
		t.Fatal(err)
	}

	var ids []string

	for _, row := range page.Rows {
		_, rest, _ := strings.Cut(string(row), `"_id":"`)
		id, _, _ := strings.Cut(rest, `"`)
		ids = append(ids, id)
	}

	return strings.Join(ids, ",")
}

// NDB_RESTORE_URL with a new NDB_RESTORE_ID restores the backup into the data
// dir before the daemon opens it, also over old data. The same id again does
// nothing, so a restart never restores twice.
func TestStartRestore(t *testing.T) {
	files := map[string][]byte{"/a": backupOf(t, "a1"), "/b": backupOf(t, "b1")}
	gets := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets++

		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}

		w.Write(body)
	}))

	defer ts.Close()

	dir := filepath.Join(t.TempDir(), "data")
	env := func(url, id string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			switch key {
			case "NDB_RESTORE_URL":
				return url, url != ""
			case "NDB_RESTORE_ID":
				return id, id != ""
			}

			return "", false
		}
	}

	log := slog.New(slog.DiscardHandler)

	if err := startRestore(dir, env("", ""), log); err != nil {
		t.Fatalf("no restore: %v", err)
	}

	if err := startRestore(dir, env(ts.URL+"/a", "bk-1"), log); err != nil {
		t.Fatalf("restore into an empty data dir: %v", err)
	}

	if got := callIDs(t, dir); got != "a1" {
		t.Fatalf("after bk-1: %q", got)
	}

	if err := startRestore(dir, env(ts.URL+"/a", "bk-1"), log); err != nil || gets != 1 {
		t.Fatalf("the same id again: %v, %d gets", err, gets)
	}

	if err := startRestore(dir, env(ts.URL+"/b", "bk-2"), log); err != nil {
		t.Fatalf("restore over data: %v", err)
	}

	if got := callIDs(t, dir); got != "b1" {
		t.Fatalf("after bk-2: %q", got)
	}

	if err := startRestore(dir, env(ts.URL+"/missing", "bk-3"), log); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("a missing file: %v", err)
	}

	if got := callIDs(t, dir); got != "b1" {
		t.Fatalf("a failed restore changed the data: %q", got)
	}

	if err := startRestore(dir, env(ts.URL+"/a", ""), log); err == nil {
		t.Fatal("NDB_RESTORE_URL without NDB_RESTORE_ID must fail")
	}

	if _, err := os.Stat(filepath.Join(dir, restoreMark)); err != nil {
		t.Fatalf("the mark: %v", err)
	}
}
