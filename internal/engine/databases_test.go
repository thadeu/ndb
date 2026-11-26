package engine_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
)

func database(t *testing.T, db *engine.DB, name string) *engine.DB {
	t.Helper()

	child, err := db.Database(name, true)
	if err != nil {
		t.Fatalf("database %s: %v", name, err)
	}

	return child
}

func insert(t *testing.T, col *engine.Collection, docs ...string) {
	t.Helper()

	raw := make([][]byte, len(docs))

	for i, d := range docs {
		raw[i] = []byte(d)
	}

	if _, err := col.Insert(ctx, raw...); err != nil {
		t.Fatal(err)
	}
}

func docOf(t *testing.T, db *engine.DB, coll, id string) string {
	t.Helper()

	col, err := db.Get(coll)
	if err != nil {
		return ""
	}

	d, ok := col.Get(id)
	if !ok {
		return ""
	}

	return string(d.Doc)
}

func TestDefaultDatabaseIsTheDataDir(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	for _, name := range []string{"", engine.DefaultDatabase} {
		got, err := db.Database(name, false)
		if err != nil || got != db {
			t.Fatalf("Database(%q) = %p, %v; want the root %p", name, got, err, db)
		}
	}

	if db.DatabaseName() != engine.DefaultDatabase {
		t.Fatalf("name %q", db.DatabaseName())
	}

	ensure(t, db, catalog.Def{Name: "users"})

	if _, err := os.Stat(filepath.Join(dir, "_databases")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a data dir with no database has a _databases dir: %v", err)
	}

	if got := db.Databases(); len(got) != 0 {
		t.Fatalf("databases %v", got)
	}
}

func TestDatabasesHoldTheSameNamesApart(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	insert(t, ensure(t, db, catalog.Def{Name: "users"}), `{"_id":"u1","app":"default"}`)
	insert(t, ensure(t, database(t, db, "billion"), catalog.Def{Name: "users"}), `{"_id":"u1","app":"billion"}`)
	insert(t, ensure(t, database(t, db, "cronota"), catalog.Def{Name: "users"}), `{"_id":"u1","app":"cronota"}`)

	for name, want := range map[string]string{"": "default", "billion": "billion", "cronota": "cronota"} {
		child, err := db.Database(name, false)
		if err != nil {
			t.Fatal(err)
		}

		if got := docOf(t, child, "users", "u1"); !strings.Contains(got, `"app":"`+want+`"`) {
			t.Fatalf("database %q: u1 = %s", name, got)
		}
	}

	if got := db.Databases(); strings.Join(got, ",") != "billion,cronota" {
		t.Fatalf("databases %v", got)
	}

	if _, err := os.Stat(filepath.Join(dir, "_databases", "billion", "collections", "users")); err != nil {
		t.Fatalf("the collection of billion is not in its dir: %v", err)
	}

	if child := database(t, db, "billion"); child.DatabaseName() != "billion" || child.Dir() != filepath.Join(dir, "_databases", "billion") {
		t.Fatalf("name %q, dir %s", child.DatabaseName(), child.Dir())
	}
}

func TestDatabaseNamesAndUnknownDatabases(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	for _, bad := range []string{"Billion", "_x", ".x", "-x", "a/b", "a b", "../x", strings.Repeat("a", 64)} {
		if _, err := db.Database(bad, true); !errors.Is(err, engine.ErrInvalidDatabase) {
			t.Fatalf("%q: %v, want ErrInvalidDatabase", bad, err)
		}
	}

	for _, good := range []string{"a", "1billion", "app_2", "app-3", strings.Repeat("a", 63)} {
		if !engine.ValidDatabase(good) {
			t.Fatalf("%q is refused", good)
		}
	}

	if _, err := db.Database("nope", false); !errors.Is(err, engine.ErrUnknownDatabase) {
		t.Fatalf("unknown: %v", err)
	}

	child := database(t, db, "app")

	if _, err := child.Database("inner", true); !errors.Is(err, engine.ErrNestedDatabase) {
		t.Fatalf("nested: %v", err)
	}

	if again := database(t, db, "app"); again != child {
		t.Fatal("a second call opened the database again")
	}
}

