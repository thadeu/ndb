package replication

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/wal"
)

// The states of a copy.
const (
	StateStarting  = "starting"
	StateStreaming = "streaming"
	StateResync    = "resync"
	StateDown      = "down"
	StatePromoted  = "promoted"
)

// DatabaseHeader names the database of a request (as in package server).
const DatabaseHeader = "Ndb-Database"

// Options of a copy. The zero value is the default of each.
type Options struct {
	// Silence is how long the stream may send nothing before the copy
	// connects again (default 10 s; the owner sends a heartbeat every
	// second).
	Silence time.Duration

	// Backoff is the first wait before a new connect (default 100 ms); it
	// doubles up to MaxBackoff (default 5 s), with jitter.
	Backoff    time.Duration
	MaxBackoff time.Duration

	// Wait bounds how long a forwarded write waits for its position
	// (default 5 s); after it the answer has Ndb-Stale: true.
	Wait time.Duration

	// Client is the HTTP client to the owner (default: no timeout, as the
	// stream is one long request).
	Client *http.Client
}

// Copy pulls one database of an owner into one local database.
type Copy struct {
	src  Source
	db   *engine.DB
	key  string
	log  *slog.Logger
	opts Options

	mu       sync.Mutex
	st       State
	state    string
	lastErr  string
	fatal    error
	gen      uint64
	last     map[string]uint64
	behind   time.Time
	changed  chan struct{}
	dirty    bool
	saved    time.Time
	progress Progress

	// first gets the result of the first hello (nil, or a fatal error);
	// it is closed after.
	first     chan error
	firstOnce sync.Once

	parent context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

// Progress is the work of a resync.
type Progress struct {
	Collections int   `json:"collections"`
	Done        int   `json:"done"`
	Bytes       int64 `json:"bytes"`
}

// Status is what GET /_replication shows of a copy.
type Status struct {
	Source     string    `json:"source"`
	State      string    `json:"state"`
	Term       uint64    `json:"term"`
	Synced     bool      `json:"synced"`
	LagRecords uint64    `json:"lag_records"`
	LagSeconds float64   `json:"lag_seconds"`
	LastError  string    `json:"last_error,omitempty"`
	Resync     *Progress `json:"resync,omitempty"`
}

// New makes the copy of src into db, the local database. key is the
// fingerprint of the local encryption key.
func New(src Source, db *engine.DB, key string, log *slog.Logger, opts Options) (*Copy, error) {
	if opts.Silence <= 0 {
		opts.Silence = 10 * time.Second
	}

	if opts.Backoff <= 0 {
		opts.Backoff = 100 * time.Millisecond
	}

	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 5 * time.Second
	}

	if opts.Wait <= 0 {
		opts.Wait = 5 * time.Second
	}

	if opts.Client == nil {
		opts.Client = &http.Client{}
	}

	st, _, err := LoadState(db.Dir())
	if err != nil {
		return nil, err
	}

	c := &Copy{src: src, db: db, key: key, log: log.With("database", src.Local), opts: opts, st: st, state: StateStarting, changed: make(chan struct{}), first: make(chan error, 1)}

	if st.Role == RoleOwner && st.Term > 0 {
		c.state = StatePromoted
	}

	return c, nil
}

// Source is the source of the copy.
func (c *Copy) Source() Source {
	return c.src
}

// Promoted tells that this database was promoted: it is an owner now, and
// the copy does not run (NDB_REPLICA_OF must go).
func (c *Copy) Promoted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.state == StatePromoted
}

// Start runs the copy in the background until ctx ends or Stop.
func (c *Copy) Start(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.parent = ctx

	if c.state == StatePromoted {
		c.log.Warn("this database was promoted: it is the owner now; remove it from NDB_REPLICA_OF", "source", c.src.Redacted(), "term", c.st.Term)
		c.report(nil)

		return
	}

	c.startLocked()
}

func (c *Copy) startLocked() {
	ctx, cancel := context.WithCancel(c.parent)

	c.cancel = cancel
	c.done = make(chan struct{})

	go c.run(ctx, c.done)
}

