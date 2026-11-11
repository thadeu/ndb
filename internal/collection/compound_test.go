package collection_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
)

func TestCompoundIndexPlans(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Indexes: []string{"acc,n", "status"}})

	seedN(t, c, 400, func(i int) string {
		return fmt.Sprintf(`{"_id":"d%03d","acc":"a%d","n":%d,"status":"s%d"}`, i, i%4, i, i%2)
	})

	waitReady(t, c)

	cases := []struct {
		body     string
		plan     string
		rows     int
		examined int
	}{
		{`{"query":{"acc":"a1","n":{"$gte":100,"$lt":140}}}`, "index:acc,n", 10, 10},
		{`{"query":{"acc":"a1","n":101}}`, "index:acc,n", 1, 1},
		{`{"query":{"acc":"a1"}}`, "index:acc,n", 100, 100},
		{`{"query":{"acc":{"$in":["a1","a2"]},"n":{"$lt":8}}}`, "index:acc,n", 4, 4},
		{`{"query":{"acc":"a1","n":{"$in":[1,5,6]}}}`, "index:acc,n", 2, 2},
		{`{"query":{"acc":"a1","n":{"$gt":389}}}`, "index:acc,n", 2, 2},
		{`{"query":{"acc":"a1","n":{"$lte":9}}}`, "index:acc,n", 3, 3},
		{`{"query":{"acc":"a1","status":"s1","n":{"$lt":20}}}`, "index:acc,n", 5, 5},
		{`{"query":{"n":{"$lt":8}}}`, "scan", 8, 400},
		{`{"query":{"status":"s1"}}`, "index:status", 200, 200},
	}

	for _, cs := range cases {
		p := rq(t, c, cs.body)

		if p.Plan != cs.plan || len(p.Rows) != cs.rows || p.Examined != cs.examined {
			t.Errorf("%s: plan %s rows %d examined %d, want %s %d %d", cs.body, p.Plan, len(p.Rows), p.Examined, cs.plan, cs.rows, cs.examined)
		}
	}
}

func TestCompoundIndexFollowsWrites(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Indexes: []string{"acc,tags"}})

	seed(t, c, `{"_id":"a","acc":"x","tags":["p","q"]}`, `{"_id":"b","acc":"x","tags":"q"}`, `{"_id":"c","acc":"x"}`, `{"_id":"d","tags":"q"}`)
	waitReady(t, c)

	eq(t, ids(t, rq(t, c, `{"query":{"acc":"x","tags":"q"},"sort":{"_id":1}}`)), []string{"a", "b"})
	eq(t, ids(t, rq(t, c, `{"query":{"acc":"x"},"sort":{"_id":1}}`)), []string{"a", "b", "c"})

	c.UpdateByID(ctx, "a", collection.Change{Set: map[string]any{"tags": []any{"z"}}}, 0)
	c.UpdateByID(ctx, "c", collection.Change{Set: map[string]any{"tags": "q"}}, 0)
	c.UpdateByID(ctx, "d", collection.Change{Set: map[string]any{"acc": "x"}}, 0)
	c.DeleteByID(ctx, "b")

	p := rq(t, c, `{"query":{"acc":"x","tags":"q"},"sort":{"_id":1}}`)

	eq(t, ids(t, p), []string{"c", "d"})

	if p.Plan != "index:acc,tags" || p.Examined != 2 {
		t.Fatalf("plan %s examined %d", p.Plan, p.Examined)
	}
}

func TestCompoundIndexOnTheTimeField(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Time: "at", Indexes: []string{"acc,at"}})

	seedN(t, c, 48, func(i int) string {
		return fmt.Sprintf(`{"_id":"e%02d","acc":"a%d","at":"2026-11-%02dT%02d:00:00Z"}`, i, i%2, 1+i/24, i%24)
	})

	waitReady(t, c)

	p := rq(t, c, `{"query":{"acc":"a0","at":{"$gte":"2026-11-02T00:00:00Z","$lt":"2026-11-02T06:00:00Z"}},"sort":{"at":1}}`, "at")

	eq(t, ids(t, p), []string{"e24", "e26", "e28"})

	if p.Plan != "index:acc,at" || p.Examined != 3 {
		t.Fatalf("plan %s examined %d", p.Plan, p.Examined)
	}
}

