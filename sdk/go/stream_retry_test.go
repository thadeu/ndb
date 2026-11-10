package ndb

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// startWork runs g.Work in a goroutine. The stop it gives ends the run and
// waits for Work to return.
func startWork(t *testing.T, ctx context.Context, g *Group[SOrder], consumer string, h Handler[SOrder], w Work) func() {
	t.Helper()

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		g.Work(runCtx, consumer, h, w)
	}()

	stop := func() {
		cancel()
		<-done
	}

	t.Cleanup(stop)

	return stop
}

func pendingCount(t *testing.T, ctx context.Context, g *Group[SOrder]) PendingSummary {
	t.Helper()

	sum, err := g.Pending(ctx)

	if err != nil {
		t.Fatal(err)
	}

	return sum
}

func deadEntries(t *testing.T, ctx context.Context, s *Stream[SOrder], name string) []Entry[DeadEntry] {
	t.Helper()

	dead, err := StreamOf[DeadEntry](s.db, name)

	if err != nil {
		t.Fatal(err)
	}

	got, err := dead.Range(ctx, "-", "+", 0)

	if err != nil && !errors.Is(err, ErrUnknownCollection) {
		t.Fatal(err)
	}

	return got
}

func TestTiers(t *testing.T) {
	retry := Tiers(time.Second, 2*time.Second, 3*time.Second)
	want := map[int64]time.Duration{0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 3 * time.Second, 4: 3 * time.Second, 100: 3 * time.Second}

	for n, wait := range want {
		if got := retry(n); got != wait {
			t.Errorf("Tiers(%d) = %v, want %v", n, got, wait)
		}
	}

	if got := Tiers()(3); got != 0 {
		t.Errorf("Tiers() = %v, want 0", got)
	}
}

func TestTierSteps(t *testing.T) {
	retry := TierSteps(Step{Wait: time.Second, UpTo: 2}, Step{Wait: time.Minute, UpTo: 4})
	want := map[int64]time.Duration{0: time.Second, 1: time.Second, 2: time.Second, 3: time.Minute, 4: time.Minute, 5: time.Minute, 50: time.Minute}

	for n, wait := range want {
		if got := retry(n); got != wait {
			t.Errorf("TierSteps(%d) = %v, want %v", n, got, wait)
		}
	}

	open := TierSteps(Step{Wait: time.Second, UpTo: 1}, Step{Wait: time.Hour})

	if open(1) != time.Second || open(2) != time.Hour || open(1000) != time.Hour {
		t.Errorf("open step = %v %v %v", open(1), open(2), open(1000))
	}

	if got := TierSteps()(3); got != 0 {
		t.Errorf("TierSteps() = %v, want 0", got)
	}
}

// The retry schedule of the fsw-esl outbox on RabbitMQ (retryTierFor in
// internal/jobworker/outbox_retry.go): try 1 waits 15 s, 2-5 wait 1 min,
// 6-11 wait 10 min, 12-23 wait 1 h, then 6 h, until the max age of 15 days
// moves the job to dead.
func TestRetryScheduleOfTheFswEslOutbox(t *testing.T) {
	steps := TierSteps(
		Step{Wait: 15 * time.Second, UpTo: 1},
		Step{Wait: time.Minute, UpTo: 5},
		Step{Wait: 10 * time.Minute, UpTo: 11},
		Step{Wait: time.Hour, UpTo: 23},
		Step{Wait: 6 * time.Hour},
	)

	waits := []time.Duration{15 * time.Second}
	waits = append(waits, slices.Repeat([]time.Duration{time.Minute}, 4)...)
	waits = append(waits, slices.Repeat([]time.Duration{10 * time.Minute}, 6)...)
	waits = append(waits, slices.Repeat([]time.Duration{time.Hour}, 12)...)
	waits = append(waits, 6*time.Hour)

	tiers := Tiers(waits...)

	for n := int64(1); n <= 200; n++ {
		if steps(n) != tiers(n) {
			t.Fatalf("try %d: steps %v, tiers %v", n, steps(n), tiers(n))
		}
	}

	checks := map[int64]time.Duration{1: 15 * time.Second, 2: time.Minute, 5: time.Minute, 6: 10 * time.Minute, 11: 10 * time.Minute, 12: time.Hour, 23: time.Hour, 24: 6 * time.Hour, 80: 6 * time.Hour}

	for n, wait := range checks {
		if got := steps(n); got != wait {
			t.Errorf("try %d waits %v, want %v", n, got, wait)
		}
	}

	// The deliveries of an entry that always fails, as Work runs them:
	// delivery n at its age, then Retry(n), until a delivery is past
	// MaxAge (it goes to the dead letter without the handler).
	maxAge := 15 * 24 * time.Hour
	age := time.Duration(0)
	tries := int64(0)

	for age <= maxAge {
		tries++
		age += steps(tries)
	}

	lastTry := age - steps(tries)

	// 23 tries reach 13 h 04 min 15 s, then one try each 6 h: 57 more
	// tries fit in 15 days, and the 81st try is at 355 h 04 min 15 s.
	if tries != 81 || lastTry != 355*time.Hour+4*time.Minute+15*time.Second {
		t.Fatalf("tries = %d, last at %v", tries, lastTry)
	}

	if age <= maxAge {
		t.Fatalf("delivery 82 at %v is not past the max age", age)
	}
}

