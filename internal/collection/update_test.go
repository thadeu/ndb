package collection_test

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
)

func upd(t *testing.T, c *collection.Collection, body string, opts collection.UpdateOpts) collection.UpdateResult {
	t.Helper()

	res, err := c.Update(ctx, []byte(body), opts)
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}

	return res
}

func docOf(t *testing.T, c *collection.Collection, id string) map[string]any {
	t.Helper()

	d, ok := c.Find(id)
	if !ok {
		t.Fatalf("%s not found", id)
	}

	var m map[string]any

	if err := json.Unmarshal(d.Doc, &m); err != nil {
		t.Fatal(err)
	}

	return m
}

func TestConcurrentIncNeverLosesAnUpdate(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1","n":0}`))

	const workers = 40

	var wg sync.WaitGroup

	for range workers {
		wg.Go(func() {
			if res, err := c.Update(ctx, []byte(`{"_id":"p1","$inc":{"n":1}}`), collection.UpdateOpts{}); err != nil || res.Modified != 1 {
				t.Errorf("res=%+v err=%v", res, err)
			}
		})
	}

	wg.Wait()

	if n := docOf(t, c, "p1")["n"]; n != float64(workers) {
		t.Fatalf("n = %v, want %d", n, workers)
	}
}

func TestGuardedDecrementWinsOnce(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"p1","qty":1}`))

	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)

	for range 2 {
		wg.Go(func() {
			res, err := c.Update(ctx, []byte(`{"_id":"p1","qty":{"$gte":1},"$inc":{"qty":-1}}`), collection.UpdateOpts{})
			if err != nil {
				t.Error(err)

				return
			}

			mu.Lock()
			ok += res.Modified
			mu.Unlock()
		})
	}

	wg.Wait()

	if qty := docOf(t, c, "p1")["qty"]; ok != 1 || qty != 0.0 {
		t.Fatalf("modified %d, qty %v", ok, qty)
	}
}

func TestUpdateResultShapes(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Indexes: []string{"g"}})

	c.Insert(ctx, doc(`{"_id":"a","g":1,"n":1}`), doc(`{"_id":"b","g":1,"n":1}`), doc(`{"_id":"c","g":2,"n":1}`))

	res := upd(t, c, `{"_id":"a","$set":{"n":5}}`, collection.UpdateOpts{})

	var doc map[string]any

	json.Unmarshal(res.Doc, &doc)

	if res.Matched != 1 || res.Modified != 1 || doc["_v"] != 2.0 || doc["n"] != 5.0 {
		t.Fatalf("single: %+v %s", res, res.Doc)
	}

	res = upd(t, c, `{"_id":"zzz","$set":{"n":5}}`, collection.UpdateOpts{})

	if res.Matched != 0 || res.Modified != 0 || res.Doc != nil {
		t.Fatalf("no match: %+v", res)
	}

	res = upd(t, c, `{"_id":"a","_v":1,"$set":{"n":9}}`, collection.UpdateOpts{})

	if res.Matched != 0 {
		t.Fatalf("stale _v matched: %+v", res)
	}

	res = upd(t, c, `{"_id":"a","$set":{"n":5}}`, collection.UpdateOpts{})

	if res.Matched != 1 || res.Modified != 0 || docOf(t, c, "a")["n"] != 5.0 {
		t.Fatalf("same value: %+v", res)
	}

	if d, _ := c.Find("a"); d.V != 2 {
		t.Fatalf("an unchanged update wrote version %d", d.V)
	}

	res = upd(t, c, `{"g":1,"$inc":{"n":10}}`, collection.UpdateOpts{Many: true})

	if res.Matched != 2 || res.Modified != 2 || res.Doc != nil {
		t.Fatalf("many: %+v", res)
	}

	if docOf(t, c, "b")["n"] != 11.0 || docOf(t, c, "c")["n"] != 1.0 {
		t.Fatal("many touched the wrong documents")
	}
}

func TestUpdateWithoutIDTakesAMatch(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"a","s":"x"}`), doc(`{"_id":"b","s":"y"}`))

	res := upd(t, c, `{"s":"y","$set":{"done":true}}`, collection.UpdateOpts{})

	if res.Matched != 1 || docOf(t, c, "b")["done"] != true || docOf(t, c, "a")["done"] != nil {
		t.Fatalf("%+v", res)
	}
}

