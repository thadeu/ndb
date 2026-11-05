package engine

import (
	"context"

	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/query"
)

const (
	// knownLimit bounds the ids a filtered stream remembers. Past it the
	// memory is dropped, and a later delete of a forgotten id is not delivered.
	knownLimit = 1 << 16

	// ackEvery is how many filtered-out events pass before the stored
	// position is moved.
	ackEvery = 256
)

// filterStream delivers only the events whose document matches where (plan
// §10.4).
//
// A put matches when its doc matches. A del has no doc, so it matches by the
// last known doc: the last put of that id that this stream saw. It is
// delivered only if that put matched; an id the stream never saw has no known
// doc, and its del is not delivered. Without where, every del is delivered.
//
// The events left out are never acked by the consumer, so the stream moves
// the stored position over them itself: at every ackEvery events, when it
// goes idle (a heartbeat timeout reaches it as an error), and on Close.
type filterStream struct {
	in  feed.Stream
	q   *query.Query
	ack func(pos string) error

	// known maps an id to "the last put of this id matched".
	known map[string]bool

	// skipped is the last position left out and not stored yet.
	skipped   string
	unflushed int
}

func newFilterStream(in feed.Stream, q *query.Query, ack func(pos string) error) *filterStream {
	return &filterStream{in: in, q: q, ack: ack, known: map[string]bool{}}
}

func (f *filterStream) Next(ctx context.Context) (feed.Event, error) {
	for {
		ev, err := f.in.Next(ctx)
		if err != nil {
			f.flush()

			return feed.Event{}, err
		}

		if f.matches(ev) {
			return ev, nil
		}

		f.skipped = ev.Pos
		f.unflushed++

		if f.unflushed >= ackEvery {
			f.flush()
		}
	}
}

func (f *filterStream) Close() {
	f.flush()
	f.in.Close()
}

// flush stores the position of the last event left out. A failed store is
// not fatal: the consumer sees the events again, which at-least-once allows.
func (f *filterStream) flush() {
	if f.skipped == "" {
		return
	}

	_ = f.ack(f.skipped)

	f.skipped = ""
	f.unflushed = 0
}

func (f *filterStream) matches(ev feed.Event) bool {
	if ev.Op == "del" {
		ok := f.known[ev.ID]

		delete(f.known, ev.ID)

		return ok
	}

	m, err := query.Decode(ev.Doc)
	ok := err == nil && f.q.Match(m)

	if len(f.known) >= knownLimit {
		clear(f.known)
	}

	f.known[ev.ID] = ok

	return ok
}