// First waits until the first hello of the owner was checked, up to d.
// It gives the fatal error of that check (another key, a lower term, a
// resync to do), or nil; nil also when the owner did not answer in time.
func (c *Copy) First(d time.Duration) error {
	select {
	case err := <-c.first:
		return err
	case <-time.After(d):
		return nil
	}
}

func (c *Copy) report(err error) {
	c.firstOnce.Do(func() {
		c.first <- err

		close(c.first)
	})
}

// Stop ends the copy and saves its position.
func (c *Copy) Stop() {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel = nil
	c.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	<-done

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dirty {
		c.saveLocked()
	}
}

func (c *Copy) run(ctx context.Context, done chan struct{}) {
	defer close(done)

	wait := c.opts.Backoff

	for ctx.Err() == nil {
		progressed, err := c.session(ctx)

		if ctx.Err() != nil {
			return
		}

		if c.isFatal(err) {
			c.setDown(err)
			c.report(err)
			c.log.Error("replication stopped", "source", c.src.Redacted(), "err", err)

			<-ctx.Done()

			return
		}

		if err != nil {
			c.setDown(err)
			c.log.Warn("replication: owner not reachable, retrying", "source", c.src.Redacted(), "err", err, "in_ms", wait.Milliseconds())
		}

		if progressed {
			wait = c.opts.Backoff
		}

		jitter := time.Duration(rand.Int64N(int64(wait)/2 + 1))

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait/2 + jitter):
		}

		if err != nil && !progressed {
			wait = min(wait*2, c.opts.MaxBackoff)
		}
	}
}

// fatalError is an error that a new connect does not fix.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }

func (e *fatalError) Unwrap() error { return e.err }

func (c *Copy) isFatal(err error) bool {
	var f *fatalError

	return errors.As(err, &f)
}

func (c *Copy) setDown(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.state = StateDown
	c.lastErr = err.Error()

	if c.isFatal(err) {
		c.fatal = err
	}
}

// request is a GET of the owner with the token and the database.
func (c *Copy) request(ctx context.Context, path string, q url.Values) (*http.Response, error) {
	u := c.src.Base + path

	if len(q) > 0 {
		u += "?" + q.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	c.auth(req.Header, false)

	return c.opts.Client.Do(req)
}

// auth sets the token and the database of a request to the owner. keep
// leaves an Authorization that the request already has.
func (c *Copy) auth(h http.Header, keep bool) {
	if c.src.Token != "" && (!keep || h.Get("Authorization") == "") {
		h.Set("Authorization", "Bearer "+c.src.Token)
	}

	h.Set(DatabaseHeader, c.src.Remote)
}

// session reads one stream until it ends. progressed is true when it got
// at least the hello, so the next connect does not wait long.
func (c *Copy) session(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.mu.Lock()
	from := FormatPositions(c.st.Cursors)
	c.mu.Unlock()

	resp, err := c.request(ctx, "/_replication/stream", url.Values{"from": {from}})
	if err != nil {
		return false, err
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusGone {
		gone := strings.Split(resp.Header.Get(GoneHeader), ",")

		return true, c.resync(ctx, gone)
	}

	if resp.StatusCode != http.StatusOK {
		return false, answerError(resp)
	}

	watch := time.AfterFunc(c.opts.Silence, cancel)
	defer watch.Stop()

	rd := bufio.NewReaderSize(resp.Body, 64<<10)
	progressed := false

	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			if ctx.Err() != nil && progressed {
				return true, fmt.Errorf("the owner sent nothing for %s", c.opts.Silence)
			}

			return progressed, err
		}

		watch.Reset(c.opts.Silence)

		var l Line

		if err := json.Unmarshal(line, &l); err != nil {
			return progressed, fmt.Errorf("bad stream line: %w", err)
		}

		if l.Hello != nil {
			if err := c.hello(*l.Hello); err != nil {
				return progressed, err
			}

			progressed = true

			c.report(nil)

			continue
		}

		if !progressed {
			return false, errors.New("the stream did not start with hello")
		}

		if len(l.Gone) > 0 {
			return true, c.resync(ctx, l.Gone)
		}

		if err := c.line(ctx, l); err != nil {
			return true, err
		}
	}
}

