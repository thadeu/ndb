package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Handler does the work on one entry. nil acks it. An error is a failure:
// with Work.Retry, Work nacks the entry, and it comes back after the wait
// of Retry; without Retry, it stays pending, and after Work.ClaimIdle a
// worker of the group claims it and tries again. An error while ctx ends
// (Work stops) is not a failure: the entry stays pending and does not go
// to the dead letter.
type Handler[T any] func(ctx context.Context, e Entry[T]) error

// Work says how Group.Work runs.
type Work struct {
	// Count is the most entries per read: default 10.
	Count int

	// Block is how long a read waits for new entries: default 5 s.
	Block time.Duration

	// ClaimIdle: the entries pending for at least this long, of any
	// consumer of the group, are claimed and tried again. Default 1 min;
	// a negative value turns the claim off. Nacked entries that still wait
	// are not in this claim.
	ClaimIdle time.Duration

	// ClaimEvery is how often Work looks for idle entries: default
	// ClaimIdle / 2.
	ClaimEvery time.Duration

	// MaxDeliveries: an entry delivered this many times is a poison
	// message. When its handler fails on that delivery, it goes to
	// DeadLetter (when set) and is acked at once, also with Retry. Zero is
	// no limit.
	MaxDeliveries int64

	// DeadLetter is the stream that gets the poison messages and the
	// entries past MaxAge, as a DeadEntry.
	DeadLetter string

	// Retry (ndb only) gives the wait before the next try of an entry
	// whose handler failed on its deliveries-th delivery (1 is the first).
	// Work nacks the entry with this wait: it has no owner while it waits,
	// so no worker takes it early, and the due claim of any worker of the
	// group takes it when the wait ends. A wait below 0 is 0; a wait above
	// 30 days is 30 days. nil: the entry stays pending for the idle claim.
	// Tiers and TierSteps make one.
	Retry func(deliveries int64) time.Duration

	// MaxAge: an entry older than this, by the time of its ID (Entry.Time),
	// goes to DeadLetter (reason "max_age") without the handler, and is
	// acked: no more tries. Work looks at each delivery, the first one too.
	// Zero is no limit.
	MaxAge time.Duration

	// DueEvery is how often Work claims the nacked entries whose wait
	// ended (ClaimDue): default 1 s when Retry is set. A negative value
	// turns the due claim off; a positive value turns it on also without
	// Retry, for the entries that other workers nacked.
	DueEvery time.Duration

	// OnError gets the errors that do not stop Work: a failed read, a
	// handler error, a poison message. Default: none.
	OnError func(error)
}

// The reasons of a DeadEntry.
const (
	// ReasonMaxDeliveries: the entry failed on its MaxDeliveries-th
	// delivery, or it had MaxDeliveries deliveries without an ack (a
	// crash).
	ReasonMaxDeliveries = "max_deliveries"

	// ReasonMaxAge: the entry was older than MaxAge at a delivery.
	ReasonMaxAge = "max_age"
)

// maxRetryWait is the longest wait of a nack: 30 days.
const maxRetryWait = 30 * 24 * time.Hour

// DeadEntry is what Work appends to the dead-letter stream.
type DeadEntry struct {
	Stream     string          `ndb:"stream"`
	Group      string          `ndb:"group"`
	ID         string          `ndb:"id"`
	Deliveries int64           `ndb:"deliveries"`
	Doc        json.RawMessage `ndb:"doc"`

	// Reason is ReasonMaxDeliveries or ReasonMaxAge.
	Reason string `ndb:"reason"`
}

// ErrPoison is the error OnError gets for an entry that goes to the dead
// letter: a poison message, or an entry past MaxAge.
var ErrPoison = errors.New("ndb: poison message")