func TestManyNeedsAnIndex(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"a","s":"x"}`))

	_, err := c.Update(ctx, []byte(`{"s":"x","$set":{"n":1}}`), collection.UpdateOpts{Many: true})

	if code(err) != "unindexed_many" {
		t.Fatalf("got %v", err)
	}

	_, err = c.Delete(ctx, []byte(`{"s":"x"}`), true)

	if code(err) != "unindexed_many" {
		t.Fatalf("delete: got %v", err)
	}
}

func TestUpsert(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	res := upd(t, c, `{"_id":"u1","meta.k":"v","n":{"$gte":1},"$inc":{"count":2},"$push":{"tags":"a"},"$min":{"lo":4},"$max":{"hi":4}}`, collection.UpdateOpts{Upsert: true})

	if res.Matched != 0 || res.Modified != 1 {
		t.Fatalf("%+v", res)
	}

	m := docOf(t, c, "u1")

	if m["count"] != 2.0 || m["lo"] != 4.0 || m["hi"] != 4.0 || m["n"] != nil || m["meta"].(map[string]any)["k"] != "v" || len(m["tags"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}

	res = upd(t, c, `{"_id":"u1","$inc":{"count":2}}`, collection.UpdateOpts{Upsert: true})

	if res.Matched != 1 || docOf(t, c, "u1")["count"] != 4.0 {
		t.Fatalf("second upsert: %+v", res)
	}

	_, err := c.Update(ctx, []byte(`{"_id":"u1","count":{"$gt":100},"$inc":{"count":1}}`), collection.UpdateOpts{Upsert: true})

	if code(err) != "duplicate_id" {
		t.Fatalf("upsert onto a non-matching id: %v", err)
	}
}

func TestUpsertGeneratesAnID(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	res := upd(t, c, `{"k":"x","$set":{"n":1}}`, collection.UpdateOpts{Upsert: true})

	var m map[string]any

	json.Unmarshal(res.Doc, &m)

	if id, _ := m["_id"].(string); len(id) != 36 || m["k"] != "x" {
		t.Fatalf("%s", res.Doc)
	}
}

func TestOperators(t *testing.T) {
	cases := []struct {
		name, doc, body, field string
		want                   any
	}{
		{"set dot", `{"_id":"a"}`, `{"$set":{"x.y":1}}`, "x", map[string]any{"y": 1.0}},
		{"unset", `{"_id":"a","t":1}`, `{"$unset":{"t":true}}`, "t", nil},
		{"unset empty string", `{"_id":"a","t":1}`, `{"$unset":{"t":""}}`, "t", nil},
		{"inc missing", `{"_id":"a"}`, `{"$inc":{"n":3}}`, "n", 3.0},
		{"inc negative", `{"_id":"a","n":5}`, `{"$inc":{"n":-2}}`, "n", 3.0},
		{"inc float", `{"_id":"a","n":1}`, `{"$inc":{"n":0.5}}`, "n", 1.5},
		{"inc dot", `{"_id":"a","s":{"n":1}}`, `{"$inc":{"s.n":1}}`, "s", map[string]any{"n": 2.0}},
		{"push", `{"_id":"a","t":[1]}`, `{"$push":{"t":2}}`, "t", []any{1.0, 2.0}},
		{"push missing", `{"_id":"a"}`, `{"$push":{"t":2}}`, "t", []any{2.0}},
		{"push each", `{"_id":"a","t":[1]}`, `{"$push":{"t":{"$each":[2,3]}}}`, "t", []any{1.0, 2.0, 3.0}},
		{"push object", `{"_id":"a","t":[]}`, `{"$push":{"t":{"k":1}}}`, "t", []any{map[string]any{"k": 1.0}}},
		{"pull value", `{"_id":"a","t":[1,2,1]}`, `{"$pull":{"t":1}}`, "t", []any{2.0}},
		{"pull cond", `{"_id":"a","t":[1,5,9]}`, `{"$pull":{"t":{"$gte":5}}}`, "t", []any{1.0}},
		{"pull objects", `{"_id":"a","t":[{"status":"done","n":1},{"status":"open"}]}`, `{"$pull":{"t":{"status":"done"}}}`, "t", []any{map[string]any{"status": "open"}}},
		{"min lower", `{"_id":"a","n":5}`, `{"$min":{"n":3}}`, "n", 3.0},
		{"min higher", `{"_id":"a","n":5}`, `{"$min":{"n":9}}`, "n", 5.0},
		{"max higher", `{"_id":"a","n":5}`, `{"$max":{"n":9}}`, "n", 9.0},
		{"max lower", `{"_id":"a","n":5}`, `{"$max":{"n":1}}`, "n", 5.0},
		{"max missing", `{"_id":"a"}`, `{"$max":{"n":1}}`, "n", 1.0},
		{"max strings", `{"_id":"a","s":"b"}`, `{"$max":{"s":"c"}}`, "s", "c"},
		{"min strings", `{"_id":"a","s":"b"}`, `{"$min":{"s":"a"}}`, "s", "a"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := open(t, t.TempDir(), catalog.Def{})

			c.Insert(ctx, doc(tc.doc))

			body := `{"_id":"a",` + tc.body[1:]
			upd(t, c, body, collection.UpdateOpts{})

			got := docOf(t, c, "a")[tc.field]

			gb, _ := json.Marshal(got)
			wb, _ := json.Marshal(tc.want)

			if string(gb) != string(wb) {
				t.Fatalf("got %s, want %s", gb, wb)
			}
		})
	}
}

func TestMaxLowerDoesNotWrite(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"a","n":5}`))

	res := upd(t, c, `{"_id":"a","$max":{"n":1}}`, collection.UpdateOpts{})

	if d, _ := c.Find("a"); res.Matched != 1 || res.Modified != 0 || d.V != 1 {
		t.Fatalf("%+v v=%d", res, d.V)
	}
}

