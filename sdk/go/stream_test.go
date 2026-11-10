package ndb

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type SOrder struct {
	Order int     `ndb:"order"`
	Total float64 `ndb:"total"`
}

func testStream(t *testing.T) (*Stream[SOrder], context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	db := openTest(t, daemon.unix)

	s, err := StreamOf[SOrder](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	return s, ctx
}

func appendOrders(t *testing.T, ctx context.Context, s *Stream[SOrder], from, to int) []string {
	t.Helper()

	docs := []SOrder{}

	for i := from; i <= to; i++ {
		docs = append(docs, SOrder{Order: i, Total: float64(i) * 10})
	}

	ids, err := s.AppendMany(ctx, docs)
	if err != nil {
		t.Fatal(err)
	}

	return ids
}

func TestStreamAppendInfoRange(t *testing.T) {
	s, ctx := testStream(t)

	id, err := s.Append(ctx, SOrder{Order: 1, Total: 9.9})
	if err != nil {
		t.Fatal(err)
	}

	if IDTime(id).IsZero() {
		t.Fatalf("id = %q", id)
	}

	ids := appendOrders(t, ctx, s, 2, 4)

	if len(ids) != 3 {
		t.Fatalf("ids = %v", ids)
	}

	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if info.Length != 4 || info.FirstID != id || info.LastID != ids[2] || info.EntriesAdded != 4 {
		t.Fatalf("info = %+v", info)
	}

	all, err := s.Range(ctx, "-", "+", 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(all) != 4 || all[0].Doc.Order != 1 || all[0].Doc.Total != 9.9 || all[0].Time.IsZero() {
		t.Fatalf("range = %+v", all)
	}

	last, err := s.RevRange(ctx, "+", "-", 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(last) != 1 || last[0].ID != ids[2] {
		t.Fatalf("revrange = %+v", last)
	}

	if _, err := s.Append(ctx, SOrder{}, WithEntryID("1-1")); !errors.Is(err, ErrStreamIDTooSmall) {
		t.Fatalf("small id: %v", err)
	}

	n, err := s.Delete(ctx, id)
	if err != nil || n != 1 {
		t.Fatalf("delete = %d, %v", n, err)
	}

	deleted, err := s.Trim(ctx, MaxLen(1, false))
	if err != nil || deleted != 2 {
		t.Fatalf("trim = %d, %v", deleted, err)
	}
}

func TestStreamAppendWithTrimAndNoCreate(t *testing.T) {
	s, ctx := testStream(t)

	if _, err := s.Append(ctx, SOrder{Order: 1}, NoCreate()); !errors.Is(err, ErrUnknownCollection) {
		t.Fatalf("no create: %v", err)
	}

	appendOrders(t, ctx, s, 1, 5)

	if _, err := s.Append(ctx, SOrder{Order: 6}, WithTrim(MaxLen(2, false))); err != nil {
		t.Fatal(err)
	}

	info, err := s.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if info.Length != 2 {
		t.Fatalf("length = %d", info.Length)
	}
}

func TestStreamGroupReadAckPendingClaim(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("workers")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	if err := g.Create(ctx, StartAll); !errors.Is(err, ErrGroupExists) {
		t.Fatalf("create again: %v", err)
	}

	if err := g.Ensure(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 3)

	got, err := g.Read(ctx, "w1", Read{Count: 2})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0].ID != ids[0] || got[1].Doc.Order != 2 {
		t.Fatalf("read = %+v", got)
	}

	acked, err := g.Ack(ctx, ids[0])
	if err != nil || acked != 1 {
		t.Fatalf("ack = %d, %v", acked, err)
	}

	sum, err := g.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if sum.Count != 1 || *sum.Lowest != ids[1] || len(sum.Consumers) != 1 || sum.Consumers[0].Name != "w1" {
		t.Fatalf("pending = %+v", sum)
	}

	mine, err := g.Read(ctx, "w1", Read{Pending: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(mine) != 1 || mine[0].ID != ids[1] {
		t.Fatalf("own pending = %+v", mine)
	}

	list, err := g.PendingList(ctx, PendingQuery{Consumer: "w1"})
	if err != nil {
		t.Fatal(err)
	}

	// The pending read above counts as a delivery, as in Redis.
	if len(list) != 1 || list[0].ID != ids[1] || list[0].Deliveries != 2 {
		t.Fatalf("pending list = %+v", list)
	}

	claimed, err := g.Claim(ctx, "w2", 0, ids[1])
	if err != nil {
		t.Fatal(err)
	}

	if len(claimed.Entries) != 1 || claimed.Entries[0].Doc.Order != 2 {
		t.Fatalf("claim = %+v", claimed)
	}

	auto, err := g.AutoClaim(ctx, "w3", 0, "", 10)
	if err != nil {
		t.Fatal(err)
	}

	if auto.Cursor != "0-0" || len(auto.Entries) != 1 {
		t.Fatalf("autoclaim = %+v", auto)
	}

	consumers, err := g.Consumers(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(consumers) != 3 {
		t.Fatalf("consumers = %+v", consumers)
	}

	groups, err := s.Groups(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if len(groups) != 1 || groups[0].Pending != 1 || groups[0].LastDeliveredID != ids[1] {
		t.Fatalf("groups = %+v", groups)
	}

	held, err := g.DeleteConsumer(ctx, "w3")
	if err != nil || held != 1 {
		t.Fatalf("delete consumer = %d, %v", held, err)
	}

	if err := g.Destroy(ctx); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Read(ctx, "w1", Read{}); !errors.Is(err, ErrUnknownGroup) {
		t.Fatalf("read after destroy: %v", err)
	}
}

func TestStreamReadBlocksUntilAnAppend(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("workers")

	if err := g.Create(ctx, StartNew); err != nil {
		t.Fatal(err)
	}

	go func() {
		time.Sleep(200 * time.Millisecond)

		s.Append(ctx, SOrder{Order: 7})
	}()

	start := time.Now()

	got, err := g.Read(ctx, "w1", Read{Block: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Doc.Order != 7 || time.Since(start) > 4*time.Second {
		t.Fatalf("read = %+v after %v", got, time.Since(start))
	}

	empty, err := g.Read(ctx, "w1", Read{Block: 50 * time.Millisecond})
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty read = %+v, %v", empty, err)
	}
}

func TestStreamOfMap(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	s, err := StreamOf[map[string]any](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Append(ctx, map[string]any{"kind": "x", "n": 1}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Range(ctx, "-", "+", 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Doc["kind"] != "x" {
		t.Fatalf("range = %+v", got)
	}
}

func TestStreamWorkDoesEveryEntryOnce(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("workers")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 50)

	var mu sync.Mutex

	seen := map[int]int{}

	runCtx, stop := context.WithCancel(ctx)

	var wg sync.WaitGroup

	for _, name := range []string{"w1", "w2", "w3"} {
		wg.Add(1)

		go func() {
			defer wg.Done()

			g.Work(runCtx, name, func(_ context.Context, e Entry[SOrder]) error {
				mu.Lock()
				seen[e.Doc.Order]++
				mu.Unlock()

				return nil
			}, Work{Block: 100 * time.Millisecond})
		}()
	}

	appendOrders(t, ctx, s, 51, 60)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(seen) == 60
	})

	stop()
	wg.Wait()

	for order, n := range seen {
		if n != 1 {
			t.Fatalf("order %d done %d times", order, n)
		}
	}

	sum, err := g.Pending(ctx)
	if err != nil || sum.Count != 0 {
		t.Fatalf("pending = %+v, %v", sum, err)
	}
}

func TestStreamWorkRetriesAndBuriesPoison(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("workers")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 2)

	deadName := s.Name() + "_dead"

	var mu sync.Mutex

	tries := map[int]int{}
	poisoned := 0

	runCtx, stop := context.WithCancel(ctx)

	done := make(chan struct{})

	go func() {
		defer close(done)

		g.Work(runCtx, "w1", func(_ context.Context, e Entry[SOrder]) error {
			mu.Lock()
			defer mu.Unlock()

			tries[e.Doc.Order]++

			// SOrder 1 fails once, then works; order 2 always fails.
			if e.Doc.Order == 2 || tries[1] == 1 && e.Doc.Order == 1 {
				return errors.New("boom")
			}

			return nil
		}, Work{
			Block:         50 * time.Millisecond,
			ClaimIdle:     100 * time.Millisecond,
			ClaimEvery:    100 * time.Millisecond,
			MaxDeliveries: 3,
			DeadLetter:    deadName,
			OnError: func(err error) {
				if errors.Is(err, ErrPoison) {
					mu.Lock()
					poisoned++
					mu.Unlock()
				}
			},
		})
	}()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return poisoned == 1
	})

	stop()
	<-done

	if tries[1] != 2 || tries[2] != 3 {
		t.Fatalf("tries = %v", tries)
	}

	sum, err := g.Pending(ctx)
	if err != nil || sum.Count != 0 {
		t.Fatalf("pending = %+v, %v", sum, err)
	}

	dead, err := StreamOf[DeadEntry](s.db, deadName)
	if err != nil {
		t.Fatal(err)
	}

	got, err := dead.Range(ctx, "-", "+", 0)
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Doc.ID != ids[1] || got[0].Doc.Deliveries != 3 || got[0].Doc.Group != "workers" {
		t.Fatalf("dead = %+v", got)
	}
}

// An entry that fails on its last delivery goes to the dead letter at
// once, not after one more ClaimIdle.
func TestStreamWorkBuriesAtTheLastFailure(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("workers")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 7, 7)

	deadName := s.Name() + "_dead"
	poisoned := make(chan struct{}, 1)

	runCtx, stop := context.WithCancel(ctx)

	defer stop()

	go g.Work(runCtx, "w1", func(context.Context, Entry[SOrder]) error {
		return errors.New("boom")
	}, Work{
		Block:         50 * time.Millisecond,
		ClaimIdle:     time.Hour,
		MaxDeliveries: 1,
		DeadLetter:    deadName,
		OnError: func(err error) {
			if errors.Is(err, ErrPoison) {
				poisoned <- struct{}{}
			}
		},
	})

	select {
	case <-poisoned:
	case <-time.After(3 * time.Second):
		t.Fatal("the entry did not go to the dead letter at its last failure")
	}

	dead, err := StreamOf[DeadEntry](s.db, deadName)
	if err != nil {
		t.Fatal(err)
	}

	got, err := dead.Range(ctx, "-", "+", 0)

	if err != nil || len(got) != 1 || got[0].Doc.ID != ids[0] || got[0].Doc.Deliveries != 1 {
		t.Fatalf("dead = %+v, %v", got, err)
	}

	sum, err := g.Pending(ctx)

	if err != nil || sum.Count != 0 {
		t.Fatalf("pending = %+v, %v", sum, err)
	}
}

func TestStreamWorkStartsWithItsOwnPending(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("workers")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 3)

	// w1 took the entries and died before the acks.
	if _, err := g.Read(ctx, "w1", Read{}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex

	var got []int

	runCtx, stop := context.WithCancel(ctx)

	done := make(chan struct{})

	go func() {
		defer close(done)

		g.Work(runCtx, "w1", func(_ context.Context, e Entry[SOrder]) error {
			mu.Lock()
			got = append(got, e.Doc.Order)
			mu.Unlock()

			return nil
		}, Work{Block: 50 * time.Millisecond, ClaimIdle: -1})
	}()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(got) == 3
	})

	stop()
	<-done

	if got[0] != 1 || got[2] != 3 {
		t.Fatalf("got = %v", got)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)

	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}

		time.Sleep(20 * time.Millisecond)
	}
}

