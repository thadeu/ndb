package index

import (
	"bytes"
	"slices"
	"testing"
)

func keysOf(t *testing.T, doc map[string]any, name, timeField string) [][]byte {
	t.Helper()

	return DocKeys(doc, name, timeField)
}

func TestDocKeysOfOneFieldAreTheKeysOfBefore(t *testing.T) {
	doc := map[string]any{"tags": []any{"x", "y", "x"}, "n": float64(3)}

	if got, want := keysOf(t, doc, "tags", ""), Keys(doc, "tags", false); !slices.EqualFunc(got, want, bytes.Equal) {
		t.Fatalf("got %v, want %v", got, want)
	}

	if got := keysOf(t, doc, "nope", ""); len(got) != 0 {
		t.Fatalf("a missing field gave %d keys", len(got))
	}
}

func TestTupleKeysSortAsTheTuple(t *testing.T) {
	docs := []map[string]any{
		{"a": "acc1", "b": float64(2)},
		{"a": "acc1", "b": float64(10)},
		{"a": "acc1"},
		{"a": "acc1", "b": "x"},
		{"a": "acc10", "b": float64(1)},
		{"a": "acc2", "b": float64(-5)},
	}

	var keys [][]byte

	for _, d := range docs {
		keys = append(keys, keysOf(t, d, "a,b", "")...)
	}

	sorted := slices.Clone(keys)
	slices.SortFunc(sorted, bytes.Compare)

	// acc1 with no b, then b=2, b=10, b="x" (missing < number < string),
	// then acc10 (a longer string of the same start), then acc2.
	want := [][]byte{keys[2], keys[0], keys[1], keys[3], keys[4], keys[5]}

	if !slices.EqualFunc(sorted, want, bytes.Equal) {
		t.Fatalf("tuple keys do not sort as the tuples")
	}
}

func TestTupleKeysOfArraysAndMissingFields(t *testing.T) {
	doc := map[string]any{"a": []any{"x", "y"}, "b": []any{float64(1), float64(2), float64(1)}}

	if got := keysOf(t, doc, "a,b", ""); len(got) != 4 {
		t.Fatalf("%d keys, want 4 (2 x 2 distinct elements)", len(got))
	}

	if got := keysOf(t, map[string]any{"b": float64(1)}, "a,b", ""); len(got) != 0 {
		t.Fatalf("a document with no first field gave %d keys", len(got))
	}

	if got := keysOf(t, map[string]any{"a": "x"}, "a,b,c", ""); len(got) != 1 {
		t.Fatalf("a document with only the first field gave %d keys, want 1", len(got))
	}

	big := make([]any, 40)
	for i := range big {
		big[i] = float64(i)
	}

	over := keysOf(t, map[string]any{"a": big, "b": big}, "a,b", "")
	last := keysOf(t, map[string]any{"a": "\xff\xff", "b": true}, "a,b", "")[0]

	if len(over) != 1 || bytes.Compare(over[0], Overflow().Lo) < 0 || bytes.Compare(over[0], last) <= 0 {
		t.Fatalf("40 x 40 pairs gave %d keys, want the one overflow key after every tuple", len(over))
	}
}

func TestTuplePrefixCoversExactlyItsTuples(t *testing.T) {
	in := keysOf(t, map[string]any{"a": "acc1", "b": float64(7)}, "a,b", "")[0]
	longer := keysOf(t, map[string]any{"a": "acc10", "b": float64(7)}, "a,b", "")[0]
	prefix := keysOf(t, map[string]any{"a": "acc1"}, "a", "")[0]
	end := PrefixEnd(prefix)

	if bytes.Compare(in, prefix) < 0 || bytes.Compare(in, end) >= 0 {
		t.Fatal("the key of acc1 is not inside the prefix of acc1")
	}

	if bytes.Compare(longer, end) < 0 && bytes.Compare(longer, prefix) >= 0 {
		t.Fatal("the key of acc10 is inside the prefix of acc1")
	}
}

func TestSplitAndCheckName(t *testing.T) {
	for _, bad := range []string{"a,", ",a", "a,,b", "a,a", "a,b,c,d,e", "a,_id", ""} {
		if err := CheckName(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}

	for _, good := range []string{"a", "a,b", "owner.id,created_at", "a,b,c,d"} {
		if err := CheckName(good); err != nil {
			t.Errorf("%q: %v", good, err)
		}
	}

	if got := Fields("a,b.c"); !slices.Equal(got, []string{"a", "b.c"}) {
		t.Fatalf("Fields = %v", got)
	}
}
