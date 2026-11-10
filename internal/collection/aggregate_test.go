package collection_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

var aggDef = catalog.Def{Name: "calls", Time: "ts", Indexes: []string{"status"}}

// aggDoc makes document i, with null, missing, mixed types, arrays and
// time zones in the offsets, so the column engine falls back to _raw often.
func aggDoc(i int) string {
	status := []string{`"a"`, `"b"`, `"c"`, `null`, `7`, `"a"`}[i%6]
	dur := []string{fmt.Sprint(i % 50), fmt.Sprintf("%d.5", i%9), `null`, `"slow"`, fmt.Sprint(i % 13)}[i%5]
	ts := fmt.Sprintf(`"2026-11-%02dT%02d:%02d:00Z"`, 1+i%6, i%24, (i*7)%60)

	if i%11 == 0 {
		ts = fmt.Sprintf(`"2026-11-%02dT%02d:%02d:00-03:00"`, 1+i%6, i%24, (i*7)%60)
	}

	doc := fmt.Sprintf(`{"_id":"d%04d","status":%s,"dur":%s,"ts":%s,"n":%d`, i, status, dur, ts, i)

	if i%4 == 0 {
		doc += fmt.Sprintf(`,"tags":["t%d","u"]`, i%3)
	}

	if i%9 != 0 {
		doc += fmt.Sprintf(`,"meta":{"city":"c%d"}`, i%4)
	}

	return doc + "}"
}

// fillAgg writes the same history into c; seal puts it in segments in
// between (the history has updates and deletes of sealed rows).
func fillAgg(t *testing.T, c *collection.Collection, seal bool) {
	t.Helper()

	put := func(from, to int) {
		for i := from; i < to; i++ {
			if _, err := c.Insert(ctx, []byte(aggDoc(i))); err != nil {
				t.Fatal(err)
			}
		}
	}

	sealNow := func() {
		if seal {
			if err := c.Seal(); err != nil {
				t.Fatal(err)
			}
		}
	}

	put(0, 250)
	sealNow()
	put(250, 500)
	sealNow()

	for i := 0; i < 500; i += 17 {
		set := map[string]any{"status": "z", "dur": 1000 + i}

		if _, err := c.UpdateByID(ctx, fmt.Sprintf("d%04d", i), collection.Change{Set: set}, 0); err != nil {
			t.Fatal(err)
		}
	}

	for i := 3; i < 500; i += 29 {
		if err := c.DeleteByID(ctx, fmt.Sprintf("d%04d", i)); err != nil {
			t.Fatal(err)
		}
	}

	put(500, 650)
}

func aggRows(t *testing.T, c *collection.Collection, body string) []string {
	t.Helper()

	p, err := c.ParseAggregate([]byte(body))
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}

	page, err := c.Aggregate(ctx, p)
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}

	out := make([]string, len(page.Rows))

	for i, r := range page.Rows {
		out[i] = string(r)
	}

	return out
}

var aggPipelines = []string{
	`{"pipeline":[{"$group":{"_id":"$status","n":{"$count":{}},"s":{"$sum":"$dur"},"a":{"$avg":"$dur"},"lo":{"$min":"$dur"},"hi":{"$max":"$dur"}}}]}`,
	`{"pipeline":[{"$match":{"status":{"$in":["a","b"]}}},{"$group":{"_id":{"s":"$status","h":{"$dateTrunc":{"date":"$ts","unit":"hour"}}},"n":{"$count":{}}}}]}`,
	`{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"day","timezone":"America/Sao_Paulo"}},"n":{"$sum":1},"first":{"$first":"$_id"},"last":{"$last":"$_id"}}}]}`,
	`{"pipeline":[{"$sort":{"n":-1}},{"$group":{"_id":"$meta.city","f":{"$first":"$_id"},"l":{"$last":"$_id"},"top":{"$max":"$n"}}}]}`,
	`{"pipeline":[{"$match":{"n":{"$gte":100,"$lt":400}}},{"$group":{"_id":"$tags","c":{"$count":{}}}},{"$sort":{"c":-1}},{"$limit":3}]}`,
	`{"pipeline":[{"$group":{"_id":null,"c":{"$count":{}},"lo":{"$min":"$ts"},"hi":{"$max":"$ts"},"f":{"$first":"$ts"}}}]}`,
	`{"pipeline":[{"$group":{"_id":"$status","c":{"$count":{}}}},{"$match":{"c":{"$gt":5}}},{"$project":{"c":1,"_id":0}},{"$skip":1}]}`,
	`{"pipeline":[{"$group":{"_id":{"w":{"$dateTrunc":{"date":"$ts","unit":"week"}},"m":{"$dateTrunc":{"date":"$ts","unit":"month"}}},"c":{"$count":{}}}}]}`,
	`{"pipeline":[{"$group":{"_id":"$status","cities":{"$distinct":"$meta.city"},"durs":{"$distinct":"$dur"},"set":{"$addToSet":"$meta.city"}}}]}`,
	`{"pipeline":[{"$group":{"_id":"$meta.city","tags":{"$addToSet":"$tags"},"n":{"$distinct":"$n"}}},{"$having":{"n":{"$gt":100}}},{"$sort":{"n":-1}},{"$limit":2}]}`,
}

