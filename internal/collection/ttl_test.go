package collection_test

import (
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

// clock is a fake time source in unix seconds.
type clock struct{ sec atomic.Int64 }

func (c *clock) now() time.Time { return time.Unix(c.sec.Load(), 0).UTC() }

func (c *clock) advance(d time.Duration) { c.sec.Add(int64(d / time.Second)) }

func newClock() *clock {
	c := &clock{}

	c.sec.Store(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).Unix())

	return c
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func openTTL(t *testing.T, def catalog.Def, clk *clock, seal bool) *collection.Collection {
	t.Helper()

	root := t.TempDir()
	def.Name, def.ID = "sessions", "id"

	cfg := collection.Config{Dir: filepath.Join(root, "wal"), FS: fs.OS{}, Def: def, Now: clk.now, SweepEvery: time.Hour}

	if seal {
		cfg.Data = root
	}

	c, err := collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	return c
}

func countOf(t *testing.T, c *collection.Collection, body string) int {
	t.Helper()

	q, err := c.ParseQuery([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	page, err := c.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}

	return len(page.Rows)
}

func TestExpiresFieldHidesAtOnce(t *testing.T) {
	for _, seal := range []bool{false, true} {
		clk := newClock()
		c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, seal)

		for _, d := range []string{
			`{"id":"past","exp":"2020-01-01T00:00:00Z"}`,
			`{"id":"past_unix","exp":1577836800}`,
			`{"id":"future","exp":"2999-01-01T00:00:00Z"}`,
			`{"id":"never"}`,
		} {
			if _, err := c.Insert(ctx, doc(d)); err != nil {
				t.Fatal(err)
			}
		}

		if seal {
			if err := c.Seal(); err != nil {
				t.Fatal(err)
			}
		}

		if n := countOf(t, c, `{}`); n != 2 {
			t.Fatalf("seal=%v: find returned %d rows, want 2", seal, n)
		}

		q, _ := c.ParseQuery([]byte(`{"count":true,"limit":1}`))
		page, _ := c.Query(ctx, q)

		if page.Total == nil || *page.Total != 2 {
			t.Fatalf("seal=%v: count = %v, want 2", seal, page.Total)
		}

		p, err := c.ParseAggregate([]byte(`{"pipeline":[{"$group":{"_id":null,"n":{"$count":{}}}}]}`))
		if err != nil {
			t.Fatal(err)
		}

		ap, err := c.Aggregate(ctx, p)
		if err != nil || len(ap.Rows) != 1 || string(ap.Rows[0]) != `{"_id":null,"n":2}` {
			t.Fatalf("seal=%v: aggregate = %s err %v", seal, ap.Rows, err)
		}
	}
}

func TestExpiryFollowsTheClock(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, false)

	exp := rfc(clk.now().Add(time.Minute))

	if _, err := c.Insert(ctx, doc(fmt.Sprintf(`{"id":"a","exp":%q}`, exp))); err != nil {
		t.Fatal(err)
	}

	if n := countOf(t, c, `{}`); n != 1 {
		t.Fatalf("before expiry: %d", n)
	}

	clk.advance(2 * time.Minute)

	if n := countOf(t, c, `{}`); n != 0 {
		t.Fatalf("after expiry: %d", n)
	}
}

func TestTTLCountsFromTheTimeField(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{Time: "at", TTL: "1h"}, clk, true)

	old := rfc(clk.now().Add(-2 * time.Hour))
	fresh := rfc(clk.now().Add(-10 * time.Minute))

	for _, d := range []string{
		fmt.Sprintf(`{"id":"old","at":%q}`, old),
		fmt.Sprintf(`{"id":"fresh","at":%q}`, fresh),
		`{"id":"no_time"}`,
	} {
		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	if n := countOf(t, c, `{}`); n != 2 {
		t.Fatalf("got %d rows, want fresh and no_time", n)
	}

	clk.advance(51 * time.Minute)

	if n := countOf(t, c, `{}`); n != 1 {
		t.Fatalf("after the clock moved: %d rows, want no_time", n)
	}
}

