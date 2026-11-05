package index_test

import (
	"bytes"
	"math"
	"math/rand"
	"slices"
	"testing"

	"github.com/thadeu/ndb/internal/index"
	"github.com/thadeu/ndb/internal/query"
)

func val(v any) query.Value {
	return query.FromJSON(v, true)
}

// Encode must keep the order of query.Compare, for every pair of types.
func TestEncodeKeepsOrder(t *testing.T) {
	vals := []query.Value{
		val(nil), val(math.Inf(-1)), val(-1e9), val(-1.5), val(-1e-9), val(0.0), val(1e-9), val(1.0), val(1.5), val(1e9), val(math.Inf(1)),
		val(""), val("a"), val("a\x00"), val("a\x00b"), val("ab"), val("b"),
		val("2020-01-01T00:00:00Z"), val("2026-11-04T12:00:00Z"), val("2026-11-04T12:00:00.5Z"), val("2300-01-01T00:00:00Z"),
		val(map[string]any{}), val(map[string]any{"a": 1.0}),
		val([]any{}), val([]any{1.0}),
		val(false), val(true),
	}

	for _, a := range vals {
		for _, b := range vals {
			want := query.Compare(a, b)
			got := bytes.Compare(index.Encode(a), index.Encode(b))

			if sign(got) != sign(want) {
				t.Fatalf("%+v vs %+v: bytes %d, Compare %d", a, b, got, want)
			}
		}
	}
}

func TestEncodeNegativeZero(t *testing.T) {
	if !bytes.Equal(index.Encode(val(math.Copysign(0, -1))), index.Encode(val(0.0))) {
		t.Fatal("-0 and 0 must have one key")
	}
}

func TestEncodeRandomNumbers(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	nums := make([]float64, 500)

	for i := range nums {
		nums[i] = (r.Float64() - 0.5) * math.Pow(10, float64(r.Intn(20)-10))
	}

	byKey := slices.Clone(nums)
	slices.SortFunc(byKey, func(a, b float64) int { return bytes.Compare(index.Encode(val(a)), index.Encode(val(b))) })
	slices.Sort(nums)

	if !slices.Equal(nums, byKey) {
		t.Fatal("key order differs from numeric order")
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}

	return 0
}

func collect(m *index.Mem, r index.Range) []string {
	var ids []string

	m.Ascend(r, func(id string) bool {
		ids = append(ids, id)

		return true
	})

	return ids
}

func TestMemRanges(t *testing.T) {
	m := index.NewMem()

	for i, v := range []any{3.0, 1.0, 2.0, 2.0, "x", nil, true} {
		m.Add(index.Encode(val(v)), string(rune('a'+i)))
	}

	two := index.Encode(val(2.0))

	cases := []struct {
		name string
		r    index.Range
		want []string
	}{
		{"eq", index.Eq(val(2.0)), []string{"c", "d"}},
		{"gt number only", index.Cmp("gt", val(1.0)), []string{"c", "d", "a"}},
		{"gte", index.Cmp("gte", val(2.0)), []string{"c", "d", "a"}},
		{"lt skips null", index.Cmp("lt", val(3.0)), []string{"b", "c", "d"}},
		{"lte", index.Cmp("lte", val(2.0)), []string{"b", "c", "d"}},
		{"string class", index.Cmp("gte", val("")), []string{"e"}},
		{"bool class", index.Cmp("lte", val(true)), []string{"g"}},
		{"null eq", index.Eq(val(nil)), []string{"f"}},
		{"raw bounds", index.Range{Lo: two, LoIncl: true}, []string{"c", "d", "a", "e", "g"}},
		{"nothing", index.Eq(val(99.0)), nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := collect(m, c.r); !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestMemAddRemoveAndStop(t *testing.T) {
	m := index.NewMem()
	k := index.Encode(val("k"))

	m.Add(k, "a")
	m.Add(k, "a")
	m.Add(k, "b")

	if m.Len() != 2 {
		t.Fatalf("Len = %d", m.Len())
	}

	m.Remove(k, "a")
	m.Remove(k, "nope")

	if got := collect(m, index.Eq(val("k"))); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("got %v", got)
	}

	m.Add(k, "c")

	n := 0

	m.Ascend(index.Eq(val("k")), func(string) bool {
		n++

		return false
	})

	if n != 1 {
		t.Fatalf("callback false must stop, called %d", n)
	}
}

func TestKeysOneEntryPerArrayElement(t *testing.T) {
	doc := map[string]any{"tags": []any{"b", "a", "b"}, "n": 1.0, "empty": []any{}}

	if got := index.Keys(doc, "tags", false); len(got) != 2 {
		t.Fatalf("duplicate elements share one key, got %d", len(got))
	}

	if got := index.Keys(doc, "n", false); len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}

	if got := index.Keys(doc, "empty", false); len(got) != 0 {
		t.Fatalf("empty array has no entry, got %d", len(got))
	}

	if got := index.Keys(doc, "missing", false); len(got) != 0 {
		t.Fatalf("missing has no entry, got %d", len(got))
	}
}
