package collection_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/query"
)

func rq(t *testing.T, c *collection.Collection, body string, timeField ...string) collection.Page {
	t.Helper()

	opts := query.Options{}

	if len(timeField) > 0 {
		opts.TimeField = timeField[0]
	}

	q, err := query.Parse([]byte(body), opts)
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}

	p, err := c.Query(ctx, q)
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}

	return p
}

func rqErr(t *testing.T, c *collection.Collection, body string) error {
	t.Helper()

	q, err := query.Parse([]byte(body), query.Options{})
	if err != nil {
		return err
	}

	_, err = c.Query(ctx, q)

	return err
}

func ids(t *testing.T, p collection.Page) []string {
	t.Helper()

	out := make([]string, len(p.Rows))

	for i, r := range p.Rows {
		out[i], _ = field(t, r, "post_id").(string)
	}

	return out
}

func seed(t *testing.T, c *collection.Collection, docs ...string) {
	t.Helper()

	for _, d := range docs {
		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}
}

func seedN(t *testing.T, c *collection.Collection, n int, format func(i int) string) {
	t.Helper()

	docs := make([][]byte, n)

	for i := range docs {
		docs[i] = []byte(format(i))
	}

	if _, err := c.Insert(ctx, docs...); err != nil {
		t.Fatal(err)
	}
}

func eq(t *testing.T, got, want []string) {
	t.Helper()

	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestQueryWhereAndProjection(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c,
		`{"post_id":"p1","status":"draft","n":1,"meta":{"a":1,"b":2}}`,
		`{"post_id":"p2","status":"published","n":2}`,
		`{"post_id":"p3","status":"published","n":3}`,
	)

	eq(t, ids(t, rq(t, c, `{"where":{"status":"published","n":{"$gt":2}}}`)), []string{"p3"})
	eq(t, ids(t, rq(t, c, `{}`)), []string{"p1", "p2", "p3"})

	p := rq(t, c, `{"where":{"post_id":"p1"},"fields":["status","meta.b"]}`)

	if string(p.Rows[0]) != `{"meta":{"b":2},"status":"draft"}` {
		t.Fatalf("projection = %s", p.Rows[0])
	}
}

func TestQueryNoSortIsInsertionOrder(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c, `{"post_id":"z"}`, `{"post_id":"a"}`, `{"post_id":"m"}`)
	c.Update(ctx, "z", collection.Change{Set: map[string]any{"x": 1}}, 0)

	eq(t, ids(t, rq(t, c, `{}`)), []string{"z", "a", "m"})

	c.Delete(ctx, "a", 0)
	seed(t, c, `{"post_id":"a"}`)

	eq(t, ids(t, rq(t, c, `{}`)), []string{"z", "m", "a"})
}

func TestQueryCountAndSkipLimit(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	for i := range 10 {
		seed(t, c, fmt.Sprintf(`{"post_id":"p%02d","n":%d}`, i, i))
	}

	p := rq(t, c, `{"where":{"n":{"$gte":3}},"sort":{"n":1},"skip":2,"limit":3,"count":true}`)

	eq(t, ids(t, p), []string{"p05", "p06", "p07"})

	if p.Total == nil || *p.Total != 7 {
		t.Fatalf("total = %v", p.Total)
	}

	if p.Next == "" {
		t.Fatal("more rows exist, want next")
	}

	if rq(t, c, `{"limit":3}`).Total != nil {
		t.Fatal("total only with count")
	}

	last := rq(t, c, `{"sort":{"n":1},"skip":8,"limit":5}`)

	eq(t, ids(t, last), []string{"p08", "p09"})

	if last.Next != "" {
		t.Fatal("no more rows, no next")
	}
}

func TestQueryTiesOnSortField(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c, `{"post_id":"c","g":1}`, `{"post_id":"a","g":1}`, `{"post_id":"b","g":1}`, `{"post_id":"d","g":0}`)

	eq(t, ids(t, rq(t, c, `{"sort":{"g":1}}`)), []string{"d", "a", "b", "c"})
	eq(t, ids(t, rq(t, c, `{"sort":{"g":-1}}`)), []string{"c", "b", "a", "d"})
}

