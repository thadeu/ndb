package ndb

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type Note struct {
	ID    string `ndb:"_id,id"`
	Title string `json:"title"`
	Body  string
	Skip  string `json:"-"`
	Draft bool   `ndb:"-"`
	note  string
}

func TestInsertGeneratesIDAndReturnsResults(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	notes, err := Collection[Note](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	res, err := notes.Insert(ctx, Note{Title: "a"}, Note{ID: "given", Title: "b"})
	if err != nil {
		t.Fatalf("insert with an empty generated id: %v", err)
	}

	if len(res) != 2 || len(res[0].ID) != 24 || res[1].ID != "given" || res[0].V != 1 {
		t.Fatalf("results = %+v", res)
	}

	got, err := notes.Get(ctx, res[0].ID)
	if err != nil || got.ID != res[0].ID || got.Title != "a" {
		t.Fatalf("got %+v err=%v", got, err)
	}
}

func TestUntaggedFieldsUseJSONNameOrFieldName(t *testing.T) {
	s, err := schemaOf(reflect.TypeFor[Note]())
	if err != nil {
		t.Fatal(err)
	}

	raw, err := s.encode(Note{ID: "n1", Title: "t", Body: "b", Skip: "x", Draft: true, note: "y"})
	if err != nil {
		t.Fatal(err)
	}

	want := `{"Body":"b","_id":"n1","title":"t"}`

	if string(raw) != want {
		t.Fatalf("encoded %s, want %s", raw, want)
	}
}

func TestIterWalksAllPages(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	notes, err := Collection[Note](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	for i := range 25 {
		if _, err := notes.Insert(ctx, Note{ID: fmt.Sprintf("n%02d", i)}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}

	for n, err := range notes.Where().Sort(Asc("_id")).Limit(10).Iter(ctx) {
		if err != nil {
			t.Fatal(err)
		}

		if seen[n.ID] {
			t.Fatalf("%s seen twice", n.ID)
		}

		seen[n.ID] = true
	}

	if len(seen) != 25 {
		t.Fatalf("Iter gave %d rows, want 25 (limit 10 is the batch size)", len(seen))
	}
}