// answerError reads the error of an answer of the owner.
func answerError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	var e struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal(body, &e) == nil && e.Error.Code != "" {
		return fmt.Errorf("owner answered %d %s: %s", resp.StatusCode, e.Error.Code, e.Error.Message)
	}

	return fmt.Errorf("owner answered %d", resp.StatusCode)
}

// hello checks the owner: the same key, a term not lower than the one
// the copy knows, and, for a database that was not a copy, a history that
// is a prefix of the owner's.
func (c *Copy) hello(h Hello) error {
	if h.Key != c.key {
		return &fatalError{fmt.Errorf("the encryption key of the owner (fingerprint %s) is not the key of this copy (%s): set NDB_ENCRYPTION_KEY to the key of the owner", h.Key, c.key)}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if h.Term < c.st.Term {
		return &fatalError{fmt.Errorf("the owner has term %d, lower than term %d that this database knows: it is an old owner; point NDB_REPLICA_OF to the new owner", h.Term, c.st.Term)}
	}

	if c.st.Role != RoleCopy {
		cursors, err := c.adopt(h)
		if err != nil {
			return err
		}

		c.st = State{Role: RoleCopy, Cursors: cursors}
	}

	c.st.Term = h.Term
	c.st.Source = c.src.Redacted()
	c.state = StateStreaming
	c.lastErr = ""

	c.saveLocked()

	return nil
}

// adopt gives the first positions of a database that was not a copy:
// none when it is empty; for an old owner, the positions of the promote
// when its last tx of each collection is the one the new owner had.
func (c *Copy) adopt(h Hello) (map[string]uint64, error) {
	cursors := map[string]uint64{}

	for _, def := range c.db.EngineDefs() {
		last := c.db.LastTx(def.Name)

		if last == 0 {
			continue
		}

		p, ok := h.Promoted[def.Name]

		if !ok || p.From != last {
			return nil, &fatalError{fmt.Errorf("%w (collection %s, last tx %d): run \"ndb replication resync\" to copy the owner again", ErrResync, def.Name, last)}
		}

		cursors[def.Name] = p.At
	}

	return cursors, nil
}

// line applies one line of the stream after the hello.
func (c *Copy) line(ctx context.Context, l Line) error {
	switch {
	case l.Catalog != nil:
		return c.applyCatalog(*l.Catalog, true)
	case l.Record != nil:
		return c.applyRecord(ctx, *l.Record)
	case l.Heartbeat != nil:
		c.heartbeat(*l.Heartbeat)
	}

	return nil
}

// applyCatalog makes the definitions of the owner the ones of the copy.
// With all, a local collection that the owner does not have is dropped.
func (c *Copy) applyCatalog(cat Catalog, all bool) error {
	names := map[string]bool{}

	for _, def := range cat.Defs {
		names[def.Name] = true

		if err := c.db.ApplyDef(def); err != nil {
			return fmt.Errorf("collection %s: %w", def.Name, err)
		}
	}

	if all {
		for _, def := range c.db.EngineDefs() {
			if names[def.Name] {
				continue
			}

			if err := c.db.Drop(def.Name, def.Name); err != nil && !errors.Is(err, catalog.ErrUnknown) {
				return fmt.Errorf("drop %s: %w", def.Name, err)
			}

			c.mu.Lock()
			delete(c.st.Cursors, def.Name)
			c.dirty = true
			c.mu.Unlock()
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if all {
		c.gen = cat.Gen
	}

	if c.dirty {
		c.saveLocked()
	}

	c.notifyLocked()

	return nil
}

// applyRecord writes one record and moves the position of its collection.
func (c *Copy) applyRecord(ctx context.Context, rec Record) error {
	c.mu.Lock()
	cur := c.st.Cursors[rec.C]
	c.mu.Unlock()

	if rec.Tx <= cur {
		return nil
	}

	if len(rec.Ops) > 0 {
		ops, err := c.walOps(ctx, rec.Ops)
		if err != nil {
			return err
		}

		if err := c.db.ApplyOps(ctx, rec.C, ops); err != nil {
			return fmt.Errorf("apply %s tx %d: %w", rec.C, rec.Tx, err)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.st.Cursors == nil {
		c.st.Cursors = map[string]uint64{}
	}

	c.st.Cursors[rec.C] = rec.Tx
	c.dirty = true

	trunc := len(rec.Ops) == 1 && rec.Ops[0].Op == string(wal.Trunc)

	if trunc || time.Since(c.saved) > 200*time.Millisecond {
		c.saveLocked()
	}

	c.notifyLocked()

	return nil
}

// walOps turns the ops of a record into WAL ops, and copies the blobs of
// the documents that this database does not have.
func (c *Copy) walOps(ctx context.Context, in []Op) ([]wal.Op, error) {
	out := make([]wal.Op, len(in))

	for i, op := range in {
		out[i] = wal.Op{Kind: wal.OpKind(op.Op), ID: op.ID, V: op.V, Doc: op.Doc}

		if op.Op != string(wal.Put) {
			continue
		}

		for _, ref := range blob.Refs(op.Doc) {
			if err := c.copyBlob(ctx, ref); err != nil {
				return nil, err
			}
		}
	}

	return out, nil
}

func (c *Copy) copyBlob(ctx context.Context, ref string) error {
	store := c.db.Blobs()

	hash, err := blob.ParseHash(ref)
	if err != nil || store == nil || store.Has(hash) {
		return nil
	}

	resp, err := c.request(ctx, "/_blobs/"+url.PathEscape(ref), nil)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("blob %s: %w", ref, answerError(resp))
	}

	got, err := store.Put(resp.Body, "", 0)
	if err != nil {
		return fmt.Errorf("blob %s: %w", ref, err)
	}

	if got.Hash() != hash {
		return fmt.Errorf("blob %s: the owner sent another content (%s)", ref, got.Blob)
	}

	return nil
}

// heartbeat keeps the last tx of the owner, for the lag, and marks the
// copy synced once it has every record.
func (c *Copy) heartbeat(hb Heartbeat) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.last = hb.Last

	if c.lagLocked() > 0 {
		if c.behind.IsZero() {
			c.behind = time.Now()
		}

		return
	}

	c.behind = time.Time{}

	if !c.st.Synced {
		c.st.Synced = true
		c.dirty = true

		c.log.Info("replication caught up: reads are local", "source", c.src.Redacted())
	}

	if c.dirty {
		c.saveLocked()
	}
}

func (c *Copy) lagLocked() uint64 {
	var lag uint64

	for name, last := range c.last {
		if cur := c.st.Cursors[name]; last > cur {
			lag += last - cur
		}
	}

	return lag
}

// resync takes a snapshot of each collection of names, then the stream
// goes on from the tx of each snapshot.
func (c *Copy) resync(ctx context.Context, names []string) error {
	c.mu.Lock()
	c.state = StateResync
	c.progress = Progress{Collections: len(names)}
	c.mu.Unlock()

	for _, name := range names {
		if name = strings.TrimSpace(name); name == "" {
			continue
		}

		if err := c.snapshot(ctx, name); err != nil {
			return fmt.Errorf("resync %s: %w", name, err)
		}

		c.mu.Lock()
		c.progress.Done++
		c.mu.Unlock()
	}

	c.log.Info("replication resync done", "collections", len(names))

	return nil
}

// countReader counts the bytes of a resync.
type countReader struct {
	r io.Reader
	c *Copy
}

func (cr countReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)

	cr.c.mu.Lock()
	cr.c.progress.Bytes += int64(n)
	cr.c.mu.Unlock()

	return n, err
}

func (c *Copy) snapshot(ctx context.Context, name string) error {
	resp, err := c.request(ctx, "/_replication/snapshot", url.Values{"collection": {name}})
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return answerError(resp)
	}

	if err := c.db.Truncate(name, name); err != nil && !errors.Is(err, catalog.ErrUnknown) {
		return err
	}

	rd := bufio.NewReaderSize(countReader{r: resp.Body, c: c}, 64<<10)

	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("the snapshot ended before its end line: %w", err)
		}

		var l Line

		if err := json.Unmarshal(bytes.TrimSpace(line), &l); err != nil {
			return fmt.Errorf("bad snapshot line: %w", err)
		}

		switch {
		case l.Catalog != nil:
			if err := c.applyCatalog(*l.Catalog, false); err != nil {
				return err
			}
		case l.Record != nil:
			ops, err := c.walOps(ctx, l.Record.Ops)
			if err != nil {
				return err
			}

			if err := c.db.ApplyOps(ctx, name, ops); err != nil {
				return err
			}
		case l.End != nil:
			c.mu.Lock()
			defer c.mu.Unlock()

			if c.st.Cursors == nil {
				c.st.Cursors = map[string]uint64{}
			}

			c.st.Cursors[name] = l.End.Tx

			c.saveLocked()
			c.notifyLocked()

			return nil
		}
	}
}

