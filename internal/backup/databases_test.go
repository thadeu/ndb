package backup_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/catalog"
)

func noWAL(string, func([]byte) error) error { return nil }

func TestWriteWithDatabasesThenExtract(t *testing.T) {
	src := backup.Source{
		Defs: []catalog.Def{{Name: "users", Write: catalog.Engine}},
		WAL:  noWAL,
		Databases: []backup.Database{
			{Name: "billion", Source: backup.Source{Defs: []catalog.Def{{Name: "users", Write: catalog.Engine}, {Name: "events", Write: catalog.Engine}}, WAL: noWAL}},
			{Name: "cronota", Source: backup.Source{Defs: []catalog.Def{{Name: "jobs", Write: catalog.Engine}}, WAL: noWAL}},
		},
	}

	var buf bytes.Buffer

	if err := backup.Write(&buf, src, backup.Options{Version: "test"}); err != nil {
		t.Fatal(err)
	}

	text := buf.String()

	if !strings.HasPrefix(text, `{"ndb_backup":2,`) || !strings.Contains(text, `{"_section":"database","name":"billion","collections":["users","events"]}`) {
		t.Fatalf("backup:\n%s", text)
	}

	stage := filepath.Join(t.TempDir(), "stage")

	if err := backup.Extract(&buf, stage); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"catalog.bolt",
		"collections/users/000000.ndjson",
		"_databases/billion/catalog.bolt",
		"_databases/billion/collections/events/000000.ndjson",
		"_databases/cronota/collections/jobs/000000.ndjson",
	} {
		if _, err := os.Stat(filepath.Join(stage, path)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestWriteStopsOnAnErrorOfADatabase(t *testing.T) {
	boom := errors.New("boom")

	src := backup.Source{
		WAL: noWAL,
		Databases: []backup.Database{{Name: "billion", Source: backup.Source{
			Defs: []catalog.Def{{Name: "users", Write: catalog.Engine}},
			WAL:  func(string, func([]byte) error) error { return boom },
		}}},
	}

	err := backup.Write(&bytes.Buffer{}, src, backup.Options{})

	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "database billion") {
		t.Fatalf("err %v", err)
	}
}

func TestExtractRefusesBadDatabaseLines(t *testing.T) {
	const (
		v1 = `{"ndb_backup":1,"version":"x","created_at":"x","collections":[]}` + "\n"
		v2 = `{"ndb_backup":2,"version":"x","created_at":"x","collections":[]}` + "\n"
	)

	db := func(name string) string {
		return `{"_section":"database","name":"` + name + `","collections":[]}` + "\n"
	}

	cases := map[string]struct{ in, want string }{
		"format 3":          {`{"ndb_backup":3,"version":"x","created_at":"x","collections":[]}` + "\n", "format 3 is not supported"},
		"database in v1":    {v1 + db("billion"), "a database line in a backup of format 1"},
		"bad name":          {v2 + db("Bad"), "invalid or repeated"},
		"default":           {v2 + db("default"), "invalid or repeated"},
		"repeated":          {v2 + db("billion") + db("billion"), "invalid or repeated"},
		"bad json":          {v2 + `{"_section":"database","name":1}` + "\n", "line 2"},
		"bad collection":    {v2 + `{"_section":"database","name":"app","collections":["../x"]}` + "\n", "bad collection name"},
		"in a split blob":   {v2 + `{"_section":"blob","ref":"sha256:x","size":2,"part":1,"parts":2,"data":"YQ=="}` + "\n" + db("app"), "inside a split blob"},
		"catalog not named": {v2 + db("app") + `{"_section":"catalog","name":"users","definition":{"name":"users"}}` + "\n", "not in the header"},
	}

	for name, c := range cases {
		err := backup.Extract(strings.NewReader(c.in), filepath.Join(t.TempDir(), "stage"))

		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}
