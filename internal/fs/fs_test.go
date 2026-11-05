package fs_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/fs"
)

func TestOSFSWriteSyncRename(t *testing.T) {
	dir := t.TempDir()
	fsys := fs.OS{}

	tmp := filepath.Join(dir, "a.tmp")
	final := filepath.Join(dir, "a")

	f, err := fsys.Create(tmp)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := f.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}

	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}

	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := fsys.Rename(tmp, final); err != nil {
		t.Fatal(err)
	}

	if err := fsys.SyncDir(dir); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(final)
	if err != nil {
		t.Fatal(err)
	}

	if string(got) != "hello\n" {
		t.Fatalf("got %q", got)
	}
}

func TestOSFSOpenAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	fsys := fs.OS{}

	for _, line := range []string{"a\n", "b\n"} {
		f, err := fsys.OpenAppend(path)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := f.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}

		f.Close()
	}

	got, _ := os.ReadFile(path)

	if string(got) != "a\nb\n" {
		t.Fatalf("got %q", got)
	}
}

func TestFaultFSFailsSyncAtNthCall(t *testing.T) {
	fsys := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpSync, At: 2})

	f, err := fsys.Create(filepath.Join(t.TempDir(), "x"))
	if err != nil {
		t.Fatal(err)
	}

	defer f.Close()

	if err := f.Sync(); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	if err := f.Sync(); !errors.Is(err, fs.ErrInjected) {
		t.Fatalf("second sync: want ErrInjected, got %v", err)
	}
}

func TestFaultFSPartialWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x")
	fsys := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpWrite, At: 1, Partial: 3})

	f, err := fsys.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	n, err := f.Write([]byte("abcdef"))
	f.Close()

	if n != 3 || !errors.Is(err, fs.ErrInjected) {
		t.Fatalf("want n=3 ErrInjected, got n=%d err=%v", n, err)
	}

	got, _ := os.ReadFile(path)

	if string(got) != "abc" {
		t.Fatalf("got %q", got)
	}
}

func TestFaultFSFailsRename(t *testing.T) {
	dir := t.TempDir()
	fsys := fs.NewFault(fs.OS{}, fs.Fault{Op: fs.OpRename, At: 1})

	f, _ := fsys.Create(filepath.Join(dir, "a"))
	f.Close()

	err := fsys.Rename(filepath.Join(dir, "a"), filepath.Join(dir, "b"))

	if !errors.Is(err, fs.ErrInjected) {
		t.Fatalf("want ErrInjected, got %v", err)
	}
}
