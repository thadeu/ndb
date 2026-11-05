package query_test

import (
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/query"
)

var cutoff = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func TestHideRemovesRowsAtOrBeforeTheCutoff(t *testing.T) {
	q, err := query.Parse([]byte(`{"where":{"a":1}}`), query.Options{ExpiresField: "exp"})
	if err != nil {
		t.Fatal(err)
	}

	if err := q.Hide(query.ExpiredWhere("exp", cutoff)); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, doc string
		visible   bool
	}{
		{"past rfc3339", `{"a":1,"exp":"2020-01-01T00:00:00Z"}`, false},
		{"future rfc3339", `{"a":1,"exp":"2999-01-01T00:00:00Z"}`, true},
		{"past unix seconds", `{"a":1,"exp":1577836800}`, false},
		{"future unix seconds", `{"a":1,"exp":32503680000}`, true},
		{"missing never expires", `{"a":1}`, true},
		{"null never expires", `{"a":1,"exp":null}`, true},
		{"unparseable never expires", `{"a":1,"exp":"soon"}`, true},
		{"the user where still applies", `{"a":2,"exp":"2999-01-01T00:00:00Z"}`, false},
	}

	for _, c := range cases {
		if got := q.Match(decode(t, c.doc)); got != c.visible {
			t.Errorf("%s: visible=%v, want %v", c.name, got, c.visible)
		}
	}
}

func TestHideKeepsTheIndexableConditions(t *testing.T) {
	q, err := query.Parse([]byte(`{"where":{"a":1}}`), query.Options{ExpiresField: "exp"})
	if err != nil {
		t.Fatal(err)
	}

	if err := q.Hide(query.ExpiredWhere("exp", cutoff)); err != nil {
		t.Fatal(err)
	}

	conds := q.Indexable()

	if len(conds) != 1 || conds[0].Path != "a" {
		t.Fatalf("conds: %+v", conds)
	}
}

func TestHideDoesNotChangeTheCursorHash(t *testing.T) {
	a, _ := query.Parse([]byte(`{"where":{"a":1}}`), query.Options{ExpiresField: "exp"})
	b, _ := query.Parse([]byte(`{"where":{"a":1}}`), query.Options{ExpiresField: "exp"})

	if err := b.Hide(query.ExpiredWhere("exp", cutoff)); err != nil {
		t.Fatal(err)
	}

	if a.Hash() != b.Hash() {
		t.Fatal("a hide must not change the hash, or a cursor breaks when the clock moves")
	}
}