// Work runs a worker of the group as consumer until ctx ends, and then
// returns nil. It:
//
//  1. reads its own pending entries first (the ones it took before a
//     restart and did not ack), and does the work on them;
//  2. reads new entries, waiting up to Block for them;
//  3. acks each entry the handler did without an error, and nacks each
//     one that failed with the wait of Retry (when set);
//  4. every DueEvery, claims the nacked entries whose wait ended;
//  5. every ClaimEvery, claims the entries idle for ClaimIdle, and sends
//     the ones delivered MaxDeliveries times to DeadLetter.
//
// Delivery is at least once: the work on an entry must be safe to repeat.
// Run many workers with different consumer names to share the work. The
// group must exist (Group.Ensure). A failed read, also of a group that
// does not exist, goes to OnError, and Work tries again with a backoff.
//
// An entry whose doc does not decode into T (Entry.Err) does not go to the
// handler: it is a failure of the handler, so it gets Retry and, after
// MaxDeliveries, goes to DeadLetter with its raw doc. An own pending entry
// that already had MaxDeliveries deliveries without an ack (it crashed the
// process) goes to DeadLetter at the start, without the handler. A nacked
// entry has no owner, so the own pending read of a restart does not take
// it before its wait ends.
func (g *Group[T]) Work(ctx context.Context, consumer string, h Handler[T], w Work) error {
	w = w.withDefaults()

	g.workPending(ctx, consumer, h, w)

	nextClaim := time.Now().Add(w.ClaimEvery)
	nextDue := time.Now()
	dueCursor := "0-0"
	backoff := time.Duration(0)

	for ctx.Err() == nil {
		if w.ClaimIdle > 0 && !time.Now().Before(nextClaim) {
			g.workIdle(ctx, consumer, h, w)

			nextClaim = time.Now().Add(w.ClaimEvery)
		}

		if w.DueEvery > 0 && !time.Now().Before(nextDue) {
			var more bool

			dueCursor, more = g.workDue(ctx, consumer, h, w, dueCursor)
			nextDue = time.Now().Add(w.DueEvery)

			// A full batch: more entries can be due, so look again at once.
			if more {
				nextDue = time.Now()
			}
		}

		block := w.Block

		if w.ClaimIdle > 0 {
			block = min(block, time.Until(nextClaim))
		}

		if w.DueEvery > 0 {
			block = min(block, time.Until(nextDue))
		}

		entries, err := g.Read(ctx, consumer, Read{Count: w.Count, Block: max(block, time.Millisecond)})

		if err != nil {
			if ctx.Err() != nil {
				break
			}

			w.report(fmt.Errorf("ndb: read %s/%s: %w", g.s.name, g.name, err))

			backoff = nextBackoff(backoff)

			sleep(ctx, backoff)

			continue
		}

		backoff = 0

		g.handle(ctx, consumer, h, w, entries, nil)
	}

	return nil
}

func (w Work) withDefaults() Work {
	if w.Count <= 0 {
		w.Count = 10
	}

	if w.Block <= 0 {
		w.Block = 5 * time.Second
	}

	if w.ClaimIdle == 0 {
		w.ClaimIdle = time.Minute
	}

	if w.ClaimEvery <= 0 {
		w.ClaimEvery = max(w.ClaimIdle/2, time.Second)
	}

	if w.DueEvery == 0 && w.Retry != nil {
		w.DueEvery = time.Second
	}

	return w
}

func (w Work) report(err error) {
	if w.OnError != nil {
		w.OnError(err)
	}
}

// nextBackoff is the wait after one more failed read: 100 ms, then twice
// the last wait, at most 5 s.
func nextBackoff(last time.Duration) time.Duration {
	return min(max(last*2, 100*time.Millisecond), 5*time.Second)
}

// workPending does the work on the consumer's own pending entries, in ID
// order. An entry that fails stays pending (or is nacked, with Retry); the
// loop moves past it. A failed read goes to OnError and is tried again:
// only the end of ctx stops the loop.
func (g *Group[T]) workPending(ctx context.Context, consumer string, h Handler[T], w Work) {
	after := "0"
	backoff := time.Duration(0)

	for ctx.Err() == nil {
		entries, err := g.Read(ctx, consumer, Read{Pending: true, After: after, Count: w.Count})

		if err != nil {
			if ctx.Err() != nil {
				return
			}

			w.report(fmt.Errorf("ndb: read pending %s/%s: %w", g.s.name, g.name, err))

			backoff = nextBackoff(backoff)

			sleep(ctx, backoff)

			continue
		}

		backoff = 0

		if len(entries) == 0 {
			return
		}

		counts := g.pendingCounts(ctx, consumer, w, entries)
		retry := g.buryCrashed(ctx, w, entries, counts)

		g.handle(ctx, consumer, h, w, retry, counts)

		after = entries[len(entries)-1].ID
	}
}

// buryCrashed sends to the dead letter the entries above MaxDeliveries,
// and gives the others. counts has the delivery counts after the read
// that gave the entries: that read is one more delivery, so an entry above
// the limit had MaxDeliveries deliveries without an ack. The process
// stopped in its handler (a crash): do not run it again.
func (g *Group[T]) buryCrashed(ctx context.Context, w Work, entries []Entry[T], counts map[string]int64) []Entry[T] {
	retry := make([]Entry[T], 0, len(entries))

	for _, e := range entries {
		n := counts[e.ID]

		if w.MaxDeliveries > 0 && n > w.MaxDeliveries && !e.Deleted {
			g.bury(ctx, w, e, n-1, ReasonMaxDeliveries)

			continue
		}

		retry = append(retry, e)
	}

	return retry
}

