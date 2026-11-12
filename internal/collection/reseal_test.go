package collection_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/crypt"
	"github.com/thadeu/ndb/internal/fs"
)

func testBox(t *testing.T) *crypt.Box {
	t.Helper()

	box, err := crypt.New("k1", bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}

	return box
}

func openBox(t *testing.T, dir string, def catalog.Def) *collection.Collection {
	t.Helper()

	def.Name = "users"

	c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: def, Box: testBox(t)})
	if err != nil {
		t.Fatal(err)
	}

	return c
}

var blindUsers = catalog.Def{Encrypted: []string{"cpf", "email"}, Blind: catalog.Blind{"cpf": "numeric", "email": "lower"}}

func findIDs(t *testing.T, c *collection.Collection, where string) string {
	t.Helper()

	q, err := c.ParseQuery([]byte(`{"query":` + where + `,"sort":{"_id":1}}`))
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}

	page, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}

	return strings.Join(ids(t, page), ",")
}

func waitResealed(t *testing.T, c *collection.Collection) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if st := c.Resealing(); len(st) > 0 && st[0].State == "ready" {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatalf("the reseal job never became ready: %v", c.Resealing())
}

func sealed(t *testing.T, c *collection.Collection, id string) bool {
	t.Helper()

	d, ok := c.Find(id)
	if !ok {
		t.Fatalf("%s not found", id)
	}

	return strings.Contains(string(d.Doc), `"cpf":"ndb:e1:`) && !strings.Contains(string(d.Doc), "@")
}

// While the job runs, a filter on a blind field finds an old document by
// its plain value, normalized as the hash is.
func TestAFilterFindsThePlainValueUntilTheResealIsReady(t *testing.T) {
	c := openBox(t, t.TempDir(), catalog.Def{})

	defer c.Close()

	seed(t, c,
		`{"_id":"u1","cpf":"390.533.447-05","email":" Ana@Dest.test"}`,
		`{"_id":"u2","cpf":"11122233344","email":"bia@dest.test"}`,
		`{"_id":"u3"}`)

	release := make(chan struct{})

	collection.SetResealGate(c, func() { <-release })
	c.SetEncrypted(blindUsers)
	seed(t, c, `{"_id":"u4","cpf":"39053344705","email":"ana@dest.test"}`)

	cases := map[string]string{
		`{"cpf":"39053344705"}`:                          "u1,u4",
		`{"email":"ANA@dest.test"}`:                      "u1,u4",
		`{"cpf":{"$in":["111.222.333-44","x"]}}`:         "u2",
		`{"cpf":{"$ne":"39053344705"}}`:                  "u2,u3",
		`{"cpf":{"$nin":["39053344705","11122233344"]}}`: "u3",
		`{"cpf":{"$exists":true}}`:                       "u1,u2,u4",
	}

	check := func(when string) {
		for where, want := range cases {
			if got := findIDs(t, c, where); got != want {
				t.Errorf("%s %s: %s, want %s", when, where, got, want)
			}
		}
	}

	check("building")

	if st := c.Resealing(); len(st) != 2 || st[0].State != "building" {
		t.Fatalf("state %v", st)
	}

	close(release)
	waitResealed(t, c)
	check("ready")

	for _, id := range []string{"u1", "u2", "u4"} {
		if !sealed(t, c, id) {
			t.Errorf("%s is not sealed", id)
		}
	}
}

// A field added while the collection was closed (ndb.yml, a cut job, a data
// dir of 1.22) starts the job at the open. The next open has nothing to do.
func TestTheResealRunsAtTheOpenForAFieldAddedWhileClosed(t *testing.T) {
	dir := t.TempDir()
	c := openBox(t, dir, catalog.Def{})

	seed(t, c, `{"_id":"u1","cpf":"390.533.447-05","email":"ana@dest.test"}`)

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c = openBox(t, dir, blindUsers)

	waitResealed(t, c)

	if !sealed(t, c, "u1") {
		t.Error("u1 is not sealed")
	}

	if got := findIDs(t, c, `{"cpf":"39053344705"}`); got != "u1" {
		t.Errorf("got %s", got)
	}

	data, err := os.ReadFile(filepath.Join(dir, collection.ResealFile))

	if err != nil || string(data) != `["cpf","email"]` {
		t.Errorf("file %s %v", data, err)
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c = openBox(t, dir, blindUsers)

	defer c.Close()

	if st := c.Resealing(); st != nil {
		t.Errorf("a second job: %v", st)
	}
}

// A field added while a job runs waits for it, and the plain value of the
// new field is found in the meantime.
func TestAFieldAddedDuringAResealRunsNext(t *testing.T) {
	c := openBox(t, t.TempDir(), catalog.Def{})

	defer c.Close()

	seed(t, c, `{"_id":"u1","cpf":"390.533.447-05","email":"Ana@dest.test"}`)

	release := make(chan struct{})

	collection.SetResealGate(c, func() { <-release })
	c.SetEncrypted(catalog.Def{Encrypted: []string{"cpf"}, Blind: catalog.Blind{"cpf": "numeric"}})
	c.SetEncrypted(blindUsers)

	if got := findIDs(t, c, `{"email":"ana@dest.test"}`); got != "u1" {
		t.Errorf("email while the cpf job runs: %s", got)
	}

	close(release)

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		if st := c.Resealing(); len(st) == 1 && st[0].Field == "email" && st[0].State == "ready" {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	if !sealed(t, c, "u1") {
		t.Errorf("u1 is not sealed: %v", c.Resealing())
	}

	if got := findIDs(t, c, `{"email":"ana@dest.test"}`); got != "u1" {
		t.Errorf("email after: %s", got)
	}
}
