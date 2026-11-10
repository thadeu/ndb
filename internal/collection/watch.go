package collection

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/wal"
)

const defaultWatchBuffer = 4096

// nowOps is the op count of the "now" position: past any real record.
const nowOps = 1 << 30

// engineFeed is the change feed of an engine-mode collection. A position is
// "e<tx>.<n>": the first n ops of tx are delivered. The WAL holds the history
// (files are deleted only by TruncateWAL, see waltrunc.go), the writer's
// Applied hook feeds the live part.
type engineFeed struct {
	fs      fs.FS
	dir     string
	maxLine int
	buf     int

	hub feed.Hub[wal.Record]

	// base is the tx that the checkpoint covers at open. A WAL that holds no
	// record is then not "whole history": what came before base is only in
	// the segments (a restore from a snapshot starts like this).
	base uint64

	// mu guards pins and serializes WAL deletion with the start of a
	// stream. pins holds the first tx that each open WAL read still needs.
	mu      sync.Mutex
	pins    map[int]uint64
	nextPin int

	// last is the last tx that Applied published; guarded by the hub lock.
	last uint64

	// trunc is the tx of the truncate marker that starts the WAL (0 when
	// there is none). Set at open, before any stream.
	trunc uint64
}

type enginePos struct {
	tx uint64
	n  int
}

func (p enginePos) token() string {
	return fmt.Sprintf("e%d.%d", p.tx, p.n)
}

func (p enginePos) less(o enginePos) bool {
	return p.tx < o.tx || (p.tx == o.tx && p.n < o.n)
}

func parseEnginePos(s string) (enginePos, error) {
	if s == "" {
		return enginePos{}, nil
	}

	var p enginePos

	rest, ok := bytes.CutPrefix([]byte(s), []byte("e"))
	txs, ns, found := bytes.Cut(rest, []byte("."))

	if !ok || !found {
		return p, feed.ErrBadPos
	}

	tx, err1 := strconv.ParseUint(string(txs), 10, 64)
	n, err2 := strconv.Atoi(string(ns))

	if err1 != nil || err2 != nil || n < 0 {
		return p, feed.ErrBadPos
	}

	return enginePos{tx: tx, n: n}, nil
}

// publish runs in the writer goroutine, after the record is durable.
func (f *engineFeed) publish(rec wal.Record) {
	f.hub.Publish(rec, func() { f.last = rec.Tx })
}

// LastTx is the last tx that the collection committed and published.
func (c *Collection) LastTx() uint64 {
	var last uint64

	c.feed.hub.Subscribe(1, func() { last = c.feed.last }).Close()

	return last
}

// Feed is the change feed of the collection.
func (c *Collection) Feed() feed.Source {
	return c.feed
}

func (f *engineFeed) Now() string {
	var last uint64

	f.hub.Subscribe(1, func() { last = f.last }).Close()

	return enginePos{tx: last, n: nowOps}.token()
}

func (f *engineFeed) Merge(old, ack string) (string, error) {
	a, err := parseEnginePos(ack)

	if err != nil || ack == "" {
		return "", feed.ErrBadPos
	}

	// A consumer acks an event it has read, so a position after the last
	// commit is a bad token: it would skip the changes up to it.
	var last uint64

	f.hub.Subscribe(1, func() { last = f.last }).Close()

	if a.tx > last {
		return "", errf("invalid_position", "the position e%d is after the last change of the collection (tx %d)", a.tx, last)
	}

	o, err := parseEnginePos(old)
	if err != nil {
		return "", err
	}

	if old != "" && !o.less(a) {
		return old, nil
	}

	return ack, nil
}

// Open subscribes first and reads the WAL second, up to the last tx that was
// published at subscribe time. Every later commit is in the subscription, so
// the two parts have no gap and no overlap.
func (f *engineFeed) Open(start string) (feed.Stream, error) {
	from, err := parseEnginePos(start)
	if err != nil {
		return nil, err
	}

	// Every change before a truncate is gone, so a consumer from before it
	// starts at the marker: one truncate event, then the new changes.
	if f.trunc > 0 && from.tx < f.trunc {
		from = enginePos{tx: f.trunc - 1, n: nowOps}
	}

	unpin, err := f.pinHistory(from)
	if err != nil {
		return nil, err
	}

	var upTo uint64

	sub := f.hub.Subscribe(f.buf, func() { upTo = f.last })

	s := &engineStream{f: f, sub: sub, from: from, upTo: upTo, unpin: unpin}

	if upTo > 0 && (from.tx < upTo || (from.tx == upTo && from.n < nowOps)) {
		s.wal = &walReader{fs: f.fs, dir: f.dir, maxLine: f.maxLine, fromTx: from.tx}
	}

	if s.wal == nil {
		s.release()
	}

	return s, nil
}

type engineStream struct {
	f    *engineFeed
	sub  *feed.Sub[wal.Record]
	from enginePos
	upTo uint64

	wal   *walReader
	queue []feed.Event

	// unpin releases the history pin; nil once released.
	unpin func()
}

