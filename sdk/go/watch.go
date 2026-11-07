package ndb

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
)

// Op is the kind of a change event.
type Op string

const (
	OpPut Op = "put"
	OpDel Op = "del"

	// OpTruncate: the collection was truncated; every document before this
	// event is gone. ID is empty.
	OpTruncate Op = "truncate"
)

// Event is one change of a collection. Doc is the zero value for OpDel and
// OpTruncate.
// Pos is the position token that Ack sends back.
type Event[T any] struct {
	Consumer string
	Pos      string
	Op       Op
	ID       string
	V        uint64
	Doc      T
}

// WatchOption changes a Watch.
type WatchOption func(*watchBody)

// FromNow makes a new consumer start at the end of the collection instead of
// the beginning. It only matters the first time a consumer name connects.
func FromNow() WatchOption {
	return func(b *watchBody) {
		b.From = "now"
	}
}

// Limit ends the stream after n events. Zero (the default) means no limit.
func Limit(n int) WatchOption {
	return func(b *watchBody) {
		b.Limit = n
	}
}

// WatchQuery delivers only the events whose document matches the filters
// (ANDed, as in Query). A delete event has no document: it is delivered only
// when the same stream saw an earlier put of that id that matched. The stored
// position of the consumer still moves over the events that are left out.
func WatchQuery(filters ...Filter) WatchOption {
	return func(b *watchBody) {
		b.Query = queryOf(filters)
	}
}

type watchBody struct {
	Consumer string         `json:"consumer"`
	From     string         `json:"from,omitempty"`
	Limit    int            `json:"limit,omitempty"`
	Query    map[string]any `json:"query,omitempty"`
}

type ackBody struct {
	Consumer string `json:"consumer"`
	Pos      string `json:"pos"`
}

type watchLine struct {
	Pos       string          `json:"pos"`
	Op        Op              `json:"op"`
	ID        string          `json:"_id"`
	V         uint64          `json:"_v"`
	Doc       json.RawMessage `json:"doc"`
	Heartbeat bool            `json:"heartbeat"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Watch streams the changes of the collection for a named consumer: first
// the ones after its last Ack, then the new ones as they happen. Delivery is
// at least once: after a restart an event can come again, so the work on it
// must be safe to repeat. Call Ack after the work on an event is done.
//
// The loop ends with an error when the connection fails or when the daemon
// ends the stream with ErrWatchLagged (the consumer was too slow). In both
// cases, call Watch again: it continues after the last Ack. A consumer whose
// position is older than the WAL the daemon kept (wal_retention) gets
// ErrHistoryTruncated and has to start over.
func (c *Coll[T]) Watch(ctx context.Context, consumer string, opts ...WatchOption) iter.Seq2[Event[T], error] {
	return func(yield func(Event[T], error) bool) {
		var zero Event[T]

		body := watchBody{Consumer: consumer}

		for _, opt := range opts {
			opt(&body)
		}

		raw, err := json.Marshal(body)
		if err != nil {
			yield(zero, err)

			return
		}

		resp, err := c.stream(ctx, "_watch", raw)
		if err != nil {
			yield(zero, err)

			return
		}

		defer resp.Body.Close()

		for line, err := range ndjsonRows(resp.Body) {
			if err != nil {
				yield(zero, err)

				return
			}

			ev, skip, err := c.event(consumer, line)

			if skip {
				continue
			}

			if !yield(ev, err) || err != nil {
				return
			}
		}
	}
}

// event decodes one stream line. skip is true for a heartbeat.
func (c *Coll[T]) event(consumer string, line json.RawMessage) (Event[T], bool, error) {
	var zero Event[T]

	var l watchLine

	if err := json.Unmarshal(line, &l); err != nil {
		return zero, false, fmt.Errorf("ndb: watch line: %w", err)
	}

	if l.Heartbeat {
		return zero, true, nil
	}

	if l.Error != nil {
		return zero, false, &Error{Code: l.Error.Code, Message: l.Error.Message}
	}

	ev := Event[T]{Consumer: consumer, Pos: l.Pos, Op: l.Op, ID: l.ID, V: l.V}

	if len(l.Doc) > 0 {
		doc, err := c.decode(l.Doc)
		if err != nil {
			return zero, false, err
		}

		ev.Doc = doc
	}

	return ev, false, nil
}

// Ack stores the position of ev for its consumer: the next Watch with that
// consumer name starts after ev. An Ack older than the stored one is ignored.
func (c *Coll[T]) Ack(ctx context.Context, ev Event[T]) error {
	raw, err := json.Marshal(ackBody{Consumer: ev.Consumer, Pos: ev.Pos})
	if err != nil {
		return err
	}

	return c.call(ctx, http.MethodPost, "_ack", raw, nil)
}
