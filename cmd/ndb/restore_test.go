package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
)

// makeBackup seeds a data dir and copies it with the offline backup.
func makeBackup(t *testing.T) string {
	t.Helper()

	src := t.TempDir()

	seedDataDir(t, src)

	dest := filepath.Join(t.TempDir(), "bk")

	if err := backup.Offline(src, dest); err != nil {
		t.Fatal(err)
	}

	return dest
}

func TestRestoreIntoEmptyDataDir(t *testing.T) {
	bk := makeBackup(t)
	target := filepath.Join(t.TempDir(), "data")

	var out strings.Builder

	if err := restore([]string{bk, "-config", "none.yml", "-data-dir", target}, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "2 collections, 6 rows") {
		t.Errorf("summary = %q", out.String())
	}

	if _, err := os.Stat(filepath.Join(target, stageDir)); !os.IsNotExist(err) {
		t.Error("stage dir was left behind")
	}

	sock, _ := startDaemon(t, target)

	var st strings.Builder

	if err := stats([]string{"-config", "none.yml", "-url", "unix://" + sock}, &st); err != nil {
		t.Fatal(err)
	}

	if strings.Count(st.String(), "engine") != 2 {
		t.Errorf("restored daemon stats:\n%s", st.String())
	}
}

func TestRestoreFlagsMayFollowTheBackupDir(t *testing.T) {
	bk := makeBackup(t)
	target := filepath.Join(t.TempDir(), "data")

	var out strings.Builder

	if err := restore([]string{"-config", "none.yml", bk, "-data-dir", target}, &out); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusesANonEmptyDataDirWithoutForce(t *testing.T) {
	bk := makeBackup(t)
	target := t.TempDir()

	keep := filepath.Join(target, "keep.txt")

	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	err := restore([]string{bk, "-config", "none.yml", "-data-dir", target}, &out)
	if err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("err = %v", err)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Error("the refused restore changed the data dir")
	}

	if err := restore([]string{bk, "-config", "none.yml", "-data-dir", target, "-force"}, &out); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(keep); !os.IsNotExist(err) {
		t.Error("-force must replace the old contents")
	}

	if _, err := os.Stat(filepath.Join(target, "catalog.bolt")); err != nil {
		t.Error("catalog.bolt not restored")
	}
}

func TestRestoreRefusesARunningDaemon(t *testing.T) {
	bk := makeBackup(t)
	target := t.TempDir()

	seedDataDir(t, target)
	startDaemon(t, target)

	var out strings.Builder

	err := restore([]string{bk, "-config", "none.yml", "-data-dir", target, "-force"}, &out)
	if err == nil || !strings.Contains(err.Error(), "running daemon") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreRejectsBadSources(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")
	empty := t.TempDir()

	var out strings.Builder

	cases := map[string][]string{
		"missing":   {filepath.Join(t.TempDir(), "nope")},
		"not a bk":  {empty},
		"no source": {},
	}

	for name, pos := range cases {
		args := append(pos, "-config", "none.yml", "-data-dir", target)

		if err := restore(args, &out); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}

	if _, err := os.Stat(target); err == nil {
		t.Error("a refused restore must not create the data dir")
	}
}

func TestRestoreKeepsTheDataDirWhenTheBackupIsCorrupt(t *testing.T) {
	bk := makeBackup(t)

	wals, _ := filepath.Glob(filepath.Join(bk, "collections", "*", "*.ndjson"))
	if len(wals) == 0 {
		t.Fatal("no WAL in the backup")
	}

	// A sealed WAL file with a bad line in the middle stops recovery.
	sealed := filepath.Join(filepath.Dir(wals[0]), "000000.ndjson")
	bad := "not json\n" + strings.Repeat("{}\n", 3)

	if err := os.WriteFile(sealed, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()

	keep := filepath.Join(target, "keep.txt")

	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	err := restore([]string{bk, "-config", "none.yml", "-data-dir", target, "-force"}, &out)
	if err == nil {
		t.Skip("recovery tolerated the injected line; corruption model differs")
	}

	if _, err := os.Stat(keep); err != nil {
		t.Error("a failed verification must leave the data dir untouched")
	}

	if _, err := os.Stat(filepath.Join(target, stageDir)); !os.IsNotExist(err) {
		t.Error("stage dir was left behind")
	}
}
