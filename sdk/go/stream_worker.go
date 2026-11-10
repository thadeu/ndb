package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Handler does the work on one entry. nil acks it. An error leaves it
// pending: after Work.ClaimIdle, a worker of the group claims it and tries
// again. An error while ctx ends (Work stops) is not a failure: the entry
// stays pending and does not go to the dead letter.
type Handler[T any] func(ctx context.Context, e Entry[T]) error

// Work says how Group.Work runs.
type Work struct {
	// Count is the most entries per read: default 10.
	Count int

	// Block is how long a read waits for new entries: default 5 s.
	Block time.Duration

	// ClaimIdle: the entries pending for at least this long, of any
	// consumer of the group, are claimed and tried again. Default 1 min;
	// a negative value turns the claim off.
	ClaimIdle time.Duration

	// ClaimEvery is how often Work looks for idle entries: default
	// ClaimIdle / 2.
	ClaimEvery time.Duration

	// MaxDeliveries: an entry delivered this many times is a poison
	// message. When its handler fails on that delivery, it goes to
	// DeadLetter (when set) and is acked at once. Zero is no limit.
	MaxDeliveries int64

	// DeadLetter is the stream that gets the poison messages, as a
	// DeadEntry.
	DeadLetter string

	// OnError gets the errors that do not stop Work: a failed read, a
	// handler error, a poison message. Default: none.
	OnError func(error)
}

// DeadEntry is what Work appends to the dead-letter stream.
type DeadEntry struct {
	Stream     string          `ndb:"stream"`
	Group      string          `ndb:"group"`
	ID         string          `ndb:"id"`
	Deliveries int64           `ndb:"deliveries"`
	Doc        json.RawMessage `ndb:"doc"`
}

// ErrPoison is the error OnError gets for a poison message.
var ErrPoison = errors.New("ndb: poison message")

// Work runs a worker of the group as consumer until ctx ends, and then
// returns nil. It:
//
//  1. reads its own pending entries first (the ones it took before a
//     restart and did not ack), and does the work on them;
//  2. reads new entries, waiting up to Block for them;
//  3. acks each entry the handler did without an error;
//  4. every ClaimEvery, claims the entries idle for ClaimIdle, and sends
//     the ones delivered MaxDeliveries times to DeadLetter.
//
// Delivery is at least once: the work on an entry must be safe to repeat.
// Run many workers with different consumer names to share the work. The
// group must exist (Group.Ensure). A failed read, also of a group that
// does not exist, goes to OnError, and Work tries again with a backoff.
//
// An entry whose doc does not decode into T (Entry.Err) does not go to the
// handler: it is a failure of the handler, so it stays pending and, after
// MaxDeliveries, goes to DeadLetter with its raw doc. An own pending entry
// that already had MaxDeliveries deliveries without an ack (it crashed the
// process) goes to DeadLetter at the start, without the handler.
func (g *Group[T]) Work(ctx context.Context, consumer string, h Handler[T], w Work) error {
	w = w.withDefaults()

	g.workPending(ctx, consumer, h, w)

	nextClaim := time.Now().Add(w.ClaimEvery)
	backoff := time.Duration(0)

	for ctx.Err() == nil {
		if w.ClaimIdle > 0 && !time.Now().Before(nextClaim) {
			g.workIdle(ctx, consumer, h, w)

			nextClaim = time.Now().Add(w.ClaimEvery)
		}

		block := min(w.Block, time.Until(nextClaim))

		if w.ClaimIdle <= 0 {
			block = w.Block
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

		g.handle(ctx, h, w, entries, nil)
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
// order. An entry that fails stays pending; the loop moves past it. A
// failed read goes to OnError and is tried again: only the end of ctx
// stops the loop.
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
		retry := make([]Entry[T], 0, len(entries))

		for _, e := range entries {
			n := counts[e.ID]

			// The pending read is one more delivery. An entry above the
			// limit had MaxDeliveries deliveries without an ack: the
			// process stopped in its handler (a crash). Do not run it again.
			if w.MaxDeliveries > 0 && n > w.MaxDeliveries && !e.Deleted {
				g.bury(ctx, w, e, n-1)

				continue
			}

			retry = append(retry, e)
		}

		g.handle(ctx, h, w, retry, counts)

		after = entries[len(entries)-1].ID
	}
}

// workIdle claims the idle entries of the group, poison ones first to the
// dead letter.
func (g *Group[T]) workIdle(ctx context.Context, consumer string, h Handler[T], w Work) {
	idle, err := g.PendingList(ctx, PendingQuery{Idle: w.ClaimIdle, Count: w.Count})

	if err != nil {
		w.report(fmt.Errorf("ndb: pending %s/%s: %w", g.s.name, g.name, err))

		return
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
			g.bury(ctx, w, e, n)

			continue
		}

		// The claim is one more delivery.
		counts[e.ID] = n + 1

		retry = append(retry, e)
	}

	g.handle(ctx, h, w, retry, counts)
}

// pendingCounts gives the delivery counts of the consumer's own pending
// entries. Only a worker with MaxDeliveries needs them.
func (g *Group[T]) pendingCounts(ctx context.Context, consumer string, w Work, entries []Entry[T]) map[string]int64 {
	if w.MaxDeliveries <= 0 || len(entries) == 0 {
		return nil
	}

	rows, err := g.PendingList(ctx, PendingQuery{Start: entries[0].ID, End: entries[len(entries)-1].ID, Count: len(entries), Consumer: consumer})

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

// bury sends a poison entry to the dead letter, acks it, then reports it.
// It runs to the end also when ctx ended: the entry already failed.
func (g *Group[T]) bury(ctx context.Context, w Work, e Entry[T], deliveries int64) {
	ctx = context.WithoutCancel(ctx)

	if w.DeadLetter != "" {
		dead, err := StreamOf[DeadEntry](g.s.db, w.DeadLetter)

		if err == nil {
			_, err = dead.Append(ctx, DeadEntry{Stream: g.s.name, Group: g.name, ID: e.ID, Deliveries: deliveries, Doc: bareDoc(e.raw)})
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

	w.report(fmt.Errorf("%w: %s/%s %s after %d deliveries", ErrPoison, g.s.name, g.name, e.ID, deliveries))
}

// handle runs h on each entry and acks the ones that worked, in one call.
// A deleted entry is acked without h: there is nothing to do. An entry
// that does not decode into T is a failure without h. counts has the
// delivery count of an entry delivered before (no count is 1); an entry
// that fails on its MaxDeliveries-th delivery goes to the dead letter at
// once. A handler error while ctx ends is not a failure: the entry stays
// pending for the next run, and handle stops.
func (g *Group[T]) handle(ctx context.Context, h Handler[T], w Work, entries []Entry[T], counts map[string]int64) {
	done := make([]string, 0, len(entries))

	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}

		if e.Deleted {
			done = append(done, e.ID)

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

		if err != nil {
			w.report(err)

			n := max(counts[e.ID], 1)

			if w.MaxDeliveries > 0 && n >= w.MaxDeliveries {
				g.bury(ctx, w, e, n)
			}

			continue
		}

		done = append(done, e.ID)
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
