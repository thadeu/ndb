// Package feed is the change feed of a collection: the events a consumer
// reads with watch, and the hub that fans live commits out to the watchers
// without ever blocking the writer (plan §10.4).
package feed

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
)

// Event is one change. Pos is an opaque token: the consumer sends it back
// in ack. Doc is absent for a del. A truncate event (op "truncate") has no
// id: every document before it is gone.
type Event struct {
	Pos string          `json:"pos"`
	Op  string          `json:"op"`
	ID  string          `json:"_id,omitempty"`
	V   uint64          `json:"_v,omitempty"`
	Doc json.RawMessage `json:"doc,omitempty"`

	// Expired is set on the del of a document that expired (TTL sweeper).
	Expired bool `json:"_expired,omitempty"`
}

// Source is the feed of one collection. Engine mode positions are WAL
// positions; direct mode positions are file offsets.
type Source interface {
	// Now is the stored position that means "the current end".
	Now() string

	// Open starts a stream after the stored position start ("" = the
	// beginning of the history).
	Open(start string) (Stream, error)

	// Merge returns the stored position after an ack of the event token
	// ack. It never moves back: an older ack returns old.
	Merge(old, ack string) (string, error)
}

// Stream yields the history after a position, then the live changes.
type Stream interface {
	// Next blocks until the next event, or ctx ends. ErrLagged means the
	// stream fell behind and ended.
	Next(ctx context.Context) (Event, error)
	Close()
}

// Error is a feed error with a stable API code.
type Error struct {
	code string
	msg  string
}

func (e *Error) Error() string {
	return e.code + ": " + e.msg
}

func (e *Error) Code() string {
	return e.code
}

var (
	ErrLagged = &Error{code: "watch_lagged", msg: "the consumer is too slow; reconnect, the stream resumes from the last ack"}
	ErrBadPos = &Error{code: "invalid_position", msg: "the position token is not valid for this collection"}

	// ErrHistoryTruncated: the WAL files that hold the requested history
	// were deleted (wal_retention). The consumer cannot resume from there.
	ErrHistoryTruncated = &Error{code: "history_truncated", msg: "the history before this position was deleted by wal_retention; start from now or rebuild from find"}
)

// Hub fans values out to subscribers. A subscriber has a bounded buffer:
// when it is full, the hub drops the subscriber (it is lagged) and goes on.
// The zero Hub is ready.
type Hub[T any] struct {
	mu   sync.Mutex
	subs map[*Sub[T]]struct{}
}

// Sub is one subscriber. C is closed when the subscriber ends: by Close, or
// because it lagged (see Lagged).
type Sub[T any] struct {
	C chan T

	hub    *Hub[T]
	lagged atomic.Bool
}

// Subscribe adds a subscriber. snap, if set, runs under the hub lock: no
// Publish runs at the same time, so it sees a state that is consistent with
// the values the subscriber will get.
func (h *Hub[T]) Subscribe(buf int, snap func()) *Sub[T] {
	h.mu.Lock()
	defer h.mu.Unlock()

	if snap != nil {
		snap()
	}

	s := &Sub[T]{C: make(chan T, buf), hub: h}

	if h.subs == nil {
		h.subs = map[*Sub[T]]struct{}{}
	}

	h.subs[s] = struct{}{}

	return s
}

// Publish gives v to every subscriber without blocking. before, if set,
// runs under the lock first.
func (h *Hub[T]) Publish(v T, before func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if before != nil {
		before()
	}

	for s := range h.subs {
		select {
		case s.C <- v:
		default:
			s.lagged.Store(true)

			delete(h.subs, s)
			close(s.C)
		}
	}
}

// CloseAll ends every subscriber, as if each one called Close: a stream
// reads io.EOF and the client reconnects from its last ack.
func (h *Hub[T]) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()

	for s := range h.subs {
		delete(h.subs, s)
		close(s.C)
	}
}

// Len is the number of subscribers.
func (h *Hub[T]) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.subs)
}

// Lagged reports that the hub dropped the subscriber because its buffer
// was full.
func (s *Sub[T]) Lagged() bool {
	return s.lagged.Load()
}

// Close removes the subscriber. It is safe to call more than once.
func (s *Sub[T]) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()

	if _, ok := s.hub.subs[s]; !ok {
		return
	}

	delete(s.hub.subs, s)
	close(s.C)
}