func TestCompoundIndexNames(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	for _, bad := range []string{"a,", "a,a", "a,b,c,d,e", "a,_id"} {
		if err := c.EnsureIndex(bad); err == nil {
			t.Errorf("EnsureIndex(%q) was accepted", bad)
		}
	}

	if err := c.EnsureIndex("a,b"); err != nil {
		t.Fatal(err)
	}

	waitReady(t, c)

	if got := c.Indexes(); len(got) != 1 || got[0].Field != "a,b" {
		t.Fatalf("indexes %+v", got)
	}

	c.DropIndex("a,b")

	if p := rq(t, c, `{"query":{"a":1,"b":2}}`); p.Plan != "scan" {
		t.Fatalf("plan %s after the drop", p.Plan)
	}
}

// The same random queries on a collection with compound indexes and on one
// with none must give the same documents: in the memtable, in segments
// (key files written by the seal, and built later), and with both.
func TestCompoundIndexAnswersLikeAScan(t *testing.T) {
	r := rand.New(rand.NewPCG(26, 1))
	root := t.TempDir()

	openAt := func(name string, indexes []string) *collection.Collection {
		c := openData(t, filepath.Join(root, name), catalog.Def{Indexes: indexes})

		t.Cleanup(func() { c.Close() })

		return c
	}

	plain := openAt("plain", nil)
	indexed := openAt("indexed", []string{"a,b", "b,c,a"})
	late := openAt("late", nil)
	all := []*collection.Collection{plain, indexed, late}

	value := func() string {
		switch r.IntN(12) {
		case 0:
			return `null`
		case 1:
			return fmt.Sprintf(`[%d,%d]`, r.IntN(4), r.IntN(4))
		case 2:
			return fmt.Sprintf(`"s%d"`, r.IntN(3))
		case 3:
			return `[]`
		case 4:
			return `true`
		}

		return fmt.Sprint(r.IntN(6))
	}

	write := func(from, to int) {
		docs := make([][]byte, 0, to-from)

		for i := from; i < to; i++ {
			fields := []string{fmt.Sprintf(`"_id":"d%04d"`, i)}

			for _, f := range []string{"a", "b", "c"} {
				if r.IntN(8) > 0 {
					fields = append(fields, fmt.Sprintf(`"%s":%s`, f, value()))
				}
			}

			docs = append(docs, []byte("{"+strings.Join(fields, ",")+"}"))
		}

		for _, c := range all {
			if _, err := c.Insert(ctx, docs...); err != nil {
				t.Fatal(err)
			}
		}
	}

	cond := func() string {
		switch r.IntN(6) {
		case 0:
			return fmt.Sprintf(`{"$gte":%d}`, r.IntN(6))
		case 1:
			return fmt.Sprintf(`{"$lt":%d}`, r.IntN(6))
		case 2:
			return fmt.Sprintf(`{"$gt":%d,"$lte":%d}`, r.IntN(3), 2+r.IntN(4))
		case 3:
			return fmt.Sprintf(`{"$in":[%d,"s%d"]}`, r.IntN(6), r.IntN(3))
		case 4:
			return fmt.Sprintf(`"s%d"`, r.IntN(3))
		}

		return fmt.Sprint(r.IntN(6))
	}

	used := 0

	check := func(stage string) {
		for range 300 {
			var parts []string

			for _, f := range []string{"a", "b", "c"} {
				if r.IntN(3) > 0 {
					parts = append(parts, fmt.Sprintf(`"%s":%s`, f, cond()))
				}
			}

			body := `{"query":{` + strings.Join(parts, ",") + `},"sort":{"_id":1},"limit":5000}`
			want := ids(t, rq(t, plain, body))

			for _, c := range all[1:] {
				p := rq(t, c, body)

				if strings.HasPrefix(p.Plan, "index:") {
					used++
				}

				if got := ids(t, p); !slices.Equal(got, want) {
					t.Fatalf("%s: %s (plan %s): got %d documents, want %d\n got %v\nwant %v", stage, body, p.Plan, len(got), len(want), got, want)
				}
			}
		}
	}

	write(0, 600)
	waitReady(t, indexed)
	check("memtable")

	for _, c := range all {
		if err := c.Seal(); err != nil {
			t.Fatal(err)
		}
	}

	check("sealed")

	for _, name := range []string{"a,b", "b,c,a"} {
		if err := late.EnsureIndex(name); err != nil {
			t.Fatal(err)
		}
	}

	waitReady(t, late)
	write(600, 900)
	check("sealed and memtable, an index built later")

	for i := 0; i < 900; i += 7 {
		for _, c := range all {
			if i%2 == 0 {
				c.DeleteByID(ctx, fmt.Sprintf("d%04d", i))
			} else {
				c.UpdateByID(ctx, fmt.Sprintf("d%04d", i), collection.Change{Set: map[string]any{"b": i % 6}, Unset: []string{"a"}}, 0)
			}
		}
	}

	check("after updates and deletes")

	if used < 300 {
		t.Fatalf("the compound indexes answered only %d of the queries: the test does not test them", used)
	}
}