// workIdle claims the idle entries of the group, poison ones first to the
// dead letter. The nacked entries that still wait are not in it.
func (g *Group[T]) workIdle(ctx context.Context, consumer string, h Handler[T], w Work) {
	idle, err := g.idleRows(ctx, w)

	if err != nil {
		w.report(fmt.Errorf("ndb: pending %s/%s: %w", g.s.name, g.name, err))
	}

	if len(idle) == 0 {
		return
	}

	ids := make([]string, 0, len(idle))
	deliveries := map[string]int64{}

	for _, p := range idle {
		ids = append(ids, p.ID)
		deliveries[p.ID] = p.Deliveries
	}

	claimed, err := g.Claim(ctx, consumer, w.ClaimIdle, ids...)

	if err != nil {
		w.report(fmt.Errorf("ndb: claim %s/%s: %w", g.s.name, g.name, err))

		return
	}

	retry := make([]Entry[T], 0, len(claimed.Entries))
	counts := make(map[string]int64, len(claimed.Entries))

	for _, e := range claimed.Entries {
		n := deliveries[e.ID]

		// A process that stopped before the bury left it pending.
		if w.MaxDeliveries > 0 && n >= w.MaxDeliveries {
			g.bury(ctx, w, e, n, ReasonMaxDeliveries)

			continue
		}

		// The claim is one more delivery.
		counts[e.ID] = n + 1

		retry = append(retry, e)
	}

	g.handle(ctx, consumer, h, w, retry, counts)
}

// idleRows gives up to Count pending entries idle for ClaimIdle. A nacked
// entry that still waits is left out (the claim refuses it), and the list
// pages on past it: so many waiting entries with low IDs do not fill every
// page and hide the idle ones after them. A due nacked entry stays in.
func (g *Group[T]) idleRows(ctx context.Context, w Work) ([]PendingEntry, error) {
	rows := make([]PendingEntry, 0, w.Count)
	start := "-"

	for len(rows) < w.Count && ctx.Err() == nil {
		page, err := g.PendingList(ctx, PendingQuery{Start: start, Idle: w.ClaimIdle, Count: w.Count})

		if err != nil {
			return rows, err
		}

		for _, p := range page {
			waiting := p.RetryAt != nil && p.RetryAt.After(time.Now())

			if !waiting && len(rows) < w.Count {
				rows = append(rows, p)
			}
		}

		if len(page) < w.Count {
			break
		}

		start = "(" + page[len(page)-1].ID
	}

	return rows, nil
}

// workDue claims up to Count nacked entries whose wait ended, from cursor,
// and does the work on them. It gives the next cursor, and more is true
// when the batch was full (more entries can be due now).
func (g *Group[T]) workDue(ctx context.Context, consumer string, h Handler[T], w Work, cursor string) (string, bool) {
	claimed, err := g.ClaimDue(ctx, consumer, cursor, w.Count)

	if err != nil {
		if ctx.Err() == nil {
			w.report(fmt.Errorf("ndb: claim due %s/%s: %w", g.s.name, g.name, err))
		}

		return "0-0", false
	}

	next := claimed.Cursor

	if next == "" {
		next = "0-0"
	}

	more := len(claimed.Entries)+len(claimed.Deleted) >= w.Count || next != "0-0"

	if len(claimed.Entries) == 0 {
		return next, more
	}

	// The claim is one more delivery: the counts are after it.
	counts := g.pendingCounts(ctx, consumer, w, claimed.Entries)
	retry := g.buryCrashed(ctx, w, claimed.Entries, counts)

	g.handle(ctx, consumer, h, w, retry, counts)

	return next, more
}

// pendingCounts gives the delivery counts of the consumer's own pending
// entries. Only a worker with MaxDeliveries or Retry needs them.
func (g *Group[T]) pendingCounts(ctx context.Context, consumer string, w Work, entries []Entry[T]) map[string]int64 {
	if w.MaxDeliveries <= 0 && w.Retry == nil || len(entries) == 0 {
		return nil
	}

	// Other own pending entries can be in the range: the count is the
	// most of a page, not len(entries).
	q := PendingQuery{Start: entries[0].ID, End: entries[len(entries)-1].ID, Count: 10_000, Consumer: consumer}

	rows, err := g.PendingList(ctx, q)

	if err != nil {
		w.report(fmt.Errorf("ndb: pending %s/%s: %w", g.s.name, g.name, err))

		return nil
	}

	counts := make(map[string]int64, len(rows))

	for _, p := range rows {
		counts[p.ID] = p.Deliveries
	}

	return counts
}