func TestDatabasesOpenAgainAfterARestart(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	insert(t, ensure(t, database(t, db, "billion"), catalog.Def{Name: "events", Indexes: []string{"kind"}}), `{"_id":"e1","kind":"sent"}`)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Database("billion", false); !errors.Is(err, engine.ErrClosed) {
		t.Fatalf("after Close: %v", err)
	}

	db = open(t, cfg(dir, nil))

	child, err := db.Database("billion", false)
	if err != nil {
		t.Fatalf("the database is not open after a restart: %v", err)
	}

	if got := docOf(t, child, "events", "e1"); !strings.Contains(got, `"kind":"sent"`) {
		t.Fatalf("e1 = %s", got)
	}

	if def, ok := child.Definition("events"); !ok || len(def.Indexes) != 1 {
		t.Fatalf("definition %+v", def)
	}
}

func TestDropDatabase(t *testing.T) {
	dir := t.TempDir()
	db := open(t, cfg(dir, nil))

	insert(t, ensure(t, database(t, db, "old"), catalog.Def{Name: "users"}), `{"_id":"u1"}`)

	if err := db.DropDatabase(engine.DefaultDatabase); !errors.Is(err, engine.ErrDefaultDatabase) {
		t.Fatalf("drop default: %v", err)
	}

	if err := db.DropDatabase("nope"); !errors.Is(err, engine.ErrUnknownDatabase) {
		t.Fatalf("drop unknown: %v", err)
	}

	if err := db.DropDatabase("old"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Database("old", false); !errors.Is(err, engine.ErrUnknownDatabase) {
		t.Fatalf("after drop: %v", err)
	}

	if got := db.Databases(); len(got) != 0 {
		t.Fatalf("databases %v", got)
	}

	if got := docOf(t, database(t, db, "old"), "users", "u1"); got != "" {
		t.Fatalf("a new database of the same name has the old data: %s", got)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(filepath.Join(dir, "_databases"))
	if err != nil {
		t.Fatal(err)
	}

	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("the dropped database is still on disk: %s", e.Name())
		}
	}
}

func TestDroppedDatabaseLeftOverIsSweptAtStart(t *testing.T) {
	dir := t.TempDir()
	left := filepath.Join(dir, "_databases", ".drop-old-1")

	if err := os.MkdirAll(left, 0o755); err != nil {
		t.Fatal(err)
	}

	db := open(t, cfg(dir, nil))

	if got := db.Databases(); len(got) != 0 {
		t.Fatalf("databases %v", got)
	}

	db.Close()

	if _, err := os.Stat(left); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the left over of a drop is still there: %v", err)
	}
}

func TestStartSkipsWhatIsNotADatabase(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "_databases")

	if err := os.MkdirAll(filepath.Join(root, "Not-Valid"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "afile"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	db := open(t, cfg(dir, nil))

	if got := db.Databases(); len(got) != 0 {
		t.Fatalf("databases %v", got)
	}

	if _, err := os.Stat(filepath.Join(root, "Not-Valid")); err != nil {
		t.Fatalf("a dir that is not a database was touched: %v", err)
	}
}

func TestStartFailsOnABrokenDatabase(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "_databases", "app", "catalog.bolt"), 0o755); err != nil {
		t.Fatal(err)
	}

	db, err := engine.Open(cfg(dir, nil), nil)
	if err == nil {
		db.Close()

		t.Fatal("a broken database did not stop the start")
	}

	if !strings.Contains(err.Error(), "database app") {
		t.Fatalf("err %v", err)
	}

	if db, err := engine.Open(cfg(dir, nil), nil); err == nil {
		db.Close()

		t.Fatal("the lock of the failed start was kept")
	} else if !strings.Contains(err.Error(), "database app") {
		t.Fatalf("second start: %v", err)
	}
}

func TestADatabaseHasNoDrop(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	child := database(t, db, "app")

	if err := child.DropDatabase("x"); !errors.Is(err, engine.ErrNestedDatabase) {
		t.Fatalf("drop on a database: %v", err)
	}

	if err := db.DropDatabase(""); !errors.Is(err, engine.ErrDefaultDatabase) {
		t.Fatalf("drop of the empty name: %v", err)
	}
}

func TestDatabasesShareTheKeyOfTheDaemon(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	child := database(t, db, "billion")

	if child.Box() != db.Box() || child.Settings() != db.Settings() || child.Key().ID != db.Key().ID {
		t.Fatal("a database has a key of its own")
	}

	if _, err := child.RotateKey(false); !errors.Is(err, engine.ErrNotDefaultDatabase) {
		t.Fatalf("rotate on a database: %v", err)
	}
}

