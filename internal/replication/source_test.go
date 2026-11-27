package replication_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/replication"
)

func TestParseSources(t *testing.T) {
	got, err := replication.ParseSources("tcp://:tok@ndb-0.app.voodu:7070/1billion?as=1billion, tcp://:t2@ndb-0.cronota.voodu:7070?as=cronota,https://:t3@h:443", engine.ValidDatabase)
	if err != nil {
		t.Fatal(err)
	}

	want := []replication.Source{
		{Base: "http://ndb-0.app.voodu:7070", Token: "tok", Remote: "1billion", Local: "1billion"},
		{Base: "http://ndb-0.cronota.voodu:7070", Token: "t2", Remote: "default", Local: "cronota"},
		{Base: "https://h:443", Token: "t3", Remote: "default", Local: "default"},
	}

	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("source %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if r := got[1].Redacted(); r != "tcp://:***@ndb-0.cronota.voodu:7070/default?as=cronota" {
		t.Fatalf("redacted = %s", r)
	}

	if r := got[2].Redacted(); r != "https://:***@h:443/default" {
		t.Fatalf("redacted = %s", r)
	}

	if user, _ := replication.ParseSources("tcp://tok@h:1", engine.ValidDatabase); user[0].Token != "tok" {
		t.Fatalf("a token as the user: %+v", user)
	}

	if none, err := replication.ParseSources(" ", engine.ValidDatabase); err != nil || len(none) != 0 {
		t.Fatalf("empty: %v %v", none, err)
	}
}

func TestParseSourcesErrors(t *testing.T) {
	cases := map[string]string{
		"tcp://:a@h:1/x,tcp://:b@g:1?as=x": "two sources",
		"redis://h:1":                      "use tcp://",
		"tcp://:secret@h":                  "host and a port",
		"tcp://:a@h:1/Bad_Name!":           "not a valid database",
		"tcp://:a@h:1/x?as=no%20pe":        "not a valid database",
		"tcp://%zz":                        "NDB_REPLICA_OF",
	}

	for in, want := range cases {
		_, err := replication.ParseSources(in, engine.ValidDatabase)

		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", in, err, want)
		}

		if err != nil && strings.Contains(err.Error(), "secret") {
			t.Errorf("%q: the error shows the token: %v", in, err)
		}
	}
}

func TestPositions(t *testing.T) {
	s := replication.FormatPositions(map[string]uint64{"b": 2, "a": 10, replication.CatalogKey: 3})

	if s != "_catalog=3,a=10,b=2" {
		t.Fatalf("format = %s", s)
	}

	got, err := replication.ParsePositions(s)
	if err != nil || got["a"] != 10 || got["b"] != 2 || got[replication.CatalogKey] != 3 {
		t.Fatalf("parse = %v %v", got, err)
	}

	for _, bad := range []string{"a", "=1", "a=x", "a=-1"} {
		if _, err := replication.ParsePositions(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}

	if got, err := replication.ParsePositions(""); err != nil || len(got) != 0 {
		t.Fatalf("empty = %v %v", got, err)
	}
}

func TestState(t *testing.T) {
	dir := t.TempDir()

	if _, ok, err := replication.LoadState(dir); ok || err != nil {
		t.Fatalf("no state: %v %v", ok, err)
	}

	if err := replication.RemoveState(dir); err != nil {
		t.Fatal(err)
	}

	st := replication.State{Role: replication.RoleCopy, Term: 2, Cursors: map[string]uint64{"a": 5}}

	if err := replication.SaveState(dir, st); err != nil {
		t.Fatal(err)
	}

	got, ok, err := replication.LoadState(dir)
	if err != nil || !ok || got.Term != 2 || got.Cursors["a"] != 5 {
		t.Fatalf("state = %+v %v %v", got, ok, err)
	}

	if err := os.WriteFile(filepath.Join(dir, replication.StateFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := replication.LoadState(dir); err == nil {
		t.Fatal("a bad state file loaded")
	}

	if err := replication.SaveState(filepath.Join(dir, "nope", "deeper"), st); err == nil {
		t.Fatal("saved into a dir that is not there")
	}

	if err := replication.RemoveState(dir); err != nil {
		t.Fatal(err)
	}
}