func TestUpdateContractErrors(t *testing.T) {
	cases := map[string]struct{ body, code string }{
		"unknown operator":   {`{"_id":"a","$rename":{"a":"b"}}`, "unknown_operator"},
		"unknown top $not":   {`{"$not":{"a":1},"$set":{"a":1}}`, "unknown_operator"},
		"no operator":        {`{"_id":"a","n":1}`, "no_update"},
		"conflict":           {`{"_id":"a","$set":{"n":1},"$inc":{"n":1}}`, "conflicting_update"},
		"conflict parent":    {`{"_id":"a","$set":{"n":{}},"$inc":{"n.x":1}}`, "conflicting_update"},
		"dollar field":       {`{"_id":"a","$set":{"$x":1}}`, "invalid_field"},
		"dollar nested path": {`{"_id":"a","$set":{"a.$b":1}}`, "invalid_field"},
		"op not object":      {`{"_id":"a","$set":5}`, "invalid_field"},
		"inc not number":     {`{"_id":"a","$inc":{"s":"x"}}`, "invalid_field"},
		"inc on string":      {`{"_id":"a","$inc":{"s":1}}`, "invalid_field"},
		"push on non array":  {`{"_id":"a","$push":{"s":1}}`, "invalid_field"},
		"pull on non array":  {`{"_id":"a","$pull":{"s":1}}`, "invalid_field"},
		"min mismatch":       {`{"_id":"a","$min":{"s":1}}`, "invalid_field"},
		"each not array":     {`{"_id":"a","$push":{"t":{"$each":1}}}`, "invalid_field"},
		"id change":          {`{"_id":"a","$set":{"_id":"b"}}`, "immutable_id"},
		"empty filter":       {`{"$set":{"n":1}}`, "empty_filter"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := open(t, t.TempDir(), catalog.Def{})

			c.Insert(ctx, doc(`{"_id":"a","s":"str"}`))

			if _, err := c.Update(ctx, []byte(tc.body), collection.UpdateOpts{}); code(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestDeleteByFilter(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Indexes: []string{"g"}})

	c.Insert(ctx, doc(`{"_id":"a","g":1}`), doc(`{"_id":"b","g":1}`), doc(`{"_id":"c","g":2}`))

	if n, err := c.Delete(ctx, []byte(`{"_id":"zzz"}`), false); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}

	if n, err := c.Delete(ctx, []byte(`{"g":1}`), true); err != nil || n != 2 || c.Len() != 1 {
		t.Fatalf("n=%d err=%v len=%d", n, err, c.Len())
	}

	if _, err := c.Delete(ctx, []byte(`{"_id":"c","$set":{"a":1}}`), false); code(err) != "unknown_operator" {
		t.Fatalf("got %v", err)
	}
}

func TestNorFilter(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	c.Insert(ctx, doc(`{"_id":"a","s":1}`), doc(`{"_id":"b","s":2}`))

	res := upd(t, c, `{"$nor":[{"s":1}],"$set":{"x":1}}`, collection.UpdateOpts{})

	if res.Matched != 1 || docOf(t, c, "b")["x"] != 1.0 {
		t.Fatalf("%+v", res)
	}
}

func TestSetOnInsert(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	res := upd(t, c, `{"k":"x","$setOnInsert":{"n":1}}`, collection.UpdateOpts{Upsert: true})

	if res.Matched != 0 || res.Modified != 1 {
		t.Fatalf("insert: %+v", res)
	}

	var m map[string]any

	json.Unmarshal(res.Doc, &m)

	if m["k"] != "x" || m["n"] != 1.0 {
		t.Fatalf("%s", res.Doc)
	}

	res = upd(t, c, `{"k":"x","$setOnInsert":{"n":2}}`, collection.UpdateOpts{Upsert: true})

	if res.Matched != 1 || res.Modified != 0 {
		t.Fatalf("match: %+v", res)
	}

	json.Unmarshal(res.Doc, &m)

	if m["n"] != 1.0 {
		t.Fatalf("a match must not overwrite: %s", res.Doc)
	}

	_, err := c.Update(ctx, []byte(`{"k":"x","$set":{"n":2},"$setOnInsert":{"n":3}}`), collection.UpdateOpts{Upsert: true})

	if code(err) != "conflicting_update" {
		t.Fatalf("conflict: %v", err)
	}
}
