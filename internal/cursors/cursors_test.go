package cursors_test

import (
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/cursors"
)

func TestUpdateStoresAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursors.bolt")

	s, err := cursors.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, _ := s.Get("c", "relay"); ok {
		t.Fatal("unknown consumer must not exist")
	}

	set := func(old string, _ bool) (string, error) { return "p1", nil }

	if err := s.Update("c", "relay", set); err != nil {
		t.Fatal(err)
	}

	s.Close()

	s, err = cursors.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	got, ok, err := s.Get("c", "relay")
	if err != nil || !ok || got != "p1" {
		t.Fatalf("got %q ok=%v err=%v", got, ok, err)
	}
}

func TestConsumersAndCollectionsAreIndependent(t *testing.T) {
	s, err := cursors.Open(filepath.Join(t.TempDir(), "cursors.bolt"))
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	put := func(coll, consumer, v string) {
		t.Helper()

		err := s.Update(coll, consumer, func(string, bool) (string, error) { return v, nil })
		if err != nil {
			t.Fatal(err)
		}
	}

	put("a", "x", "1")
	put("a", "y", "2")
	put("b", "x", "3")

	for _, c := range []struct{ coll, consumer, want string }{{"a", "x", "1"}, {"a", "y", "2"}, {"b", "x", "3"}} {
		if got, _, _ := s.Get(c.coll, c.consumer); got != c.want {
			t.Fatalf("%s/%s = %q, want %q", c.coll, c.consumer, got, c.want)
		}
	}
}

func TestListReturnsOnlyTheCollectionsPositions(t *testing.T) {
	s, err := cursors.Open(filepath.Join(t.TempDir(), "cursors.bolt"))
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	set := func(pos string) func(string, bool) (string, error) {
		return func(string, bool) (string, error) { return pos, nil }
	}

	s.Update("posts", "a", set("e1.1"))
	s.Update("posts", "b", set("e5.2"))
	s.Update("posts2", "a", set("e9.9"))
	s.Update("other", "a", set("e7.7"))

	got, err := s.List("posts")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0] != "e1.1" || got[1] != "e5.2" {
		t.Fatalf("list = %v", got)
	}
}