func TestSweepWritesExpiredTombstones(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, true)

	for i := range 5 {
		if _, err := c.Insert(ctx, doc(fmt.Sprintf(`{"id":"e%d","exp":"2020-01-01T00:00:00Z"}`, i))); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := c.Insert(ctx, doc(`{"id":"keep","exp":"2999-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	if c.Len() != 6 {
		t.Fatalf("len before sweep = %d", c.Len())
	}

	n, err := c.Sweep()
	if err != nil || n != 5 {
		t.Fatalf("sweep = %d, %v", n, err)
	}

	if c.Len() != 1 {
		t.Fatalf("len after sweep = %d, want 1", c.Len())
	}

	if n, _ := c.Sweep(); n != 0 {
		t.Fatalf("a second sweep expired %d", n)
	}

	s, err := c.Feed().Open("")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	expired := 0

	for range 11 {
		ev := nextEv(t, s)

		if ev.Op == "del" {
			if !ev.Expired {
				t.Fatalf("del without _expired: %+v", ev)
			}

			expired++
		}
	}

	if expired != 5 {
		t.Fatalf("expired events = %d", expired)
	}
}

func TestSweepBatchesAreSmall(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, false)

	for i := range 250 {
		if _, err := c.Insert(ctx, doc(fmt.Sprintf(`{"id":"e%03d","exp":"2020-01-01T00:00:00Z"}`, i))); err != nil {
			t.Fatal(err)
		}
	}

	n, err := c.Sweep()
	if err != nil || n != 250 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
}

func TestUpdateThatMovesExpiryForwardKeepsTheDoc(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, false)

	if _, err := c.Insert(ctx, doc(`{"id":"s1","exp":"2020-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	ch := collection.Change{Set: map[string]any{"exp": "2999-01-01T00:00:00Z"}}

	if _, err := c.Update(ctx, "s1", ch, 0); err != nil {
		t.Fatal(err)
	}

	if n, err := c.Sweep(); err != nil || n != 0 {
		t.Fatalf("sweep = %d, %v", n, err)
	}

	if n := countOf(t, c, `{}`); n != 1 {
		t.Fatalf("doc lost: %d rows", n)
	}
}

func TestSweeperRunsOnItsTick(t *testing.T) {
	clk := newClock()
	root := t.TempDir()
	def := catalog.Def{Name: "sessions", ID: "id", ExpiresField: "exp"}

	c, err := collection.Open(collection.Config{
		Dir: filepath.Join(root, "wal"), FS: fs.OS{}, Def: def, Now: clk.now, SweepEvery: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { closeOnce(c) })

	if _, err := c.Insert(ctx, doc(`{"id":"a","exp":"2020-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)

	for c.Len() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the sweeper did not remove the expired doc")
		}

		time.Sleep(10 * time.Millisecond)
	}
}

func TestSweepSurvivesRestart(t *testing.T) {
	clk := newClock()
	root := t.TempDir()
	def := catalog.Def{Name: "sessions", ID: "id", ExpiresField: "exp"}
	cfg := collection.Config{Dir: filepath.Join(root, "wal"), Data: root, FS: fs.OS{}, Def: def, Now: clk.now, SweepEvery: time.Hour}

	c, err := collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		if _, err := c.Insert(ctx, doc(fmt.Sprintf(`{"id":"e%d","exp":"2020-01-01T00:00:00Z"}`, i))); err != nil {
			t.Fatal(err)
		}
	}

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Sweep(); err != nil {
		t.Fatal(err)
	}

	c.Close()

	c, err = collection.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	if c.Len() != 0 {
		t.Fatalf("expired docs came back after a restart: %d", c.Len())
	}
}

func TestRetentionHidesOldRows(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{Time: "at", Retention: "7d"}, clk, true)

	for _, d := range []string{
		fmt.Sprintf(`{"id":"old","at":%q}`, rfc(clk.now().Add(-8*24*time.Hour))),
		fmt.Sprintf(`{"id":"new","at":%q}`, rfc(clk.now().Add(-24*time.Hour))),
	} {
		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	if n := countOf(t, c, `{}`); n != 1 {
		t.Fatalf("retention: %d rows, want 1", n)
	}

	p, _ := c.ParseAggregate([]byte(`{"pipeline":[{"$group":{"_id":null,"n":{"$count":{}}}}]}`))

	ap, err := c.Aggregate(ctx, p)
	if err != nil || string(ap.Rows[0]) != `{"_id":null,"n":1}` {
		t.Fatalf("aggregate: %s %v", ap.Rows, err)
	}
}

func TestDirectModeOnlyHides(t *testing.T) {
	d := collection.NewDirect(collection.DirectConfig{Def: catalog.Def{Name: "x", ID: "id", ExpiresField: "exp"}})

	defer d.Close()

	for i, line := range []string{
		`{"id":"gone","exp":"2020-01-01T00:00:00Z"}`,
		`{"id":"here","exp":"2999-01-01T00:00:00Z"}`,
	} {
		if err := d.Apply("a.ndjson", int64(i*100), []byte(line)); err != nil {
			t.Fatal(err)
		}
	}

	q, err := d.ParseQuery([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	page, err := d.Query(ctx, q)
	if err != nil || len(page.Rows) != 1 {
		t.Fatalf("rows = %d err %v", len(page.Rows), err)
	}

	if d.Len() != 2 {
		t.Fatalf("direct mode must keep the doc, only hide it: len %d", d.Len())
	}
}

func TestFindByIDHidesExpiredDocs(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{ExpiresField: "exp"}, clk, false)

	for _, d := range []string{`{"id":"a","exp":"2020-01-01T00:00:00Z"}`, `{"id":"b"}`} {
		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok := c.Find("a"); ok {
		t.Fatal("an expired doc must not be found by id")
	}

	if _, ok := c.Find("b"); !ok {
		t.Fatal("a doc without expiry must be found")
	}

	if n := countOf(t, c, `{"where":{"id":"a"}}`); n != 0 {
		t.Fatalf("find by id plan returned %d rows", n)
	}
}