func TestCompoundIndexWithMoreKeysThanTheLimit(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Indexes: []string{"a,b"}})

	var elems []string

	for i := range 40 {
		elems = append(elems, fmt.Sprint(i))
	}

	arr := "[" + strings.Join(elems, ",") + "]"

	seed(t, c, `{"_id":"big","a":`+arr+`,"b":`+arr+`}`, `{"_id":"small","a":5,"b":7}`, `{"_id":"other","a":5,"b":8}`)
	waitReady(t, c)

	p := rq(t, c, `{"query":{"a":5,"b":7},"sort":{"_id":1}}`)

	eq(t, ids(t, p), []string{"big", "small"})

	if p.Plan != "index:a,b" {
		t.Fatalf("plan %s", p.Plan)
	}
}

func uniqueErr(t *testing.T, err error) {
	t.Helper()

	var coded interface{ Code() string }

	if !errors.As(err, &coded) || coded.Code() != "unique_violation" {
		t.Fatalf("err %v, want unique_violation", err)
	}
}

func TestCompoundUnique(t *testing.T) {
	dir := t.TempDir()
	def := catalog.Def{Unique: []string{"acc,email"}}
	c := open(t, dir, def)

	seed(t, c,
		`{"_id":"u1","acc":"a","email":"x@x"}`,
		`{"_id":"u2","acc":"b","email":"x@x"}`,
		`{"_id":"u3","acc":"a","email":"y@x"}`,
		`{"_id":"u4","acc":"a"}`,
		`{"_id":"u5","acc":"a"}`,
		`{"_id":"u6","email":"x@x"}`,
		`{"_id":"u7","acc":"a","email":null}`,
	)

	_, err := c.Insert(ctx, doc(`{"_id":"dup","acc":"a","email":"x@x"}`))
	uniqueErr(t, err)

	if !strings.Contains(err.Error(), "acc,email") {
		t.Fatalf("the error does not name the key: %v", err)
	}

	_, err = c.UpdateByID(ctx, "u3", collection.Change{Set: map[string]any{"email": "x@x"}}, 0)
	uniqueErr(t, err)

	_, err = c.UpdateByID(ctx, "u4", collection.Change{Set: map[string]any{"email": "y@x"}}, 0)
	uniqueErr(t, err)

	_, err = c.Insert(ctx, doc(`{"_id":"n1","acc":"z","email":"p"}`), doc(`{"_id":"n2","acc":"z","email":"p"}`))
	uniqueErr(t, err)

	if _, err := c.UpdateByID(ctx, "u1", collection.Change{Set: map[string]any{"email": "z@x"}}, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Insert(ctx, doc(`{"_id":"free","acc":"a","email":"x@x"}`)); err != nil {
		t.Fatalf("the tuple was freed by the update: %v", err)
	}

	if err := c.DeleteByID(ctx, "free"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Insert(ctx, doc(`{"_id":"again","acc":"a","email":"x@x"}`)); err != nil {
		t.Fatalf("the tuple was freed by the delete: %v", err)
	}

	c.Close()

	c = open(t, dir, def)

	_, err = c.Insert(ctx, doc(`{"_id":"dup2","acc":"a","email":"x@x"}`))
	uniqueErr(t, err)
}

func TestCompoundUniqueInSegments(t *testing.T) {
	root := t.TempDir()
	def := catalog.Def{Name: "users", Unique: []string{"acc,email"}}

	openIt := func() *collection.Collection { return openData(t, root, def) }

	c := openIt()

	seed(t, c, `{"_id":"u1","acc":"a","email":"x@x"}`, `{"_id":"u2","acc":"b","email":"x@x"}`)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	_, err := c.Insert(ctx, doc(`{"_id":"dup","acc":"a","email":"x@x"}`))
	uniqueErr(t, err)

	c.Close()

	c = openIt()
	defer c.Close()

	_, err = c.Insert(ctx, doc(`{"_id":"dup","acc":"b","email":"x@x"}`))
	uniqueErr(t, err)

	if _, err := c.Insert(ctx, doc(`{"_id":"ok","acc":"c","email":"x@x"}`)); err != nil {
		t.Fatal(err)
	}
}