func TestAggregateEnginesAgree(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	mem := openAgg(t, false)
	seg := openAgg(t, true)

	for _, body := range aggPipelines {
		want := aggRows(t, mem, body)
		got := aggRows(t, seg, body)

		if len(want) == 0 {
			t.Fatalf("%s: no rows", body)
		}

		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s\n segments:\n  %s\n memtable:\n  %s", body, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}

func openAgg(t *testing.T, seal bool) *collection.Collection {
	t.Helper()

	cfg := collection.Config{Dir: t.TempDir(), FS: fs.OS{}, Def: aggDef}

	if seal {
		root := t.TempDir()
		cfg.Dir, cfg.Data = filepath.Join(root, "wal"), root
	}

	c, err := collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	fillAgg(t, c, seal)

	return c
}

func TestAggregateColumnPathReadsNoMoreThanItNeeds(t *testing.T) {
	c := openAgg(t, true)

	p, err := c.ParseAggregate([]byte(`{"pipeline":[{"$match":{"n":{"$gte":100}}},{"$group":{"_id":"$n","c":{"$count":{}}}}],"explain":true}`))
	if err != nil {
		t.Fatal(err)
	}

	page, err := c.Aggregate(ctx, p)
	if err != nil {
		t.Fatal(err)
	}

	ex := page.Explain

	if ex == nil || ex.SegmentsRead == 0 || ex.RowsRead == 0 || ex.RowsMatched == 0 {
		t.Fatalf("explain: %+v", ex)
	}

	if mem := collection.MemDocs(c); ex.RawFetched != mem {
		t.Fatalf("n is a plain int column: only the %d memtable documents are read, fetched %d", mem, ex.RawFetched)
	}
}

func TestAggregateGroupLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	for _, seal := range []bool{false, true} {
		c := openAgg(t, seal)

		c.SetGroupLimit(4096)

		p, err := c.ParseAggregate([]byte(`{"pipeline":[{"$group":{"_id":"$_id","c":{"$count":{}}}}]}`))
		if err != nil {
			t.Fatal(err)
		}

		_, err = c.Aggregate(ctx, p)

		var ce interface{ Code() string }

		if err == nil || !errors.As(err, &ce) || ce.Code() != "group_limit_exceeded" {
			t.Fatalf("seal=%v: want group_limit_exceeded, got %v", seal, err)
		}
	}
}

func TestAggregateCursorWalksAllRows(t *testing.T) {
	c := openAgg(t, true)
	pipe := `[{"$group":{"_id":"$n","c":{"$count":{}}}}]`
	want := aggRows(t, c, `{"pipeline":`+pipe+`}`)

	var got []string

	body := `{"pipeline":` + pipe + `,"limit":100}`

	for body != "" {
		p, err := c.ParseAggregate([]byte(body))
		if err != nil {
			t.Fatal(err)
		}

		page, err := c.Aggregate(ctx, p)
		if err != nil {
			t.Fatal(err)
		}

		for _, r := range page.Rows {
			got = append(got, string(r))
		}

		body = ""

		if page.Next != "" {
			body = `{"pipeline":` + pipe + `,"limit":100,"after":"` + page.Next + `"}`
		}
	}

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("pages differ from the full result: %d vs %d rows", len(got), len(want))
	}
}