func TestStreamNackClaimDuePendingList(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 3)

	if _, err := g.Read(ctx, "w1", Read{}); err != nil {
		t.Fatal(err)
	}

	before := time.Now()
	n, err := g.Nack(ctx, "w1", time.Hour, ids[0])

	if err != nil || n != 1 {
		t.Fatalf("nack = %d, %v", n, err)
	}

	// The entry is not pending for w2: it counts 0.
	n, err = g.Nack(ctx, "w2", 0, ids[1])

	if err != nil || n != 0 {
		t.Fatalf("nack of w2 = %d, %v", n, err)
	}

	n, err = g.Nack(ctx, "w1", 0, ids[1])

	if err != nil || n != 1 {
		t.Fatalf("nack due = %d, %v", n, err)
	}

	if n, err := g.Nack(ctx, "w1", time.Second); err != nil || n != 0 {
		t.Fatalf("nack of no ids = %d, %v", n, err)
	}

	sum := pendingCount(t, ctx, g)

	if sum.Count != 3 || sum.Waiting != 1 || len(sum.Consumers) != 1 || sum.Consumers[0].Count != 1 {
		t.Fatalf("summary = %+v", sum)
	}

	rows, err := g.PendingList(ctx, PendingQuery{})

	if err != nil || len(rows) != 3 {
		t.Fatalf("rows = %+v, %v", rows, err)
	}

	waiting := rows[0]

	if waiting.Consumer != "" || waiting.RetryAt == nil || waiting.Deliveries != 1 {
		t.Fatalf("waiting row = %+v", waiting)
	}

	if at := *waiting.RetryAt; at.Before(before.Add(time.Hour-time.Second)) || at.After(time.Now().Add(time.Hour+time.Second)) {
		t.Fatalf("retry at = %v", at)
	}

	if rows[1].Consumer != "" || rows[1].RetryAt == nil || rows[2].Consumer != "w1" || rows[2].RetryAt != nil {
		t.Fatalf("rows = %+v", rows)
	}

	yes, no := true, false
	due, err := g.PendingList(ctx, PendingQuery{Due: &yes})

	if err != nil || len(due) != 1 || due[0].ID != ids[1] {
		t.Fatalf("due = %+v, %v", due, err)
	}

	notDue, err := g.PendingList(ctx, PendingQuery{Due: &no})

	if err != nil || len(notDue) != 1 || notDue[0].ID != ids[0] {
		t.Fatalf("not due = %+v, %v", notDue, err)
	}

	// A nacked entry has no owner: the pending read of w1 does not give it.
	own, err := g.Read(ctx, "w1", Read{Pending: true})

	if err != nil || len(own) != 1 || own[0].ID != ids[2] {
		t.Fatalf("own pending = %+v, %v", own, err)
	}

	// The waiting entry is not claimed, by IDs or by a scan.
	claimed, err := g.Claim(ctx, "w2", 0, ids[0])

	if err != nil || len(claimed.Entries) != 0 {
		t.Fatalf("claim of a waiting entry = %+v, %v", claimed, err)
	}

	dueClaim, err := g.ClaimDue(ctx, "w2", "", 10)

	if err != nil || dueClaim.Cursor != "0-0" || len(dueClaim.Entries) != 1 || dueClaim.Entries[0].ID != ids[1] || dueClaim.Entries[0].Doc.Order != 2 {
		t.Fatalf("claim due = %+v, %v", dueClaim, err)
	}

	rows, err = g.PendingList(ctx, PendingQuery{Consumer: "w2"})

	if err != nil || len(rows) != 1 || rows[0].RetryAt != nil || rows[0].Deliveries != 2 {
		t.Fatalf("rows of w2 = %+v, %v", rows, err)
	}

	again, err := g.ClaimDueIDs(ctx, "w3", "0-0", 10)

	if err != nil || len(again.IDs) != 0 || again.Cursor != "0-0" {
		t.Fatalf("claim due ids = %+v, %v", again, err)
	}

	// An ack of a waiting entry cancels the retry.
	if n, err := g.Ack(ctx, ids[0]); err != nil || n != 1 {
		t.Fatalf("ack = %d, %v", n, err)
	}

	if sum := pendingCount(t, ctx, g); sum.Waiting != 0 || sum.Count != 2 {
		t.Fatalf("summary after ack = %+v", sum)
	}

	if _, err := g.Nack(ctx, "w2", -time.Second, ids[1]); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("nack with a negative delay: %v", err)
	}
}