func TestQuerySortMissingAndTypes(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c, `{"post_id":"s","v":"x"}`, `{"post_id":"n","v":1}`, `{"post_id":"m"}`, `{"post_id":"z","v":null}`, `{"post_id":"b","v":true}`)

	eq(t, ids(t, rq(t, c, `{"sort":{"v":1}}`)), []string{"m", "z", "n", "s", "b"})
}

func pageAll(t *testing.T, c *collection.Collection, body string, between func(page int)) [][]string {
	t.Helper()

	var pages [][]string

	after := ""

	for i := 0; i < 50; i++ {
		b := body

		if after != "" {
			b = strings.Replace(body, "{", fmt.Sprintf(`{"after":%q,`, after), 1)
		}

		p := rq(t, c, b)
		pages = append(pages, ids(t, p))

		if between != nil {
			between(i)
		}

		if p.Next == "" {
			return pages
		}

		after = p.Next
	}

	t.Fatal("too many pages")

	return nil
}

func TestCursorPaginationInsertsBetweenPages(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	for i := 0; i < 9; i++ {
		seed(t, c, fmt.Sprintf(`{"post_id":"p%d","n":%d}`, i, i*10))
	}

	pages := pageAll(t, c, `{"sort":{"n":1},"limit":3}`, func(page int) {
		if page == 0 {
			seed(t, c, `{"post_id":"early","n":5}`, `{"post_id":"late","n":95}`)
		}
	})

	var all []string

	for _, p := range pages {
		all = append(all, p...)
	}

	eq(t, all, []string{"p0", "p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "late"})
}

func TestCursorPaginationDescTiesNoSkipNoRepeat(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	for i := 0; i < 12; i++ {
		seed(t, c, fmt.Sprintf(`{"post_id":"p%02d","g":%d}`, i, i%3))
	}

	var all []string

	for _, p := range pageAll(t, c, `{"sort":{"g":-1},"limit":5}`, nil) {
		all = append(all, p...)
	}

	want := ids(t, rq(t, c, `{"sort":{"g":-1}}`))

	eq(t, all, want)
}

func TestCursorNoSortUsesInsertionPosition(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c, `{"post_id":"c"}`, `{"post_id":"a"}`, `{"post_id":"b"}`, `{"post_id":"d"}`)

	pages := pageAll(t, c, `{"limit":2}`, func(page int) {
		if page == 0 {
			c.Update(ctx, "c", collection.Change{Set: map[string]any{"x": 1}}, 0)
			seed(t, c, `{"post_id":"e"}`)
		}
	})

	eq(t, slices.Concat(pages...), []string{"c", "a", "b", "d", "e"})
}

func TestCursorMismatchAndInvalid(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c, `{"post_id":"a","n":1}`, `{"post_id":"b","n":2}`)

	next := rq(t, c, `{"where":{"n":{"$gt":0}},"sort":{"n":1},"limit":1}`).Next

	for name, body := range map[string]string{
		"different where": fmt.Sprintf(`{"where":{"n":{"$gt":1}},"sort":{"n":1},"after":%q}`, next),
		"different sort":  fmt.Sprintf(`{"where":{"n":{"$gt":0}},"sort":{"n":-1},"after":%q}`, next),
	} {
		if got := code(rqErr(t, c, body)); got != "cursor_mismatch" {
			t.Fatalf("%s: got %s", name, got)
		}
	}

	if got := code(rqErr(t, c, `{"after":"!!"}`)); got != "cursor_invalid" {
		t.Fatalf("got %s", got)
	}

	eq(t, ids(t, rq(t, c, fmt.Sprintf(`{"where":{"n":{"$gt":0}},"sort":{"n":1},"after":%q,"limit":10}`, next))), []string{"b"})
}

func TestSnapshotCursorIgnoresLaterWrites(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	for i := 0; i < 6; i++ {
		seed(t, c, fmt.Sprintf(`{"post_id":"p%d","n":%d}`, i, i))
	}

	pages := pageAll(t, c, `{"sort":{"n":1},"limit":2,"snapshot":true,"count":true}`, func(page int) {
		if page == 0 {
			seed(t, c, `{"post_id":"new","n":2}`)
			c.Delete(ctx, "p3", 0)
			c.Update(ctx, "p4", collection.Change{Set: map[string]any{"n": -1}}, 0)
		}
	})

	eq(t, slices.Concat(pages...), []string{"p0", "p1", "p2", "p3", "p4", "p5"})

	fresh := ids(t, rq(t, c, `{"sort":{"n":1}}`))

	eq(t, fresh, []string{"p4", "p0", "p1", "new", "p2", "p5"})
}