func TestKeyRotationReachesEveryDatabase(t *testing.T) {
	old := engine.RotationTick
	engine.RotationTick = 20 * time.Millisecond

	t.Cleanup(func() { engine.RotationTick = old })

	db := open(t, cfg(t.TempDir(), nil))
	secrets := ensure(t, database(t, db, "billion"), catalog.Def{Name: "secrets", Encrypted: []string{"pem"}})

	insert(t, secrets, `{"_id":"k1","pem":"-----BEGIN-----"}`)

	if _, err := db.RotateKey(false); err != nil {
		t.Fatal(err)
	}

	next := db.Key().ID
	deadline := time.Now().Add(10 * time.Second)

	for db.RotationState().From != "" {
		if time.Now().After(deadline) {
			t.Fatalf("the rotation never ends: %+v", db.RotationState())
		}

		time.Sleep(20 * time.Millisecond)
	}

	d, _ := secrets.Get("k1")

	if !strings.Contains(string(d.Doc), "ndb:e1:"+next+":") {
		t.Fatalf("the value of the database was not sealed again: %s", d.Doc)
	}
}

func TestPendingResealNamesTheDatabase(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	child := database(t, db, "billion")

	insert(t, ensure(t, child, catalog.Def{Name: "secrets", Encrypted: []string{"pem"}}), `{"_id":"k1","pem":"x"}`)

	if _, err := db.RotateKey(false); err != nil {
		t.Fatal(err)
	}

	state := db.RotationState()

	for _, p := range state.Pending {
		if p != "billion/secrets" {
			t.Fatalf("pending %v", state.Pending)
		}
	}
}

func TestConcurrentOpenOfOneDatabase(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		got = map[*engine.DB]bool{}
	)

	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			child, err := db.Database("app", true)
			if err != nil {
				t.Error(err)

				return
			}

			mu.Lock()
			got[child] = true
			mu.Unlock()
		}()
	}

	wg.Wait()

	if len(got) != 1 {
		t.Fatalf("%d instances of one database", len(got))
	}
}

func TestBackupHoldsEveryDatabase(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insert(t, ensure(t, db, catalog.Def{Name: "users"}), `{"_id":"u1","app":"default"}`)
	insert(t, ensure(t, database(t, db, "billion"), catalog.Def{Name: "users"}), `{"_id":"u1","app":"billion"}`)
	ensure(t, database(t, db, "empty"), catalog.Def{Name: "nothing"})

	var buf bytes.Buffer

	if err := db.Backup(ctx, &buf, backup.Options{Version: "test"}); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(buf.String(), `{"ndb_backup":2,`) {
		t.Fatalf("header %.80s", buf.String())
	}

	stage := filepath.Join(t.TempDir(), "restored")

	if err := backup.Extract(&buf, stage); err != nil {
		t.Fatal(err)
	}

	restored := open(t, cfg(stage, nil))

	if got := docOf(t, restored, "users", "u1"); !strings.Contains(got, `"app":"default"`) {
		t.Fatalf("default u1 = %s", got)
	}

	child, err := restored.Database("billion", false)
	if err != nil {
		t.Fatal(err)
	}

	if got := docOf(t, child, "users", "u1"); !strings.Contains(got, `"app":"billion"`) {
		t.Fatalf("billion u1 = %s", got)
	}

	if got := restored.Databases(); strings.Join(got, ",") != "billion,empty" {
		t.Fatalf("databases %v", got)
	}
}

func TestBackupWithNoDatabaseKeepsFormat1(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insert(t, ensure(t, db, catalog.Def{Name: "users"}), `{"_id":"u1"}`)

	var buf bytes.Buffer

	if err := db.Backup(ctx, &buf, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(buf.String(), `{"ndb_backup":1,`) {
		t.Fatalf("header %.80s", buf.String())
	}
}

func TestBackupOfOneDatabase(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	insert(t, ensure(t, db, catalog.Def{Name: "users"}), `{"_id":"u1","app":"default"}`)

	child := database(t, db, "billion")

	insert(t, ensure(t, child, catalog.Def{Name: "users"}), `{"_id":"u1","app":"billion"}`)

	var buf bytes.Buffer

	if err := child.Backup(ctx, &buf, backup.Options{}); err != nil {
		t.Fatal(err)
	}

	stage := filepath.Join(t.TempDir(), "restored")

	if err := backup.Extract(&buf, stage); err != nil {
		t.Fatal(err)
	}

	restored := open(t, cfg(stage, nil))

	if got := docOf(t, restored, "users", "u1"); !strings.Contains(got, `"app":"billion"`) {
		t.Fatalf("the backup of one database restores as the default: u1 = %s", got)
	}

	if restored.Key().ID != db.Key().ID {
		t.Fatal("the backup of a database has another key")
	}
}