// Shared case 1: an entry fails twice, and comes back after each wait of
// Retry, not before.
func TestStreamWorkRetryComesBackAfterEachWait(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 1)

	var mu sync.Mutex

	times := []time.Time{}

	stop := startWork(t, ctx, g, "w1", func(context.Context, Entry[SOrder]) error {
		mu.Lock()
		defer mu.Unlock()

		times = append(times, time.Now())

		if len(times) <= 2 {
			return errors.New("boom")
		}

		return nil
	}, Work{
		Block:     50 * time.Millisecond,
		ClaimIdle: time.Hour,
		DueEvery:  20 * time.Millisecond,
		Retry:     Tiers(300*time.Millisecond, 600*time.Millisecond),
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(times) == 3
	})

	waitFor(t, func() bool {
		return pendingCount(t, ctx, g).Count == 0
	})

	stop()

	first, second := times[1].Sub(times[0]), times[2].Sub(times[1])

	if first < 300*time.Millisecond || second < 600*time.Millisecond {
		t.Fatalf("the entry came back before its wait: %v, %v", first, second)
	}

	if first > 2*time.Second || second > 3*time.Second {
		t.Fatalf("the entry came back too late: %v, %v", first, second)
	}
}

// Shared case 2: an entry past MaxAge goes to the dead letter with reason
// max_age, and is acked, without the handler and with no retry.
func TestStreamWorkMaxAgeGoesToTheDeadLetter(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	old := fmt.Sprintf("%d-0", time.Now().Add(-time.Hour).UnixMilli())
	oldID, err := s.Append(ctx, SOrder{Order: 1}, WithEntryID(old))

	if err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 2, 2)

	deadName := s.Name() + "_dead"

	var mu sync.Mutex

	handled := []int{}
	poisoned := 0

	stop := startWork(t, ctx, g, "w1", func(_ context.Context, e Entry[SOrder]) error {
		mu.Lock()
		defer mu.Unlock()

		handled = append(handled, e.Doc.Order)

		return nil
	}, Work{
		Block:      50 * time.Millisecond,
		ClaimIdle:  time.Hour,
		DueEvery:   20 * time.Millisecond,
		Retry:      Tiers(10 * time.Millisecond),
		MaxAge:     30 * time.Minute,
		DeadLetter: deadName,
		OnError: func(err error) {
			mu.Lock()
			defer mu.Unlock()

			if errors.Is(err, ErrPoison) {
				poisoned++
			}
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return poisoned == 1 && len(handled) == 1
	})

	stop()

	if !slices.Equal(handled, []int{2}) {
		t.Fatalf("handled = %v", handled)
	}

	if sum := pendingCount(t, ctx, g); sum.Count != 0 {
		t.Fatalf("pending = %+v", sum)
	}

	got := deadEntries(t, ctx, s, deadName)

	if len(got) != 1 || got[0].Doc.ID != oldID || got[0].Doc.Reason != ReasonMaxAge || got[0].Doc.Deliveries != 1 {
		t.Fatalf("dead = %+v", got)
	}
}

// An entry that gets too old in the handler and fails goes to the dead
// letter at once with max_age: it is not nacked first.
func TestStreamWorkMaxAgeAtAFailureIsNotNacked(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 1)
	deadName := s.Name() + "_dead"
	poisoned := make(chan struct{}, 1)

	stop := startWork(t, ctx, g, "w1", func(context.Context, Entry[SOrder]) error {
		time.Sleep(150 * time.Millisecond)

		return errors.New("boom")
	}, Work{
		Block:      50 * time.Millisecond,
		ClaimIdle:  time.Hour,
		DueEvery:   20 * time.Millisecond,
		Retry:      Tiers(time.Hour),
		MaxAge:     100 * time.Millisecond,
		DeadLetter: deadName,
		OnError: func(err error) {
			if errors.Is(err, ErrPoison) {
				select {
				case poisoned <- struct{}{}:
				default:
				}
			}
		},
	})

	select {
	case <-poisoned:
	case <-time.After(5 * time.Second):
		t.Fatal("the entry did not go to the dead letter at its failure")
	}

	stop()

	got := deadEntries(t, ctx, s, deadName)

	if len(got) != 1 || got[0].Doc.ID != ids[0] || got[0].Doc.Reason != ReasonMaxAge {
		t.Fatalf("dead = %+v", got)
	}

	if sum := pendingCount(t, ctx, g); sum.Count != 0 || sum.Waiting != 0 {
		t.Fatalf("pending = %+v: the entry was nacked", sum)
	}
}

