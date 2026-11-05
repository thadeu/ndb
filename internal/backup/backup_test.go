package backup_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/lock"
)

func write(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func TestCopyFilePrefix(t *testing.T) {
	dir := t.TempDir()

	write(t, filepath.Join(dir, "a"), "0123456789")

	if err := backup.CopyFile(filepath.Join(dir, "a"), filepath.Join(dir, "sub", "b"), 4); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(dir, "sub", "b")); got != "0123" {
		t.Fatalf("got %q", got)
	}

	if err := backup.CopyFile(filepath.Join(dir, "a"), filepath.Join(dir, "c"), -1); err != nil {
		t.Fatal(err)
	}

	if got := read(t, filepath.Join(dir, "c")); got != "0123456789" {
		t.Fatalf("got %q", got)
	}
}

func TestPrepareDestRefusesAFullDir(t *testing.T) {
	dir := t.TempDir()

	write(t, filepath.Join(dir, "x"), "1")

	if err := backup.PrepareDest(dir); err == nil {
		t.Fatal("a dest with files must be refused")
	}

	if err := backup.PrepareDest(filepath.Join(dir, "new")); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineCopiesTreeButNotLockOrTmp(t *testing.T) {
	data := t.TempDir()
	dest := filepath.Join(t.TempDir(), "bk")

	write(t, filepath.Join(data, "catalog.bolt"), "cat")
	write(t, filepath.Join(data, "collections", "c", "000001.ndjson"), "wal")
	write(t, filepath.Join(data, "collections", "c", "segments", "000001.parquet"), "seg")
	write(t, filepath.Join(data, "blobs", "sha256", "ab", "cd", "abcd"), "blob")
	write(t, filepath.Join(data, "blobs", "tmp", "half"), "partial")

	if err := backup.Offline(data, dest); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"catalog.bolt", "collections/c/000001.ndjson", "collections/c/segments/000001.parquet", "blobs/sha256/ab/cd/abcd"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}

	for _, rel := range []string{"LOCK", "blobs/tmp/half"} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err == nil {
			t.Errorf("%s must not be copied", rel)
		}
	}
}

func TestOfflineFailsWhileADaemonHoldsTheLock(t *testing.T) {
	data := t.TempDir()

	lk, err := lock.Acquire(data)
	if err != nil {
		t.Fatal(err)
	}

	defer lk.Release()

	err = backup.Offline(data, filepath.Join(t.TempDir(), "bk"))

	if !errors.Is(err, lock.ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}
