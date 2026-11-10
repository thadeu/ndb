package ndb

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// appendBad appends {order: 1}, {order: "bad"}, {order: 3}: the second doc
// does not decode into SOrder.
func appendBad(t *testing.T, ctx context.Context, s *Stream[SOrder]) []string {
	t.Helper()

	raw, err := StreamOf[map[string]any](s.db, s.Name())

	if err != nil {
		t.Fatal(err)
	}

	ids, err := raw.AppendMany(ctx, []map[string]any{{"order": 1}, {"order": "bad"}, {"order": 3}})

	if err != nil {
		t.Fatal(err)
	}

	return ids
}

// One doc that does not decode does not fail the read: it comes with Err
// set, and the other entries come too.
func TestStreamEntryThatDoesNotDecode(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendBad(t, ctx, s)

	check := func(what string, got []Entry[SOrder]) {
		t.Helper()

		if len(got) != 3 || got[0].Err != nil || got[2].Err != nil || got[0].Doc.Order != 1 || got[2].Doc.Order != 3 {
			t.Fatalf("%s = %+v", what, got)
		}

		bad := got[1]

		if bad.ID != ids[1] || bad.Err == nil || bad.Doc != (SOrder{}) || len(bad.Raw()) == 0 {
			t.Fatalf("%s: bad entry = %+v", what, bad)
		}
	}

	read, err := g.Read(ctx, "c1", Read{})

	if err != nil {
		t.Fatal(err)
	}

	check("Read", read)

	all, err := s.Range(ctx, "-", "+", 0)

	if err != nil {
		t.Fatal(err)
	}

	check("Range", all)

	tail, err := s.Tail(ctx, Tail{})

	if err != nil {
		t.Fatal(err)
	}

	check("Tail", tail)

	claimed, err := g.Claim(ctx, "c2", 0, ids...)

	if err != nil {
		t.Fatal(err)
	}

	check("Claim", claimed.Entries)
}

// Work does the good entries of a batch, and sends the entry that does not
// decode to the dead letter with its raw doc after MaxDeliveries.
func TestStreamWorkBuriesAnEntryThatDoesNotDecode(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendBad(t, ctx, s)
	deadName := s.Name() + "_dead"

	var mu sync.Mutex

	handled := []int{}
	decodeErrs := 0
	poisoned := 0

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		g.Work(runCtx, "w1", func(_ context.Context, e Entry[SOrder]) error {
			mu.Lock()
			defer mu.Unlock()

			handled = append(handled, e.Doc.Order)

			return nil
		}, Work{
			Block:         50 * time.Millisecond,
			ClaimIdle:     100 * time.Millisecond,
			ClaimEvery:    100 * time.Millisecond,
			MaxDeliveries: 2,
			DeadLetter:    deadName,
			OnError: func(err error) {
				mu.Lock()
				defer mu.Unlock()

				if errors.Is(err, ErrPoison) {
					poisoned++

					return
				}

				decodeErrs++
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

	if !slices.Equal(handled, []int{1, 3}) || decodeErrs != 2 {
		t.Fatalf("handled = %v, decode errors = %d", handled, decodeErrs)
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

	if err != nil || len(got) != 1 || got[0].Doc.ID != ids[1] || got[0].Doc.Deliveries != 2 || string(got[0].Doc.Doc) != `{"order":"bad"}` {
		t.Fatalf("dead = %+v, %v", got, err)
	}
}

// A failed read of the own pending entries does not stop Work: a group that
// does not exist yet is reported, and Work goes on when it comes.
func TestStreamWorkGoesOnAfterAFailedPendingRead(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("late")

	var mu sync.Mutex

	got := []int{}
	reported := 0

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)

	go func() {
		done <- g.Work(runCtx, "w1", func(_ context.Context, e Entry[SOrder]) error {
			mu.Lock()
			defer mu.Unlock()

			got = append(got, e.Doc.Order)

			return nil
		}, Work{Block: 50 * time.Millisecond, ClaimIdle: -1, OnError: func(error) {
			mu.Lock()
			defer mu.Unlock()

			reported++
		}})
	}()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return reported > 0
	})

	select {
	case err := <-done:
		t.Fatalf("Work returned %v before ctx ended", err)
	default:
	}

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 2)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(got) == 2
	})

	stop()

	if err := <-done; err != nil {
		t.Fatalf("Work = %v", err)
	}
}

