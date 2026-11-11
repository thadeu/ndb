package collection_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
)

// A range of two sides matches an array when one element passes each side,
// and they need not be the same element: [1, 3] matches b > 1 and b <= 2.
// An index of the field must give the same documents as a scan.
func TestIndexRangeOfTwoSidesOnAnArrayField(t *testing.T) {
	root := t.TempDir()
	def := catalog.Def{Indexes: []string{"b"}}
	c := openData(t, root, def)

	seed(t, c, `{"_id":"x","b":[1,3]}`, `{"_id":"y","b":2}`, `{"_id":"z","b":[0,5]}`, `{"_id":"w","b":[7,9]}`, `{"_id":"v","b":0}`)
	waitReady(t, c)

	body := `{"query":{"b":{"$gt":1,"$lte":2}},"sort":{"_id":1}}`

	check := func(stage string, want ...string) {
		t.Helper()

		p := rq(t, c, body)

		if p.Plan != "index:b" || !slices.Equal(ids(t, p), want) {
			t.Fatalf("%s: plan %s, got %v, want %v", stage, p.Plan, ids(t, p), want)
		}
	}

	check("memtable", "x", "y", "z")

	c.UpdateByID(ctx, "z", collection.Change{Set: map[string]any{"b": 5}}, 0)
	c.UpdateByID(ctx, "v", collection.Change{Set: map[string]any{"b": []any{0, 9}}}, 0)
	check("memtable after updates", "v", "x", "y")

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	check("sealed", "v", "x", "y")

	c.DeleteByID(ctx, "x")
	seed(t, c, `{"_id":"n","b":[2,2]}`, `{"_id":"m","b":[1,9]}`)
	check("sealed and memtable", "m", "n", "v", "y")

	c.Close()

	c = openData(t, root, def)
	defer c.Close()

	waitReady(t, c)
	check("after a restart", "m", "n", "v", "y")
}

func TestIndexRangesAnswerLikeAScanWithArrays(t *testing.T) {
	r := rand.New(rand.NewPCG(26, 4))
	root := t.TempDir()

	plain := openData(t, root+"/plain", catalog.Def{})
	indexed := openData(t, root+"/indexed", catalog.Def{Indexes: []string{"b"}})

	defer plain.Close()
	defer indexed.Close()

	write := func(from, to int) {
		docs := make([][]byte, 0, to-from)

		for i := from; i < to; i++ {
			b := fmt.Sprint(r.IntN(10))

			switch r.IntN(6) {
			case 0:
				b = fmt.Sprintf("[%d,%d]", r.IntN(10), r.IntN(10))
			case 1:
				b = fmt.Sprintf("[%d,%d,%d]", r.IntN(10), r.IntN(10), r.IntN(10))
			case 2:
				b = `"s"`
			}

			docs = append(docs, []byte(fmt.Sprintf(`{"_id":"d%04d","b":%s}`, i, b)))
		}

		for _, c := range []*collection.Collection{plain, indexed} {
			if _, err := c.Insert(ctx, docs...); err != nil {
				t.Fatal(err)
			}
		}
	}

	check := func(stage string) {
		for range 200 {
			lo, hi := r.IntN(10), r.IntN(10)
			ops := []string{"$gt", "$gte"}[r.IntN(2)] + `":` + fmt.Sprint(lo) + `,"` + []string{"$lt", "$lte"}[r.IntN(2)] + `":` + fmt.Sprint(hi)
			body := `{"query":{"b":{"` + ops + `}},"sort":{"_id":1},"limit":5000}`

			p := rq(t, indexed, body)

			if got, want := ids(t, p), ids(t, rq(t, plain, body)); p.Plan != "index:b" || !slices.Equal(got, want) {
				t.Fatalf("%s: %s (plan %s): got %d documents, want %d\nmissing or extra: %v", stage, body, p.Plan, len(got), len(want), diff(got, want))
			}
		}
	}

	write(0, 500)
	waitReady(t, indexed)
	check("memtable")

	for _, c := range []*collection.Collection{plain, indexed} {
		if err := c.Seal(); err != nil {
			t.Fatal(err)
		}
	}

	check("sealed")
	write(500, 800)
	check("sealed and memtable")
}

func diff(got, want []string) string {
	var out []string

	for _, id := range want {
		if !slices.Contains(got, id) {
			out = append(out, "-"+id)
		}
	}

	for _, id := range got {
		if !slices.Contains(want, id) {
			out = append(out, "+"+id)
		}
	}

	return strings.Join(out, " ")
}