// Shared case 3: a worker restarts while its entry waits. The restarted
// worker does not take it early, and a worker of the group takes it when
// it is due.
func TestStreamWorkRestartDoesNotTakeAWaitingEntry(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 1)

	wait := 1500 * time.Millisecond
	w := Work{
		Block:     50 * time.Millisecond,
		ClaimIdle: 100 * time.Millisecond,
		DueEvery:  20 * time.Millisecond,
		Retry:     Tiers(wait),
	}

	failed := make(chan time.Time, 1)

	stop := startWork(t, ctx, g, "w1", func(context.Context, Entry[SOrder]) error {
		failed <- time.Now()

		return errors.New("boom")
	}, w)

	failedAt := <-failed

	waitFor(t, func() bool {
		return pendingCount(t, ctx, g).Waiting == 1
	})

	stop()

	type take struct {
		consumer string
		at       time.Time
	}

	var mu sync.Mutex

	takes := []take{}

	record := func(consumer string) Handler[SOrder] {
		return func(context.Context, Entry[SOrder]) error {
			mu.Lock()
			defer mu.Unlock()

			takes = append(takes, take{consumer, time.Now()})

			return nil
		}
	}

	stopW1 := startWork(t, ctx, g, "w1", record("w1"), w)
	stopW2 := startWork(t, ctx, g, "w2", record("w2"), w)

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return len(takes) == 1
	})

	waitFor(t, func() bool {
		return pendingCount(t, ctx, g).Count == 0
	})

	time.Sleep(200 * time.Millisecond)

	stopW1()
	stopW2()

	if len(takes) != 1 {
		t.Fatalf("takes = %+v", takes)
	}

	if took := takes[0].at.Sub(failedAt); took < wait {
		t.Fatalf("%s took the entry %v after the failure, before its wait of %v", takes[0].consumer, took, wait)
	}
}

// With Retry, the failure on the MaxDeliveries-th delivery buries the
// entry: it is not nacked.
func TestStreamWorkRetryBuriesAtMaxDeliveries(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 1)
	deadName := s.Name() + "_dead"

	var mu sync.Mutex

	tries := 0
	poisoned := 0

	stop := startWork(t, ctx, g, "w1", func(context.Context, Entry[SOrder]) error {
		mu.Lock()
		defer mu.Unlock()

		tries++

		return errors.New("boom")
	}, Work{
		Block:         50 * time.Millisecond,
		ClaimIdle:     time.Hour,
		DueEvery:      20 * time.Millisecond,
		Retry:         Tiers(50 * time.Millisecond),
		MaxDeliveries: 2,
		DeadLetter:    deadName,
		OnError: func(err error) {
			mu.Lock()
			defer mu.Unlock()

			if errors.Is(err, ErrPoison) {
				poisoned++
			}
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return poisoned == 1
	})

	time.Sleep(200 * time.Millisecond)

	stop()

	if tries != 2 {
		t.Fatalf("tries = %d", tries)
	}

	if sum := pendingCount(t, ctx, g); sum.Count != 0 {
		t.Fatalf("pending = %+v", sum)
	}

	got := deadEntries(t, ctx, s, deadName)

	if len(got) != 1 || got[0].Doc.ID != ids[0] || got[0].Doc.Reason != ReasonMaxDeliveries || got[0].Doc.Deliveries != 2 {
		t.Fatalf("dead = %+v", got)
	}
}