// release drops the pin: the stream no longer reads the WAL files.
func (s *engineStream) release() {
	if s.unpin != nil {
		s.unpin()

		s.unpin = nil
	}
}

func (s *engineStream) Close() {
	s.sub.Close()

	if s.wal != nil {
		s.wal.close()
	}

	s.release()
}

func (s *engineStream) Next(ctx context.Context) (feed.Event, error) {
	for len(s.queue) == 0 {
		if err := s.fill(ctx); err != nil {
			return feed.Event{}, err
		}
	}

	ev := s.queue[0]
	s.queue = s.queue[1:]

	return ev, nil
}

// fill queues the events of the next record: from the WAL until upTo, then
// from the subscription.
func (s *engineStream) fill(ctx context.Context) error {
	if s.wal != nil {
		rec, err := s.wal.next()
		if err != nil {
			return fmt.Errorf("watch: wal read before tx %d: %w", s.upTo, err)
		}

		if rec.Tx >= s.upTo {
			s.wal.close()

			s.wal = nil

			s.release()
		}

		s.queue = s.eventsOf(rec)

		return nil
	}

	select {
	case rec, ok := <-s.sub.C:
		if !ok {
			return s.ended()
		}

		s.queue = s.eventsOf(rec)

		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *engineStream) ended() error {
	if s.sub.Lagged() {
		return feed.ErrLagged
	}

	return io.EOF
}

// eventsOf turns the ops of rec that are after the start position into events.
func (s *engineStream) eventsOf(rec wal.Record) []feed.Event {
	var out []feed.Event

	for i, op := range rec.Ops {
		if (enginePos{tx: rec.Tx, n: i + 1}).less(enginePos{tx: s.from.tx, n: s.from.n + 1}) {
			continue
		}

		out = append(out, feed.Event{
			Pos: enginePos{tx: rec.Tx, n: i + 1}.token(),
			Op:  string(op.Kind),
			ID:  op.ID,
			V:   op.V,
			Doc: op.Doc,

			Expired: op.Exp,
		})
	}

	return out
}

// walReader reads the records with tx >= fromTx, segment after segment.
type walReader struct {
	fs      fs.FS
	dir     string
	maxLine int
	fromTx  uint64

	started bool
	paths   []string
	f       fs.File
	lr      *wal.LineReader
}

func (w *walReader) close() {
	if w.f != nil {
		w.f.Close()

		w.f = nil
	}
}

// next returns the next record, or io.EOF at the end of the last segment.
func (w *walReader) next() (wal.Record, error) {
	if !w.started {
		if err := w.start(); err != nil {
			return wal.Record{}, err
		}
	}

	for {
		if w.lr == nil {
			if len(w.paths) == 0 {
				return wal.Record{}, io.EOF
			}

			if err := w.openSegment(); err != nil {
				return wal.Record{}, err
			}
		}

		line, _, err := w.lr.Next()

		if errors.Is(err, io.EOF) || errors.Is(err, wal.ErrPartialLine) {
			w.close()

			w.lr = nil

			continue
		}

		if err != nil {
			return wal.Record{}, err
		}

		if tx, ok := txOf(line); ok && tx < w.fromTx {
			continue
		}

		return wal.Decode(line)
	}
}

// start lists the segments and drops the ones that end before fromTx: the
// last segment whose first tx is <= fromTx, and all after it.
func (w *walReader) start() error {
	w.started = true

	paths, err := filepath.Glob(filepath.Join(w.dir, "*.ndjson"))
	if err != nil {
		return err
	}

	first := 0

	for i, p := range paths {
		tx, ok := w.firstTx(p)

		if ok && tx <= w.fromTx {
			first = i
		}
	}

	w.paths = paths[first:]

	return nil
}

func (w *walReader) firstTx(path string) (uint64, bool) {
	f, err := w.fs.Open(path)
	if err != nil {
		return 0, false
	}

	defer f.Close()

	line, _, err := wal.NewLineReader(io.NewSectionReader(f, 0, 1<<62), 0, w.maxLine).Next()
	if err != nil {
		return 0, false
	}

	return txOf(line)
}

func (w *walReader) openSegment() error {
	f, err := w.fs.Open(w.paths[0])
	if err != nil {
		return err
	}

	w.paths = w.paths[1:]
	w.f = f
	w.lr = wal.NewLineReader(io.NewSectionReader(f, 0, 1<<62), 0, w.maxLine)

	return nil
}

// txOf reads the tx of a WAL line without decoding it: a record starts with
// `{"_tx":<n>,`.
func txOf(line []byte) (uint64, bool) {
	rest, ok := bytes.CutPrefix(line, []byte(`{"_tx":`))
	if !ok {
		return 0, false
	}

	end := bytes.IndexByte(rest, ',')
	if end < 0 {
		return 0, false
	}

	tx, err := strconv.ParseUint(string(rest[:end]), 10, 64)

	return tx, err == nil
}