func (c *Copy) saveLocked() {
	if c.st.Role == "" {
		c.st.Role = RoleCopy
	}

	if err := SaveState(c.db.Dir(), c.st); err != nil {
		c.log.Error("replication: saving the position failed", "err", err)

		return
	}

	c.dirty = false
	c.saved = time.Now()
}

func (c *Copy) notifyLocked() {
	close(c.changed)

	c.changed = make(chan struct{})
}

// Synced tells that the copy caught up once: its reads are local.
func (c *Copy) Synced() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.st.Synced
}

// WaitFor waits until the copy has applied every position of want (a
// collection tx, or the catalog version as CatalogKey), until ctx ends.
func (c *Copy) WaitFor(ctx context.Context, want map[string]uint64) bool {
	for {
		c.mu.Lock()

		ok := true

		for name, tx := range want {
			if name == CatalogKey {
				ok = ok && c.gen >= tx
			} else {
				ok = ok && c.st.Cursors[name] >= tx
			}
		}

		ch := c.changed
		c.mu.Unlock()

		if ok {
			return true
		}

		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}

// Status is the state of the copy for GET /_replication.
func (c *Copy) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()

	st := Status{Source: c.src.Redacted(), State: c.state, Term: c.st.Term, Synced: c.st.Synced, LagRecords: c.lagLocked(), LastError: c.lastErr}

	if !c.behind.IsZero() && st.LagRecords > 0 {
		st.LagSeconds = time.Since(c.behind).Seconds()
	}

	if c.state == StateResync {
		p := c.progress
		st.Resync = &p
	}

	return st
}