// bury sends an entry to the dead letter with reason, acks it, then
// reports it. It runs to the end also when ctx ended: the entry already
// failed.
func (g *Group[T]) bury(ctx context.Context, w Work, e Entry[T], deliveries int64, reason string) {
	ctx = context.WithoutCancel(ctx)

	if w.DeadLetter != "" {
		dead, err := StreamOf[DeadEntry](g.s.db, w.DeadLetter)

		if err == nil {
			doc := DeadEntry{Stream: g.s.name, Group: g.name, ID: e.ID, Deliveries: deliveries, Doc: bareDoc(e.raw), Reason: reason}

			_, err = dead.Append(ctx, doc)
		}

		if err != nil {
			w.report(fmt.Errorf("ndb: dead letter %s: %w", w.DeadLetter, err))

			return
		}
	}

	if _, err := g.Ack(ctx, e.ID); err != nil {
		w.report(fmt.Errorf("ndb: ack %s/%s: %w", g.s.name, g.name, err))

		return
	}

	w.report(fmt.Errorf("%w: %s/%s %s after %d deliveries (%s)", ErrPoison, g.s.name, g.name, e.ID, deliveries, reason))
}

// nack gives a failed entry back to the group with the wait of Retry. It
// goes also when ctx ended: the handler already failed.
func (g *Group[T]) nack(ctx context.Context, consumer string, w Work, id string, deliveries int64) {
	wait := min(max(w.Retry(deliveries), 0), maxRetryWait)

	if _, err := g.Nack(context.WithoutCancel(ctx), consumer, wait, id); err != nil {
		w.report(fmt.Errorf("ndb: nack %s/%s %s: %w", g.s.name, g.name, id, err))
	}
}

// handle runs h on each entry and acks the ones that worked, in one call.
// A deleted entry is acked without h: there is nothing to do. An entry
// past MaxAge goes to the dead letter without h. An entry that does not
// decode into T is a failure without h. counts has the delivery count of
// an entry delivered before (no count is 1). An entry that fails on its
// MaxDeliveries-th delivery goes to the dead letter at once; another one
// that fails is nacked with the wait of Retry (when set). A handler error
// while ctx ends is not a failure: the entry stays pending for the next
// run (no nack), and handle stops.
func (g *Group[T]) handle(ctx context.Context, consumer string, h Handler[T], w Work, entries []Entry[T], counts map[string]int64) {
	done := make([]string, 0, len(entries))

	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}

		if e.Deleted {
			done = append(done, e.ID)

			continue
		}

		n := max(counts[e.ID], 1)

		if w.MaxAge > 0 && !e.Time.IsZero() && time.Since(e.Time) > w.MaxAge {
			g.bury(ctx, w, e, n, ReasonMaxAge)

			continue
		}

		err := e.Err

		if err == nil {
			err = h(ctx, e)

			if err != nil && ctx.Err() != nil {
				break
			}

			if err != nil {
				err = fmt.Errorf("ndb: handler %s/%s %s: %w", g.s.name, g.name, e.ID, err)
			}
		}

		if err == nil {
			done = append(done, e.ID)

			continue
		}

		w.report(err)

		// An entry that got too old in the handler goes to the dead letter
		// now: a retry would wait only to bury it.
		switch {
		case w.MaxDeliveries > 0 && n >= w.MaxDeliveries:
			g.bury(ctx, w, e, n, ReasonMaxDeliveries)
		case w.MaxAge > 0 && !e.Time.IsZero() && time.Since(e.Time) > w.MaxAge:
			g.bury(ctx, w, e, n, ReasonMaxAge)
		case w.Retry != nil:
			g.nack(ctx, consumer, w, e.ID, n)
		}
	}

	if len(done) == 0 {
		return
	}

	// The acks go even when ctx ended: the work on them is done.
	if _, err := g.Ack(context.WithoutCancel(ctx), done...); err != nil {
		w.report(fmt.Errorf("ndb: ack %s/%s: %w", g.s.name, g.name, err))
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)

	defer t.Stop()

	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// bareDoc is the entry without _id and _ts: a dead entry keeps the source
// ID in its own field.
func bareDoc(raw json.RawMessage) json.RawMessage {
	var doc map[string]json.RawMessage

	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}

	delete(doc, "_id")
	delete(doc, "_ts")

	out, err := json.Marshal(doc)

	if err != nil {
		return raw
	}

	return out
}
