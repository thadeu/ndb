package query_test

import (
	"testing"

	"github.com/thadeu/ndb/internal/query"
)

func TestSortKeysArrayMinMax(t *testing.T) {
	q, _ := query.Parse([]byte(`{"sort":{"n":1}}`), query.Options{})
	d, _ := query.Parse([]byte(`{"sort":{"n":-1}}`), query.Options{})
	doc := decode(t, `{"n":[5,2,9]}`)

	if v := q.SortKeys(doc)[0]; v.Num != 2 {
		t.Fatalf("asc picks min, got %+v", v)
	}

	if v := d.SortKeys(doc)[0]; v.Num != 9 {
		t.Fatalf("desc picks max, got %+v", v)
	}

	if v := q.SortKeys(decode(t, `{}`))[0]; v.Kind != query.Missing {
		t.Fatalf("missing, got %+v", v)
	}
}

func TestCmpTieBreaksOnID(t *testing.T) {
	q, _ := query.Parse([]byte(`{"sort":{"n":1}}`), query.Options{})
	a := query.Pos{Keys: []query.Value{query.FromJSON(1.0, false)}, ID: "a"}
	b := query.Pos{Keys: []query.Value{query.FromJSON(1.0, false)}, ID: "b"}

	if q.Cmp(a, b) >= 0 || q.Cmp(b, a) <= 0 || q.Cmp(a, a) != 0 {
		t.Fatal("asc tiebreak")
	}

	d, _ := query.Parse([]byte(`{"sort":{"n":-1}}`), query.Options{})

	if d.Cmp(a, b) <= 0 {
		t.Fatal("desc sort reverses the id tiebreak too")
	}
}

func TestCmpNoSortUsesSeq(t *testing.T) {
	q, _ := query.Parse([]byte(`{}`), query.Options{})

	if q.Cmp(query.Pos{Seq: 1, ID: "z"}, query.Pos{Seq: 2, ID: "a"}) >= 0 {
		t.Fatal("seq order")
	}
}

func TestCursorRoundTripAndHash(t *testing.T) {
	q, _ := query.Parse([]byte(`{"where":{"a":1},"sort":{"at":-1},"limit":3}`), query.Options{TimeField: "at"})
	pos := query.Pos{Keys: []query.Value{query.FromJSON("2026-11-04T12:00:00Z", true)}, ID: "c9", Seq: 7}

	tok := q.EncodeCursor(pos, "snap1")

	got, snap, err := q.DecodeCursor(tok)
	if err != nil {
		t.Fatal(err)
	}

	if snap != "snap1" || got.ID != "c9" || got.Seq != 7 || query.Compare(got.Keys[0], pos.Keys[0]) != 0 || got.Keys[0].Kind != query.Time {
		t.Fatalf("%+v %s", got, snap)
	}

	other, _ := query.Parse([]byte(`{"where":{"a":2},"sort":{"at":-1}}`), query.Options{TimeField: "at"})

	if _, _, err := other.DecodeCursor(tok); err == nil || err.(*query.Error).Code() != "cursor_mismatch" {
		t.Fatalf("want cursor_mismatch, got %v", err)
	}

	same, _ := query.Parse([]byte(`{"where":{"a":1},"sort":{"at":-1},"limit":50,"skip":4,"fields":["a"]}`), query.Options{TimeField: "at"})

	if _, _, err := same.DecodeCursor(tok); err != nil {
		t.Fatalf("limit, skip and fields must not change the hash: %v", err)
	}
}

func TestCursorGarbage(t *testing.T) {
	q, _ := query.Parse([]byte(`{}`), query.Options{})

	for _, tok := range []string{"!!!", "e30", "bm90IGpzb24"} {
		if _, _, err := q.DecodeCursor(tok); err == nil {
			t.Fatalf("%q: want error", tok)
		}
	}
}
