package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

const (
	smallBlob = 300
	bigBlob   = 2*1024*1024 + 512*1024
)

// seedRich fills dir with every kind of data a backup carries: puts, an
// update, a delete, an expiry, a unique index, a watch consumer with an acked
// position, a small and a multi-part blob, and a direct-mode collection.
func seedRich(t *testing.T, dir string) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	users := mustEnsure(t, db, catalog.Def{Name: "users", Indexes: []string{"age"}, Unique: []string{"email"}, ExpiresField: "expires_at"})

	for i := 1; i <= 4; i++ {
		doc := fmt.Sprintf(`{"_id":"u%d","email":"u%d@x","age":%d}`, i, i, i)

		if _, err := users.Insert(ctx, []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := users.UpdateID(ctx, "u2", []byte(`{"$set":{"age":99}}`), collection.UpdateOpts{}); err != nil {
		t.Fatal(err)
	}

	if _, err := users.DeleteID(ctx, "u3"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := users.Expire(ctx, "u4", collection.Expiry{In: time.Hour}); err != nil {
		t.Fatal(err)
	}

	stream, err := db.Watch("users", "mailer", "beginning")
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		ev, err := stream.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}

		if ev.ID == "u3" && ev.Op != "put" {
			t.Fatalf("unexpected event %+v", ev)
		}

		if err := db.Ack("users", "mailer", ev.Pos); err != nil {
			t.Fatal(err)
		}
	}

	stream.Close()

	calls := mustEnsure(t, db, catalog.Def{Name: "calls", BlobThreshold: 64})

	for id, size := range map[string]int{"c1": smallBlob, "c2": bigBlob} {
		doc := fmt.Sprintf(`{"_id":"%s","audio":"%s"}`, id, strings.Repeat("a", size))

		if _, err := calls.Insert(ctx, []byte(doc)); err != nil {
			t.Fatal(err)
		}
	}

	files := t.TempDir()

	if err := os.WriteFile(filepath.Join(files, "a.ndjson"), []byte(`{"_id":"x1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mustEnsure(t, db, catalog.Def{Name: "ext", Write: catalog.Direct, Path: filepath.Join(files, "*.ndjson")})

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mustEnsure(t *testing.T, db *engine.DB, def catalog.Def) *engine.Collection {
	t.Helper()

	if _, err := db.Ensure(def); err != nil {
		t.Fatal(err)
	}

	col, err := db.Get(def.Name)
	if err != nil {
		t.Fatal(err)
	}

	return col
}

// checkRich opens dir like a daemon and checks what seedRich wrote. withBlobs
// tells whether the blob content must be there.
func checkRich(t *testing.T, dir string, withBlobs bool) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	users, err := db.Get("users")
	if err != nil {
		t.Fatal(err)
	}

	wantV := map[string]uint64{"u1": 1, "u2": 2}

	for id, v := range wantV {
		if d, ok := users.Get(id); !ok || d.V != v {
			t.Errorf("%s: got %+v ok=%v, want _v %d", id, d, ok, v)
		}
	}

	if _, ok := users.Get("u3"); ok {
		t.Error("u3 was deleted before the backup")
	}

	page, err := users.Find(ctx, []byte(`{"where":{"age":99}}`))
	if err != nil || len(page.Rows) != 1 || !strings.Contains(string(page.Rows[0]), `"u2"`) {
		t.Errorf("index query: %v %v", page.Rows, err)
	}

	if _, err := users.Insert(ctx, []byte(`{"_id":"u9","email":"u1@x"}`)); err == nil || !strings.Contains(err.Error(), "unique_violation") {
		t.Errorf("unique must hold after a restore: %v", err)
	}

	stream, err := db.Watch("users", "mailer", "")
	if err != nil {
		t.Fatal(err)
	}

	defer stream.Close()

	next, err := stream.Next(ctx)
	if err != nil || next.ID != "u4" {
		t.Errorf("consumer must resume after its ack (tx 3): got %+v %v", next, err)
	}

	checkBlobs(t, db, withBlobs)

	if def, ok := db.Definition("ext"); !ok || def.Write != catalog.Direct {
		t.Errorf("direct collection definition: %+v %v", def, ok)
	}
}

func checkBlobs(t *testing.T, db *engine.DB, withBlobs bool) {
	t.Helper()

	calls, err := db.Get("calls")
	if err != nil {
		t.Fatal(err)
	}

	for id, size := range map[string]int{"c1": smallBlob, "c2": bigBlob} {
		d, ok := calls.Get(id)
		if !ok {
			t.Fatalf("%s is missing", id)
		}

		refs := blob.Refs(d.Doc)
		if len(refs) != 1 {
			t.Fatalf("%s: refs = %v", id, refs)
		}

		r, err := db.Blobs().Get(refs[0])

		if !withBlobs {
			if err == nil {
				r.Close()
				t.Errorf("%s: -no-blobs must leave the content out", id)
			}

			continue
		}

		if err != nil {
			t.Fatalf("%s: blob: %v", id, err)
		}

		got, _ := io.ReadAll(r)

		r.Close()

		if len(got) != size || !bytes.Equal(got, bytes.Repeat([]byte("a"), size)) {
			t.Errorf("%s: blob has %d bytes, want %d", id, len(got), size)
		}
	}
}

func runBackupCmd(t *testing.T, args ...string) (stdout []byte, stderr string) {
	t.Helper()

	var out, errOut bytes.Buffer

	if err := backupTo(append([]string{"-config", "none.yml"}, args...), &out, &errOut); err != nil {
		t.Fatalf("backup %v: %v", args, err)
	}

	return out.Bytes(), errOut.String()
}

func restoreFrom(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()

	var out strings.Builder

	err := restore(append([]string{"-config", "none.yml"}, args...), stdin, &out)

	return out.String(), err
}

// Every way to take a backup, restored into an empty dir, gives the same data.
func TestBackupRestoreRoundTrip(t *testing.T) {
	for _, online := range []bool{false, true} {
		for _, gz := range []bool{false, true} {
			for _, toStdout := range []bool{false, true} {
				name := fmt.Sprintf("online=%v/gzip=%v/stdout=%v", online, gz, toStdout)

				t.Run(name, func(t *testing.T) {
					roundTrip(t, online, gz, toStdout)
				})
			}
		}
	}
}

func roundTrip(t *testing.T, online, gz, toStdout bool) {
	src := t.TempDir()

	seedRich(t, src)

	args := []string{"-data-dir", src}

	if online {
		sock, _ := startDaemon(t, src)

		args = []string{"-url", "unix://" + sock}
	}

	if gz {
		args = append(args, "-gzip")
	}

	var restoreArgs []string

	target := filepath.Join(t.TempDir(), "data")

	if toStdout {
		out, _ := runBackupCmd(t, append(args, "-")...)

		if gz != (len(out) > 1 && out[0] == 0x1f && out[1] == 0x8b) {
			t.Fatalf("gzip=%v but the stream starts with % x", gz, out[:2])
		}

		if _, err := restoreFrom(t, bytes.NewReader(out), "-", "-data-dir", target); err != nil {
			t.Fatal(err)
		}
	} else {
		dest := filepath.Join(t.TempDir(), "snap")

		_, msg := runBackupCmd(t, append(args, dest)...)

		file := dest + ".ndb"

		if gz {
			file += ".gz"
		}

		if !strings.Contains(msg, file) {
			t.Errorf("the final path %s is not printed: %q", file, msg)
		}

		restoreArgs = []string{file, "-data-dir", target}

		if _, err := restoreFrom(t, nil, restoreArgs...); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := os.Stat(filepath.Join(target, stageDir)); !os.IsNotExist(err) {
		t.Error("stage dir was left behind")
	}

	checkRich(t, target, true)
}

func TestBackupNoBlobs(t *testing.T) {
	src := t.TempDir()

	seedRich(t, src)

	out, _ := runBackupCmd(t, "-data-dir", src, "-no-blobs", "-")

	if bytes.Contains(out, []byte(`"_section":"blob"`)) {
		t.Fatal("-no-blobs wrote a blob line")
	}

	target := filepath.Join(t.TempDir(), "data")

	if _, err := restoreFrom(t, bytes.NewReader(out), "-", "-data-dir", target); err != nil {
		t.Fatal(err)
	}

	checkRich(t, target, false)
}

func TestBackupFileNaming(t *testing.T) {
	src := t.TempDir()

	seedRich(t, src)

	dir := t.TempDir()

	runBackupCmd(t, "-data-dir", src, filepath.Join(dir, "a"))
	runBackupCmd(t, "-data-dir", src, "-gzip", filepath.Join(dir, "b"))
	runBackupCmd(t, "-data-dir", src, filepath.Join(dir, "c.ndb"))

	for _, name := range []string{"a.ndb", "b.ndb.gz", "c.ndb"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	var out, errOut bytes.Buffer

	args := []string{"-config", "none.yml", "-data-dir", src, filepath.Join(dir, "a")}

	if err := backupTo(args, &out, &errOut); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("an existing file needs -force: %v", err)
	}

	if err := backupTo(append(args, "-force"), &out, &errOut); err != nil {
		t.Fatal(err)
	}

	if leftovers, _ := filepath.Glob(filepath.Join(dir, "*.part")); len(leftovers) != 0 {
		t.Errorf("part files left: %v", leftovers)
	}
}

// Restore reads gzip by the magic bytes, whatever the file is named.
func TestRestoreDetectsGzipByMagicBytes(t *testing.T) {
	src := t.TempDir()

	seedRich(t, src)

	out, _ := runBackupCmd(t, "-data-dir", src, "-gzip", "-")

	misnamed := filepath.Join(t.TempDir(), "backup.ndb")

	if err := os.WriteFile(misnamed, out, 0o644); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "data")

	if _, err := restoreFrom(t, nil, misnamed, "-data-dir", target); err != nil {
		t.Fatal(err)
	}

	checkRich(t, target, true)
}

func plainBackup(t *testing.T) []byte {
	t.Helper()

	src := t.TempDir()

	seedRich(t, src)

	out, _ := runBackupCmd(t, "-data-dir", src, "-")

	return out
}

// A cut or damaged file fails the restore, and the data dir is not touched.
func TestRestoreRefusesDamagedFilesAndKeepsTheDataDir(t *testing.T) {
	good := string(plainBackup(t))
	lines := strings.Split(strings.TrimSuffix(good, "\n"), "\n")

	cases := map[string]string{
		"no end line":  strings.Join(lines[:len(lines)-1], "\n") + "\n",
		"cut mid line": good[:len(good)/2],
		"bad wal crc":  strings.Replace(good, `"u1@x"`, `"u7@x"`, 1),
		"not ndb":      "just some text\nmore text\n",
		"empty":        "",
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			target := t.TempDir()
			keep := filepath.Join(target, "keep.txt")

			if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := restoreFrom(t, strings.NewReader(content), "-", "-data-dir", target, "-force")
			if err == nil {
				t.Fatal("want an error")
			}

			if _, err := os.Stat(keep); err != nil {
				t.Error("the failed restore changed the data dir")
			}

			if _, err := os.Stat(filepath.Join(target, stageDir)); !os.IsNotExist(err) {
				t.Error("stage dir was left behind")
			}

			if _, err := os.Stat(filepath.Join(target, "catalog.bolt")); err == nil {
				t.Error("catalog.bolt was swapped in")
			}
		})
	}
}

func TestRestoreNotNdbFileSaysSo(t *testing.T) {
	_, err := restoreFrom(t, strings.NewReader("hello\n"), "-", "-data-dir", filepath.Join(t.TempDir(), "data"))

	if err == nil || !strings.Contains(err.Error(), "not an ndb backup") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreFailureDoesNotCreateTheDataDir(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")

	if _, err := restoreFrom(t, strings.NewReader("hello\n"), "-", "-data-dir", target); err == nil {
		t.Fatal("want an error")
	}

	if _, err := os.Stat(target); err == nil {
		t.Error("a failed restore must not create the data dir")
	}
}

func TestRestoreRefusesANonEmptyDataDirWithoutForce(t *testing.T) {
	out := plainBackup(t)
	target := t.TempDir()
	keep := filepath.Join(target, "keep.txt")

	if err := os.WriteFile(keep, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := restoreFrom(t, bytes.NewReader(out), "-", "-data-dir", target)
	if err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("err = %v", err)
	}

	if _, err := os.Stat(keep); err != nil {
		t.Error("the refused restore changed the data dir")
	}

	if _, err := restoreFrom(t, bytes.NewReader(out), "-", "-data-dir", target, "-force"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(keep); !os.IsNotExist(err) {
		t.Error("-force must replace the old contents")
	}

	checkRich(t, target, true)
}

func TestRestoreRefusesARunningDaemon(t *testing.T) {
	out := plainBackup(t)
	target := t.TempDir()

	seedDataDir(t, target)
	startDaemon(t, target)

	_, err := restoreFrom(t, bytes.NewReader(out), "-", "-data-dir", target, "-force")
	if err == nil || !strings.Contains(err.Error(), "running daemon") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestoreRejectsMissingSource(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data")

	if _, err := restoreFrom(t, nil, filepath.Join(t.TempDir(), "nope.ndb"), "-data-dir", target); err == nil {
		t.Error("a missing file must fail")
	}

	if _, err := restoreFrom(t, nil, "-data-dir", target); err == nil {
		t.Error("no source must fail")
	}

	if _, err := os.Stat(target); err == nil {
		t.Error("a refused restore must not create the data dir")
	}
}

func TestSocketPathTooLongNamesTheLimit(t *testing.T) {
	long := "/tmp/" + strings.Repeat("x", 200) + "/ndb.sock"

	err := backupTo([]string{"-config", "none.yml", "-url", "unix://" + long, "-"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "limit") || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("cli err = %v", err)
	}

	_, err = listen(config.Listen{Unix: long})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("daemon err = %v", err)
	}
}

func TestGzipRoundTripKeepsPlainContent(t *testing.T) {
	src := t.TempDir()

	seedRich(t, src)

	plain, _ := runBackupCmd(t, "-data-dir", src, "-")
	packed, _ := runBackupCmd(t, "-data-dir", src, "-gzip", "-")

	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}

	unpacked, _ := io.ReadAll(zr)

	// The header holds created_at and the end line its crc, so compare the
	// lines between them.
	body := func(b []byte) string {
		lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")

		return strings.Join(lines[1:len(lines)-1], "\n")
	}

	if len(plain) == 0 || body(plain) != body(unpacked) {
		t.Error("-gzip must carry the same lines as the plain file")
	}
}
