// Package locks holds the pessimistic locks of documents: a lease on one
// _id, with a token and an expiry. Only the holder of the token writes the
// document until the lease is released or expires.
//
// The locks live in memory only. A daemon that restarts holds none: its
// clients lost their connections too, and a token of before matches no
// lock, so a late write with it is refused.
package locks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"
)

// ErrExpired is a token that holds no lock: the lease expired, was released,
// or belongs to another document. A write with it is refused.
var ErrExpired error = expired{}

type expired struct{}

func (expired) Error() string {
	return "lock_expired: the token holds no lock on this document (expired or released)"
}

// Code is the API error code.
func (expired) Code() string {
	return "lock_expired"
}

// Busy is a document locked by another holder. Wait blocks until that
// lease ends.
type Busy struct {
	ID    string
	done  <-chan struct{}
	until time.Time
}

func (b *Busy) Error() string {
	return "locked: " + b.ID + " is locked"
}

// Code is the API error code.
func (b *Busy) Code() string {
	return "locked"
}

// Wait returns nil when the lease was released or expired, or the error of
// ctx.
func (b *Busy) Wait(ctx context.Context) error {
	timer := time.NewTimer(time.Until(b.until))
	defer timer.Stop()

	select {
	case <-b.done:
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Lease is a held lock.
type Lease struct {
	Token   string
	Expires time.Time
}

type held struct {
	token   string
	expires time.Time
	done    chan struct{}
}

// Table is the locks of one daemon, by collection and _id.
type Table struct {
	mu   sync.Mutex
	held map[string]*held
	n    atomic.Int64
	now  func() time.Time
}

func New() *Table {
	return &Table{held: map[string]*held{}, now: time.Now}
}

// Any tells if the table holds a lock (expired ones count until they are
// seen). The write path asks it first, so a daemon with no lock pays one
// atomic load per write.
func (t *Table) Any() bool {
	return t != nil && t.n.Load() > 0
}

// Scope is the locks of one collection. The zero Scope holds none.
type Scope struct {
	t    *Table
	name string
}

func (t *Table) Scope(collection string) Scope {
	return Scope{t: t, name: collection}
}

func (s Scope) key(id string) string {
	return s.name + "\x00" + id
}

// Any tells if any collection holds a lock.
func (s Scope) Any() bool {
	return s.t.Any()
}

// TryAcquire locks id for ttl, or returns *Busy.
func (s Scope) TryAcquire(id string, ttl time.Duration) (Lease, error) {
	t := s.t

	if t == nil {
		return Lease{}, ErrExpired
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if cur := t.active(s.key(id)); cur != nil {
		return Lease{}, &Busy{ID: id, done: cur.done, until: cur.expires}
	}

	h := &held{token: newToken(), expires: t.now().Add(ttl), done: make(chan struct{})}

	t.held[s.key(id)] = h
	t.n.Add(1)

	return Lease{Token: h.token, Expires: h.expires}, nil
}

// Release ends the lease of token on id. A token that holds no lock on id
// is ErrExpired.
func (s Scope) Release(id, token string) error {
	t := s.t

	if t == nil {
		return ErrExpired
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.active(s.key(id))
	if cur == nil || cur.token != token {
		return ErrExpired
	}

	t.drop(s.key(id), cur)

	return nil
}

// Check tells if a write of id may go on. Without a token it may when no
// one holds id (*Busy otherwise). With a token it may only when the token
// holds id (ErrExpired otherwise): a holder that lost its lease must not
// write as if it still had it.
func (s Scope) Check(id, token string) error {
	if s.t == nil {
		return nil
	}

	t := s.t

	t.mu.Lock()
	defer t.mu.Unlock()

	cur := t.active(s.key(id))

	switch {
	case token != "" && (cur == nil || cur.token != token):
		return ErrExpired
	case token == "" && cur != nil:
		return &Busy{ID: id, done: cur.done, until: cur.expires}
	}

	return nil
}

// active is the live lock of key; an expired one is removed. The caller
// holds mu.
func (t *Table) active(key string) *held {
	cur := t.held[key]
	if cur == nil {
		return nil
	}

	if !t.now().Before(cur.expires) {
		t.drop(key, cur)

		return nil
	}

	return cur
}

func (t *Table) drop(key string, h *held) {
	delete(t.held, key)
	t.n.Add(-1)
	close(h.done)
}

func newToken() string {
	var b [16]byte

	_, _ = rand.Read(b[:])

	return hex.EncodeToString(b[:])
}

type tokenKey struct{}

// WithToken marks the writes of ctx as writes of the holder of token.
func WithToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}

	return context.WithValue(ctx, tokenKey{}, token)
}

// TokenOf is the token of WithToken, or "".
func TokenOf(ctx context.Context) string {
	token, _ := ctx.Value(tokenKey{}).(string)

	return token
}