// A handler that fails because Work stops does not use up a delivery: the
// entry stays pending, and does not go to the dead letter.
func TestStreamWorkKeepsTheEntryPendingAtShutdown(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 1)

	deadName := s.Name() + "_dead"
	started := make(chan struct{})

	var mu sync.Mutex

	reported := []error{}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		g.Work(runCtx, "w1", func(ctx context.Context, e Entry[SOrder]) error {
			close(started)

			<-ctx.Done()

			return ctx.Err()
		}, Work{
			Block:         50 * time.Millisecond,
			ClaimIdle:     time.Hour,
			MaxDeliveries: 1,
			DeadLetter:    deadName,
			OnError: func(err error) {
				mu.Lock()
				defer mu.Unlock()

				reported = append(reported, err)
			},
		})
	}()

	<-started

	stop()
	<-done

	if len(reported) != 0 {
		t.Fatalf("reported = %v", reported)
	}

	sum, err := g.Pending(ctx)

	if err != nil || sum.Count != 1 {
		t.Fatalf("pending = %+v, %v", sum, err)
	}

	dead, err := StreamOf[DeadEntry](s.db, deadName)

	if err != nil {
		t.Fatal(err)
	}

	got, err := dead.Range(ctx, "-", "+", 0)

	if len(got) != 0 || err != nil && !errors.Is(err, ErrUnknownCollection) {
		t.Fatalf("dead = %+v, %v", got, err)
	}
}

// bury appends to the dead letter and acks also when ctx ended.
func TestStreamBuryWithACancelledContext(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 1)

	entries, err := g.Read(ctx, "w1", Read{})

	if err != nil || len(entries) != 1 {
		t.Fatalf("read = %v, %v", entries, err)
	}

	deadName := s.Name() + "_dead"
	cancelled, cancel := context.WithCancel(ctx)

	cancel()

	g.bury(cancelled, Work{DeadLetter: deadName}, entries[0], 3, ReasonMaxDeliveries)

	sum, err := g.Pending(ctx)

	if err != nil || sum.Count != 0 {
		t.Fatalf("pending = %+v, %v", sum, err)
	}

	dead, err := StreamOf[DeadEntry](s.db, deadName)

	if err != nil {
		t.Fatal(err)
	}

	got, err := dead.Range(ctx, "-", "+", 0)

	if err != nil || len(got) != 1 || got[0].Doc.ID != entries[0].ID {
		t.Fatalf("dead = %+v, %v", got, err)
	}
}

// appendAt appends one entry for each ID, in order.
func appendAt(t *testing.T, ctx context.Context, s *Stream[SOrder], ids ...string) {
	t.Helper()

	for i, id := range ids {
		if _, err := s.Append(ctx, SOrder{Order: i}, WithEntryID(id)); err != nil {
			t.Fatal(err)
		}
	}
}

func entryIDs[T any](entries []Entry[T]) []string {
	out := []string{}

	for _, e := range entries {
		out = append(out, e.ID)
	}

	return out
}

// An ID with only the ms is <ms>-0 as a start and <ms>-<max> as an end,
// also when "(" leaves it out (checked against Redis 7.4).
func TestStreamRangeWithAnIDOfOnlyTheMs(t *testing.T) {
	s, ctx := testStream(t)

	appendAt(t, ctx, s, "5-0", "5-1", "6-0", "7-3")

	cases := []struct {
		start, end string
		rev        bool
		want       []string
	}{
		{"(5", "+", false, []string{"5-1", "6-0", "7-3"}},
		{"-", "(6", false, []string{"5-0", "5-1", "6-0"}},
		{"5", "6", false, []string{"5-0", "5-1", "6-0"}},
		{"(5-0", "(7-3", false, []string{"5-1", "6-0"}},
		{"(6", "7", false, []string{"7-3"}},
		{"(5", "+", true, []string{"7-3", "6-0", "5-1"}},
	}

	for _, c := range cases {
		var (
			got []Entry[SOrder]
			err error
		)

		if c.rev {
			got, err = s.RevRange(ctx, c.end, c.start, 0)
		} else {
			got, err = s.Range(ctx, c.start, c.end, 0)
		}

		if err != nil || !slices.Equal(entryIDs(got), c.want) {
			t.Fatalf("%s .. %s (rev %v) = %v, %v; want %v", c.start, c.end, c.rev, entryIDs(got), err, c.want)
		}
	}
}