func TestSnapshotCursorExpires(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c, `{"post_id":"a"}`, `{"post_id":"b"}`, `{"post_id":"c"}`)
	c.SetSnapshotTTL(40 * time.Millisecond)

	next := rq(t, c, `{"limit":1,"snapshot":true}`).Next

	time.Sleep(120 * time.Millisecond)

	if got := code(rqErr(t, c, fmt.Sprintf(`{"limit":1,"after":%q}`, next))); got != "cursor_expired" {
		t.Fatalf("got %s", got)
	}
}

func TestQueryDoesNotSeeWritesAfterItStarts(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seedN(t, c, 2000, func(i int) string { return fmt.Sprintf(`{"post_id":"p%04d","n":%d}`, i, i) })

	var (
		wg   sync.WaitGroup
		stop = make(chan struct{})
	)

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}

			c.Insert(ctx, doc(fmt.Sprintf(`{"post_id":"w%d","n":-1}`, i)))
			c.Update(ctx, "p0000", collection.Change{Set: map[string]any{"n": i}}, 0)
		}
	}()

	for range 30 {
		p := rq(t, c, `{"sort":{"n":1}}`)

		seen := map[string]bool{}

		for _, id := range ids(t, p) {
			if seen[id] {
				t.Fatalf("%s twice in one query", id)
			}

			seen[id] = true
		}

		for i := 0; i < 2000; i++ {
			if !seen[fmt.Sprintf("p%04d", i)] {
				t.Fatalf("p%04d lost: the query saw a half-applied state", i)
			}
		}
	}

	close(stop)
	wg.Wait()
}

func TestQueryTimeField(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Time: "at"})

	seed(t, c,
		`{"post_id":"a","at":"2026-11-04T10:00:00-03:00"}`,
		`{"post_id":"b","at":"2026-11-04T12:30:00Z"}`,
		`{"post_id":"c","at":"2026-11-04T11:00:00Z"}`,
	)

	eq(t, ids(t, rq(t, c, `{"where":{"at":{"$lt":"2026-11-04T12:00:00Z"}},"sort":{"at":1}}`, "at")), []string{"c"})
	eq(t, ids(t, rq(t, c, `{"sort":{"at":1}}`, "at")), []string{"c", "b", "a"})
}

func TestIndexPlanAndInLookups(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Indexes: []string{"post_id_ref", "tags", "n"}})

	seedN(t, c, 300, func(i int) string {
		return fmt.Sprintf(`{"post_id":"c%d","post_id_ref":"p%d","n":%d,"tags":["t%d","all"]}`, i, i%100, i, i%7)
	})

	waitReady(t, c)

	p := rq(t, c, `{"where":{"post_id_ref":{"$in":["p1","p2","nope"]}},"sort":{"post_id":1}}`)

	if len(p.Rows) != 6 || p.Plan != "index:post_id_ref" || p.Examined != 6 {
		t.Fatalf("rows %d plan %s examined %d", len(p.Rows), p.Plan, p.Examined)
	}

	if p := rq(t, c, `{"where":{"post_id":{"$in":["c1","c2"]}}}`); p.Plan != "id" || p.Examined != 2 {
		t.Fatalf("plan %s examined %d", p.Plan, p.Examined)
	}

	if p := rq(t, c, `{"where":{"n":{"$gte":10,"$lt":15}}}`); p.Plan != "index:n" || len(p.Rows) != 5 || p.Examined != 5 {
		t.Fatalf("plan %s rows %d examined %d", p.Plan, len(p.Rows), p.Examined)
	}

	if p := rq(t, c, `{"where":{"tags":"t3","n":{"$lt":50}}}`); p.Plan != "index:tags" || len(p.Rows) != 7 {
		t.Fatalf("plan %s rows %d", p.Plan, len(p.Rows))
	}

	if p := rq(t, c, `{"where":{"n":{"$ne":3}}}`); p.Plan != "scan" || p.Examined != 300 {
		t.Fatalf("plan %s examined %d", p.Plan, p.Examined)
	}
}