func TestStreamTail(t *testing.T) {
	s, ctx := testStream(t)
	ids := appendOrders(t, ctx, s, 1, 5)

	all, err := s.Tail(ctx, Tail{})

	if err != nil || len(all) != 5 || all[0].ID != ids[0] || all[4].Doc.Order != 5 {
		t.Fatalf("tail = %+v, %v", all, err)
	}

	page, err := s.Tail(ctx, Tail{After: ids[1], Count: 2})

	if err != nil || len(page) != 2 || page[0].ID != ids[2] || page[1].ID != ids[3] {
		t.Fatalf("tail after = %+v, %v", page, err)
	}

	only, err := s.Tail(ctx, Tail{After: ids[3], Fields: []string{"order"}})

	if err != nil || len(only) != 1 || only[0].Doc.Order != 5 || only[0].Doc.Total != 0 {
		t.Fatalf("tail fields = %+v, %v", only, err)
	}

	none, err := s.Tail(ctx, Tail{After: StartNew})

	if err != nil || len(none) != 0 {
		t.Fatalf("tail $ = %+v, %v", none, err)
	}

	// A tail changes no group: the pending list stays empty.
	g := s.Group("workers")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Tail(ctx, Tail{}); err != nil {
		t.Fatal(err)
	}

	sum, err := g.Pending(ctx)

	if err != nil || sum.Count != 0 {
		t.Fatalf("pending = %+v, %v", sum, err)
	}
}