// A bad ID is an error, not an empty answer.
func TestStreamBadIDs(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	appendAt(t, ctx, s, "5-0")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"abc", "5-", "-5", "(-", "(+", "5-x", "(5-", " 5"} {
		if _, err := s.Range(ctx, id, "+", 0); !errors.Is(err, ErrInvalidStreamID) {
			t.Fatalf("Range from %q = %v", id, err)
		}

		if _, err := s.RevRange(ctx, id, "-", 0); !errors.Is(err, ErrInvalidStreamID) {
			t.Fatalf("RevRange from %q = %v", id, err)
		}

		if _, err := g.PendingList(ctx, PendingQuery{Start: id}); !errors.Is(err, ErrInvalidStreamID) {
			t.Fatalf("PendingList from %q = %v", id, err)
		}
	}

	for _, id := range []string{"abc", "-", "+", "(5-0", "5-"} {
		if _, err := s.Delete(ctx, id); !errors.Is(err, ErrInvalidStreamID) {
			t.Fatalf("Delete %q = %v", id, err)
		}
	}

	// An ID with only the ms is <ms>-0, as in Redis.
	if n, err := s.Delete(ctx, "9"); err != nil || n != 0 {
		t.Fatalf("Delete 9 = %d, %v", n, err)
	}

	if n, err := s.Delete(ctx, "5"); err != nil || n != 1 {
		t.Fatalf("Delete 5 = %d, %v", n, err)
	}
}

// "(id" in PendingList leaves that ID out (checked against Redis 7.4).
func TestStreamPendingListWithoutItsEnds(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	appendAt(t, ctx, s, "5-0", "5-1", "6-0", "7-3")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Read(ctx, "c", Read{}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		start, end string
		want       []string
	}{
		{"(5-0", "(7-3", []string{"5-1", "6-0"}},
		{"(5", "(7", []string{"5-1", "6-0", "7-3"}},
		{"(5-1", "+", []string{"6-0", "7-3"}},
		{"-", "(6-0", []string{"5-0", "5-1"}},
		{"(0-0", "(0-0", []string{}},
	}

	for _, c := range cases {
		got, err := g.PendingList(ctx, PendingQuery{Start: c.start, End: c.end})

		if err != nil {
			t.Fatalf("%s .. %s: %v", c.start, c.end, err)
		}

		ids := []string{}

		for _, p := range got {
			ids = append(ids, p.ID)
		}

		if !slices.Equal(ids, c.want) {
			t.Fatalf("%s .. %s = %v; want %v", c.start, c.end, ids, c.want)
		}
	}
}

// An entry that crashed the process MaxDeliveries times (no ack, no
// failure) goes to the dead letter at the next start, without the handler.
func TestStreamWorkBuriesAnEntryThatCrashedTheProcess(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 2)

	// Three runs took the first entry and died in the handler.
	if _, err := g.Read(ctx, "w1", Read{Count: 1}); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if _, err := g.Read(ctx, "w1", Read{Pending: true, Count: 1}); err != nil {
			t.Fatal(err)
		}
	}

	deadName := s.Name() + "_dead"

	var mu sync.Mutex

	handled := []int{}

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		g.Work(runCtx, "w1", func(_ context.Context, e Entry[SOrder]) error {
			mu.Lock()
			defer mu.Unlock()

			handled = append(handled, e.Doc.Order)

			return nil
		}, Work{Block: 50 * time.Millisecond, ClaimIdle: time.Hour, MaxDeliveries: 3, DeadLetter: deadName})
	}()

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(handled) == 1
	})

	stop()
	<-done

	if !slices.Equal(handled, []int{2}) {
		t.Fatalf("handled = %v", handled)
	}

	dead, err := StreamOf[DeadEntry](s.db, deadName)

	if err != nil {
		t.Fatal(err)
	}

	got, err := dead.Range(ctx, "-", "+", 0)

	if err != nil || len(got) != 1 || got[0].Doc.ID != ids[0] || got[0].Doc.Deliveries != 3 {
		t.Fatalf("dead = %+v, %v", got, err)
	}

	sum, err := g.Pending(ctx)

	if err != nil || sum.Count != 0 {
		t.Fatalf("pending = %+v, %v", sum, err)
	}
}