// Promote makes this database the owner: the copy stops for good, and
// the term goes up by one. It keeps the positions of the promote, so the
// old owner can come back as a copy of this one.
func (c *Copy) Promote() (uint64, error) {
	c.Stop()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == StatePromoted {
		return c.st.Term, nil
	}

	promoted := map[string]Promoted{}

	for _, def := range c.db.EngineDefs() {
		promoted[def.Name] = Promoted{From: c.st.Cursors[def.Name], At: c.db.LastTx(def.Name)}
	}

	st := State{Role: RoleOwner, Term: c.st.Term + 1, Source: c.st.Source, Promoted: promoted}

	if err := SaveState(c.db.Dir(), st); err != nil {
		return 0, err
	}

	c.st = st
	c.state = StatePromoted
	c.lastErr = ""

	c.notifyLocked()

	c.log.Warn("replication promoted: this database is the owner now; remove it from NDB_REPLICA_OF before the next start", "term", st.Term)

	return st.Term, nil
}

// Resync drops every collection of the local database and its position,
// then copies the owner again from the start.
func (c *Copy) Resync() error {
	c.Stop()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.state == StatePromoted {
		return errors.New("this database was promoted: it is an owner")
	}

	for _, def := range c.db.EngineDefs() {
		if err := c.db.Drop(def.Name, def.Name); err != nil && !errors.Is(err, catalog.ErrUnknown) {
			return err
		}
	}

	if err := RemoveState(c.db.Dir()); err != nil {
		return err
	}

	c.st = State{}
	c.state = StateStarting
	c.lastErr = ""
	c.fatal = nil

	c.log.Info("replication resync started: the local data of this database was dropped")

	if c.parent != nil {
		c.startLocked()
	}

	return nil
}