func TestIndexFollowsUpdatesDeletesAndArrays(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{Indexes: []string{"tags", "status"}})

	seed(t, c, `{"post_id":"a","tags":["x","y"],"status":"new"}`, `{"post_id":"b","tags":["y"],"status":"new"}`, `{"post_id":"c","status":"old"}`)
	waitReady(t, c)

	c.Update(ctx, "a", collection.Change{Set: map[string]any{"tags": []any{"z"}}}, 0)
	c.Update(ctx, "c", collection.Change{Set: map[string]any{"status": "new"}}, 0)
	c.Delete(ctx, "b", 0)

	eq(t, ids(t, rq(t, c, `{"where":{"tags":"y"}}`)), nil)
	eq(t, ids(t, rq(t, c, `{"where":{"tags":"z"}}`)), []string{"a"})
	eq(t, ids(t, rq(t, c, `{"where":{"status":"new"}}`)), []string{"a", "c"})
	eq(t, ids(t, rq(t, c, `{"where":{"status":"old"}}`)), nil)
}

func waitReady(t *testing.T, c *collection.Collection) {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		ready := true

		for _, ix := range c.Indexes() {
			ready = ready && ix.State == "ready"
		}

		if ready {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("indexes never became ready")
}

func TestIndexBuildIsCorrectWhileBuilding(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seedN(t, c, 3000, func(i int) string { return fmt.Sprintf(`{"post_id":"p%04d","g":%d}`, i, i%10) })

	gate := make(chan struct{})
	reached := make(chan struct{}, 1)

	collection.SetBuildGate(c, func() {
		select {
		case reached <- struct{}{}:
		default:
		}

		<-gate
	})

	if err := c.EnsureIndex("g"); err != nil {
		t.Fatal(err)
	}

	<-reached

	infos := c.Indexes()

	if len(infos) != 1 || infos[0].State != "building" || infos[0].Total != 3000 || infos[0].Done != 0 {
		t.Fatalf("%+v", infos)
	}

	p := rq(t, c, `{"where":{"g":3}}`)

	if p.Plan != "scan" || len(p.Rows) != 300 {
		t.Fatalf("while building: plan %s rows %d", p.Plan, len(p.Rows))
	}

	c.Update(ctx, "p0003", collection.Change{Set: map[string]any{"g": 4}}, 0)
	c.Delete(ctx, "p0013", 0)
	seed(t, c, `{"post_id":"late","g":3}`)

	close(gate)
	waitReady(t, c)

	if p := rq(t, c, `{"where":{"g":3}}`); p.Plan != "index:g" || len(p.Rows) != 299 {
		t.Fatalf("after build: plan %s rows %d", p.Plan, len(p.Rows))
	}

	if p := rq(t, c, `{"where":{"g":4}}`); len(p.Rows) != 301 {
		t.Fatalf("moved doc: %d", len(p.Rows))
	}

	if infos := c.Indexes(); infos[0].Done != infos[0].Total {
		t.Fatalf("%+v", infos)
	}
}

func TestIndexBuildRestart(t *testing.T) {
	dir := t.TempDir()
	def := catalog.Def{Name: "posts", ID: "post_id", Indexes: []string{"g"}}

	c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: def})
	if err != nil {
		t.Fatal(err)
	}

	seedN(t, c, 2000, func(i int) string { return fmt.Sprintf(`{"post_id":"p%04d","g":%d}`, i, i%4) })

	c.Close()

	c2 := open(t, dir, def)

	if got := len(rq(t, c2, `{"where":{"g":1}}`).Rows); got != 500 {
		t.Fatalf("right after restart (building or ready): %d", got)
	}

	waitReady(t, c2)

	if p := rq(t, c2, `{"where":{"g":1}}`); p.Plan != "index:g" || len(p.Rows) != 500 {
		t.Fatalf("plan %s rows %d", p.Plan, len(p.Rows))
	}
}

func TestQueryContextCanceled(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	seed(t, c, `{"post_id":"a"}`)

	q, _ := query.Parse(nil, query.Options{})
	cctx, cancel := contextWithCancel()

	cancel()

	if _, err := c.Query(cctx, q); err == nil {
		t.Fatal("want context error")
	}
}

func contextWithCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