// An entry that does not decode is a failure of the handler: it gets
// Retry too, and goes to the dead letter after MaxDeliveries, long before
// ClaimIdle.
func TestStreamWorkRetriesAnEntryThatDoesNotDecode(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendBad(t, ctx, s)
	deadName := s.Name() + "_dead"

	var mu sync.Mutex

	handled := []int{}
	poisoned := 0

	stop := startWork(t, ctx, g, "w1", func(_ context.Context, e Entry[SOrder]) error {
		mu.Lock()
		defer mu.Unlock()

		handled = append(handled, e.Doc.Order)

		return nil
	}, Work{
		Block:         50 * time.Millisecond,
		ClaimIdle:     time.Hour,
		DueEvery:      20 * time.Millisecond,
		Retry:         Tiers(50 * time.Millisecond),
		MaxDeliveries: 2,
		DeadLetter:    deadName,
		OnError: func(err error) {
			mu.Lock()
			defer mu.Unlock()

			if errors.Is(err, ErrPoison) {
				poisoned++
			}
		},
	})

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()

		return poisoned == 1
	})

	stop()

	if !slices.Equal(handled, []int{1, 3}) {
		t.Fatalf("handled = %v", handled)
	}

	got := deadEntries(t, ctx, s, deadName)

	if len(got) != 1 || got[0].Doc.ID != ids[1] || got[0].Doc.Deliveries != 2 || got[0].Doc.Reason != ReasonMaxDeliveries {
		t.Fatalf("dead = %+v", got)
	}
}

// A stop during the handler is not a failure: with Retry too, the entry
// stays pending for its consumer, not nacked.
func TestStreamWorkRetryKeepsTheEntryPendingAtShutdown(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	appendOrders(t, ctx, s, 1, 1)

	started := make(chan struct{})

	var mu sync.Mutex

	reported := []error{}

	stop := startWork(t, ctx, g, "w1", func(ctx context.Context, e Entry[SOrder]) error {
		close(started)

		<-ctx.Done()

		return ctx.Err()
	}, Work{
		Block:     50 * time.Millisecond,
		ClaimIdle: time.Hour,
		Retry:     Tiers(time.Millisecond),
		OnError: func(err error) {
			mu.Lock()
			defer mu.Unlock()

			reported = append(reported, err)
		},
	})

	<-started

	stop()

	if len(reported) != 0 {
		t.Fatalf("reported = %v", reported)
	}

	rows, err := g.PendingList(ctx, PendingQuery{})

	if err != nil || len(rows) != 1 || rows[0].Consumer != "w1" || rows[0].RetryAt != nil {
		t.Fatalf("rows = %+v, %v", rows, err)
	}
}

// Many waiting entries do not hide an idle entry from the idle claim.
func TestStreamWorkIdleClaimSkipsWaitingEntries(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 26)

	// w0 took the 26 entries, nacked the first 25 for an hour and died
	// with the last one. With Count 2, the waiting entries fill 12 pages.
	if _, err := g.Read(ctx, "w0", Read{Count: 26}); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Nack(ctx, "w0", time.Hour, ids[:25]...); err != nil {
		t.Fatal(err)
	}

	got := make(chan int, 26)

	stop := startWork(t, ctx, g, "w1", func(_ context.Context, e Entry[SOrder]) error {
		got <- e.Doc.Order

		return nil
	}, Work{
		Count:      2,
		Block:      50 * time.Millisecond,
		ClaimIdle:  100 * time.Millisecond,
		ClaimEvery: 100 * time.Millisecond,
		DueEvery:   20 * time.Millisecond,
	})

	select {
	case order := <-got:
		if order != 26 {
			t.Fatalf("handled order %d", order)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the idle claim did not take the entry after the waiting ones")
	}

	stop()

	if sum := pendingCount(t, ctx, g); sum.Count != 25 || sum.Waiting != 25 {
		t.Fatalf("pending = %+v", sum)
	}
}

// A worker without Retry (no due claim) still takes a due nacked entry,
// with the idle claim.
func TestStreamWorkIdleClaimTakesADueNackedEntry(t *testing.T) {
	s, ctx := testStream(t)
	g := s.Group("g")

	if err := g.Create(ctx, StartAll); err != nil {
		t.Fatal(err)
	}

	ids := appendOrders(t, ctx, s, 1, 1)

	if _, err := g.Read(ctx, "w0", Read{}); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Nack(ctx, "w0", 0, ids[0]); err != nil {
		t.Fatal(err)
	}

	got := make(chan int, 1)

	stop := startWork(t, ctx, g, "w1", func(_ context.Context, e Entry[SOrder]) error {
		got <- e.Doc.Order

		return nil
	}, Work{
		Block:      50 * time.Millisecond,
		ClaimIdle:  50 * time.Millisecond,
		ClaimEvery: 50 * time.Millisecond,
	})

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the idle claim did not take the due nacked entry")
	}

	stop()

	waitFor(t, func() bool {
		return pendingCount(t, ctx, g).Count == 0
	})
}