func TestStreamTailBlocksUntilAnAppend(t *testing.T) {
	s, ctx := testStream(t)

	appendOrders(t, ctx, s, 1, 1)

	go func() {
		time.Sleep(200 * time.Millisecond)

		s.Append(ctx, SOrder{Order: 7})
	}()

	start := time.Now()

	got, err := s.Tail(ctx, Tail{After: StartNew, Block: 5 * time.Second})

	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Doc.Order != 7 || time.Since(start) > 4*time.Second {
		t.Fatalf("tail = %+v after %v", got, time.Since(start))
	}

	empty, err := s.Tail(ctx, Tail{After: got[0].ID, Block: 50 * time.Millisecond})

	if err != nil || len(empty) != 0 {
		t.Fatalf("empty tail = %+v, %v", empty, err)
	}
}

func TestStreamReadsThatWaitUseTheWatchClient(t *testing.T) {
	for path, want := range map[string]bool{
		"/orders/_streams/read":               true,
		"/orders/_streams/groups/g/read":      true,
		"/orders/_streams/groups/read/ack":    false,
		"/orders/_streams/groups/g/consumers": false,
		"/orders/_search":                     false,
	} {
		if got := isStreamRead(path); got != want {
			t.Errorf("isStreamRead(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestStreamRangeWithoutAnEnd(t *testing.T) {
	s, ctx := testStream(t)
	ids := appendOrders(t, ctx, s, 1, 4)

	up, err := s.Range(ctx, "("+ids[0], "("+ids[3], 0)

	if err != nil || len(up) != 2 || up[0].ID != ids[1] || up[1].ID != ids[2] {
		t.Fatalf("range = %+v, %v", up, err)
	}

	down, err := s.RevRange(ctx, "("+ids[3], "-", 1)

	if err != nil || len(down) != 1 || down[0].ID != ids[2] {
		t.Fatalf("revrange = %+v, %v", down, err)
	}
}

func TestStreamGroupNoCreate(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("workers")

	if err := g.Create(ctx, StartNew, GroupNoCreate()); !errors.Is(err, ErrUnknownCollection) {
		t.Fatalf("create = %v, want unknown_collection", err)
	}

	appendOrders(t, ctx, s, 1, 1)

	if err := g.Create(ctx, StartNew, GroupNoCreate()); err != nil {
		t.Fatal(err)
	}
}

func TestStreamClaimIDs(t *testing.T) {
	s, ctx := testStream(t)
	ids := appendOrders(t, ctx, s, 1, 3)
	g := s.Group("workers")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Read(ctx, "w1", Read{}); err != nil {
		t.Fatal(err)
	}

	got, err := g.ClaimIDs(ctx, "w2", 0, ids[0], ids[1])

	if err != nil || len(got.IDs) != 2 || got.IDs[0] != ids[0] || got.Cursor != "" {
		t.Fatalf("claim ids = %+v, %v", got, err)
	}

	scan, err := g.AutoClaimIDs(ctx, "w3", 0, "", 10)

	if err != nil || len(scan.IDs) != 3 || scan.Cursor != "0-0" {
		t.Fatalf("autoclaim ids = %+v, %v", scan, err)
	}

	// JUSTID does not count as a delivery.
	list, err := g.PendingList(ctx, PendingQuery{})

	if err != nil || len(list) != 3 || list[0].Consumer != "w3" || list[0].Deliveries != 1 {
		t.Fatalf("pending = %+v, %v", list, err)
	}
}
