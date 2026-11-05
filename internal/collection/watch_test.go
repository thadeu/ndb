package collection_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/fs"
)

func nextEv(t *testing.T, s feed.Stream) feed.Event {
	t.Helper()

	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ev, err := s.Next(c)
	if err != nil {
		t.Fatalf("next: %v", err)
	}

	return ev
}

func addPosts(t *testing.T, c *collection.Collection, from, to int) {
	t.Helper()

	for i := from; i <= to; i++ {
		if _, err := c.Insert(ctx, doc(fmt.Sprintf(`{"post_id":"p%d","n":%d}`, i, i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWatchReplaysHistoryThenFollowsLive(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	addPosts(t, c, 1, 3)

	s, err := c.Feed().Open("")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	addPosts(t, c, 4, 5)

	for i := 1; i <= 5; i++ {
		ev := nextEv(t, s)

		if ev.ID != fmt.Sprintf("p%d", i) || ev.Op != "put" || ev.V != 1 || len(ev.Doc) == 0 {
			t.Fatalf("event %d = %+v", i, ev)
		}
	}
}

func TestWatchDeleteHasNoDoc(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	addPosts(t, c, 1, 1)

	if err := c.Delete(ctx, "p1", 0); err != nil {
		t.Fatal(err)
	}

	s, _ := c.Feed().Open("")
	defer s.Close()

	nextEv(t, s)

	ev := nextEv(t, s)

	if ev.Op != "del" || ev.ID != "p1" || len(ev.Doc) != 0 || ev.V != 2 {
		t.Fatalf("del = %+v", ev)
	}
}

func TestWatchResumesAfterPositionAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: catalog.Def{Name: "posts", ID: "post_id"}})
	if err != nil {
		t.Fatal(err)
	}

	addPosts(t, c, 1, 4)

	s, _ := c.Feed().Open("")

	nextEv(t, s)

	pos := nextEv(t, s).Pos

	s.Close()
	c.Close()

	c = open(t, dir, catalog.Def{})

	addPosts(t, c, 5, 5)

	s, err = c.Feed().Open(pos)
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	for _, id := range []string{"p3", "p4", "p5"} {
		if ev := nextEv(t, s); ev.ID != id {
			t.Fatalf("got %s, want %s", ev.ID, id)
		}
	}
}

func TestWatchResumesInsideABatchTransaction(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	if _, err := c.Insert(ctx, doc(`{"post_id":"a"}`), doc(`{"post_id":"b"}`), doc(`{"post_id":"c"}`)); err != nil {
		t.Fatal(err)
	}

	s, _ := c.Feed().Open("")

	nextEv(t, s)

	pos := nextEv(t, s).Pos

	s.Close()

	s, _ = c.Feed().Open(pos)
	defer s.Close()

	if ev := nextEv(t, s); ev.ID != "c" {
		t.Fatalf("got %s, want c", ev.ID)
	}
}

func TestWatchNowSkipsHistory(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	addPosts(t, c, 1, 3)

	now := c.Feed().Now()

	addPosts(t, c, 4, 4)

	s, err := c.Feed().Open(now)
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	if ev := nextEv(t, s); ev.ID != "p4" {
		t.Fatalf("got %s, want p4", ev.ID)
	}
}

func TestWatchReadsAcrossSegments(t *testing.T) {
	dir := t.TempDir()

	c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: catalog.Def{Name: "posts", ID: "post_id"}, WALSegmentSize: 300})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	addPosts(t, c, 1, 20)

	s, _ := c.Feed().Open("")
	defer s.Close()

	for i := 1; i <= 20; i++ {
		if ev := nextEv(t, s); ev.ID != fmt.Sprintf("p%d", i) {
			t.Fatalf("got %s, want p%d", ev.ID, i)
		}
	}
}

func TestAckMergeIsMonotonic(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	addPosts(t, c, 1, 2)

	s, _ := c.Feed().Open("")
	defer s.Close()

	first, second := nextEv(t, s).Pos, nextEv(t, s).Pos

	got, err := c.Feed().Merge(second, first)
	if err != nil || got != second {
		t.Fatalf("older ack moved the position back: %q err=%v", got, err)
	}

	got, _ = c.Feed().Merge("", first)

	if got != first {
		t.Fatalf("first ack = %q", got)
	}

	if _, err := c.Feed().Merge("", "garbage"); err == nil {
		t.Fatal("garbage token must fail")
	}
}

func TestSlowWatcherNeverBlocksWritersAndLags(t *testing.T) {
	c, err := collection.Open(collection.Config{Dir: t.TempDir(), FS: fs.OS{}, Def: catalog.Def{Name: "posts", ID: "post_id"}, WatchBuffer: 4})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	s, _ := c.Feed().Open("")
	defer s.Close()

	done := make(chan struct{})

	go func() {
		defer close(done)

		addPosts(t, c, 1, 50)
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("writers blocked behind a slow watcher")
	}

	got := 0

	for {
		cx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err := s.Next(cx)

		cancel()

		if err != nil {
			if err != feed.ErrLagged {
				t.Fatalf("err = %v, want ErrLagged", err)
			}

			break
		}

		got++
	}

	if got != 4 {
		t.Fatalf("delivered %d events before the lag, want 4", got)
	}
}

func TestWatchLaggedStreamResumesFromLastEvent(t *testing.T) {
	c, err := collection.Open(collection.Config{Dir: t.TempDir(), FS: fs.OS{}, Def: catalog.Def{Name: "posts", ID: "post_id"}, WatchBuffer: 2})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	s, _ := c.Feed().Open("")

	addPosts(t, c, 1, 10)

	var pos string

	for range 2 {
		pos = nextEv(t, s).Pos
	}

	s.Close()

	r, _ := c.Feed().Open(pos)
	defer r.Close()

	for i := 3; i <= 10; i++ {
		if ev := nextEv(t, r); ev.ID != fmt.Sprintf("p%d", i) {
			t.Fatalf("got %s, want p%d", ev.ID, i)
		}
	}
}

func TestRelaxedWatchDeliversOnlyAfterFsync(t *testing.T) {
	dir := t.TempDir()

	c, err := collection.Open(collection.Config{
		Dir:        dir,
		Data:       dir,
		FS:         fs.OS{},
		FlushEvery: time.Hour,
		Def:        catalog.Def{Name: "posts", ID: "post_id", Durability: catalog.Relaxed},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { c.Close() })

	s, err := c.Feed().Open("")
	if err != nil {
		t.Fatal(err)
	}

	defer s.Close()

	addPosts(t, c, 1, 1)

	if _, ok := c.Find("p1"); !ok {
		t.Fatal("relaxed write not visible to reads")
	}

	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()

	if ev, err := s.Next(short); err == nil {
		t.Fatalf("event delivered before fsync: %+v", ev)
	}

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	if ev := nextEv(t, s); ev.ID != "p1" {
		t.Fatalf("after fsync got %+v", ev)
	}
}
