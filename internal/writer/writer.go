// Package writer is the single writer of one collection WAL: it serializes
// commits, groups them into one write + one fsync, and rotates segments.
package writer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/wal"
)

var ErrClosed = errors.New("writer: closed")

type Durability int

const (
	// Strict: reply after fdatasync.
	Strict Durability = iota

	// Relaxed: reply after write; fsync runs every FlushEvery. A crash can
	// lose the last FlushEvery of commits, never corrupt the WAL.
	Relaxed
)

const (
	defaultSegmentSize = 64 << 20
	defaultMaxLine     = 1 << 20
	defaultFlushEvery  = 200 * time.Millisecond
	maxBatch           = 1024
)

type Config struct {
	Dir         string
	FS          fs.FS
	Durability  Durability
	SegmentSize int64
	MaxLine     int
	FlushEvery  time.Duration

	// StartTx is the last tx already in the WAL (set by recovery).
	StartTx uint64

	// Prepare turns a request into ops inside the writer goroutine, so
	// checks (unique, IfVersion) see every earlier commit, even ones in the
	// same batch that are not durable yet. An error rejects only that
	// request. Without Prepare, the request must be []wal.Op.
	Prepare func(req any) (Prepared, error)

	// Applied runs after the record is durable, in commit order.
	Applied func(rec wal.Record)

	// Synced runs on the writer goroutine after every successful fsync of
	// the WAL. In relaxed mode Applied runs before the fsync, so code that
	// must only see durable records (watch) waits for Synced.
	Synced func()

	// OnFatal runs when a write or fsync fails. The default exits the
	// process: after a failed fsync, Linux may drop dirty pages, so the only
	// safe path is to crash and recover from the WAL.
	OnFatal func(err error)
}

// Prepared is the output of Config.Prepare. Stage runs only when the record
// is accepted (it fits the line limit), so a rejected request never changes
// the caller's head state.
type Prepared struct {
	Ops   []wal.Op
	Stage func()
}

// Pos is the end of the WAL: the segment, the byte offset in it, and the
// last tx written.
type Pos struct {
	Seq    int
	Offset int64
	Tx     uint64
}

type job struct {
	fn   func(Pos)
	done chan error
}

type request struct {
	ctx  context.Context
	req  any
	done chan result
}

type result struct {
	tx  uint64
	ops []wal.Op
	err error
}

type Writer struct {
	cfg Config

	reqs   chan *request
	jobs   chan job
	stop   chan struct{}
	exited chan struct{}
	wg     sync.WaitGroup

	mu     sync.RWMutex
	closed bool

	file fs.File
	seq  int
	size int64
	tx   uint64
	buf  []byte

	dirty bool
}

func Open(cfg Config) (*Writer, error) {
	setDefaults(&cfg)

	w := &Writer{
		cfg:    cfg,
		reqs:   make(chan *request, maxBatch),
		jobs:   make(chan job),
		stop:   make(chan struct{}),
		exited: make(chan struct{}),
		tx:     cfg.StartTx,
	}

	if err := w.openLastSegment(); err != nil {
		return nil, err
	}

	w.wg.Add(1)

	go w.loop()

	return w, nil
}

func setDefaults(cfg *Config) {
	if cfg.SegmentSize == 0 {
		cfg.SegmentSize = defaultSegmentSize
	}

	if cfg.MaxLine == 0 {
		cfg.MaxLine = defaultMaxLine
	}

	if cfg.FlushEvery == 0 {
		cfg.FlushEvery = defaultFlushEvery
	}

	if cfg.OnFatal == nil {
		cfg.OnFatal = func(err error) { log.Fatalf("ndb: wal write failed, exiting to recover: %v", err) }
	}
}

// Commit writes one transaction and returns its tx and final ops. A
// canceled ctx stops the wait, not the commit: it may still become durable.
func (w *Writer) Commit(ctx context.Context, req any) (uint64, []wal.Op, error) {
	r := &request{ctx: ctx, req: req, done: make(chan result, 1)}

	w.mu.RLock()

	if w.closed {
		w.mu.RUnlock()

		return 0, nil, ErrClosed
	}

	select {
	case w.reqs <- r:
	case <-ctx.Done():
		w.mu.RUnlock()

		return 0, nil, ctx.Err()
	}

	w.mu.RUnlock()

	select {
	case res := <-r.done:
		return res.tx, res.ops, res.err
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// Close stops new commits, finishes the queued ones, syncs and closes.
func (w *Writer) Close() error {
	w.mu.Lock()

	if w.closed {
		w.mu.Unlock()

		return nil
	}

	w.closed = true
	close(w.stop)
	w.mu.Unlock()

	w.wg.Wait()

	if w.file == nil {
		return nil
	}

	err := w.file.Sync()

	if err == nil {
		w.synced()
	}

	if cerr := w.file.Close(); err == nil {
		err = cerr
	}

	return err
}

// Run calls fn in the writer goroutine, between commit batches, with the end
// of the WAL. The WAL is synced before fn runs, so every tx up to Pos.Tx is
// durable when fn sees it. fn must not block for long: commits wait for it.
func (w *Writer) Run(fn func(end Pos)) error {
	j := job{fn: fn, done: make(chan error, 1)}

	select {
	case w.jobs <- j:
	case <-w.exited:
		return ErrClosed
	}

	return <-j.done
}

func (w *Writer) runJob(j job) bool {
	if err := w.file.Sync(); err != nil {
		j.done <- err
		w.closeOnFatal(err)

		return false
	}

	w.dirty = false

	w.synced()

	j.fn(Pos{Seq: w.seq, Offset: w.size, Tx: w.tx})
	j.done <- nil

	return true
}

func (w *Writer) loop() {
	defer w.wg.Done()

	defer close(w.exited)

	var tick <-chan time.Time

	if w.cfg.Durability == Relaxed {
		t := time.NewTicker(w.cfg.FlushEvery)
		defer t.Stop()

		tick = t.C
	}

	for {
		select {
		case first := <-w.reqs:
			if !w.commitBatch(w.drain(first)) {
				w.failRest()

				return
			}
		case j := <-w.jobs:
			if !w.runJob(j) {
				w.failRest()

				return
			}
		case <-tick:
			if !w.flushDirty() {
				w.failRest()

				return
			}
		case <-w.stop:
			w.finishQueued()

			return
		}
	}
}

// finishQueued commits what is already in the channel at Close.
func (w *Writer) finishQueued() {
	for {
		select {
		case first := <-w.reqs:
			if !w.commitBatch(w.drain(first)) {
				w.failRest()

				return
			}
		default:
			return
		}
	}
}

func (w *Writer) drain(first *request) []*request {
	batch := []*request{first}

	for len(batch) < maxBatch {
		select {
		case r := <-w.reqs:
			batch = append(batch, r)
		default:
			return batch
		}
	}

	return batch
}

type pending struct {
	req *request
	rec wal.Record
}

// commitBatch writes the batch. It returns false after a fatal error.
func (w *Writer) commitBatch(batch []*request) bool {
	var ok []pending

	w.buf = w.buf[:0]

	for _, req := range batch {
		if req.ctx.Err() != nil {
			req.done <- result{err: req.ctx.Err()}

			continue
		}

		p, err := w.encode(req)

		var rerr *rotateError

		if errors.As(err, &rerr) {
			req.done <- result{err: err}

			return w.fatal(rerr.err, ok)
		}

		if err != nil {
			req.done <- result{err: err}

			continue
		}

		ok = append(ok, p)
	}

	if len(ok) == 0 {
		return true
	}

	if err := w.flushBuf(); err != nil {
		return w.fatal(err, ok)
	}

	if w.cfg.Durability == Strict {
		if err := w.file.Sync(); err != nil {
			return w.fatal(err, ok)
		}
	} else {
		w.dirty = true
	}

	for _, p := range ok {
		if w.cfg.Applied != nil {
			w.cfg.Applied(p.rec)
		}

		p.req.done <- result{tx: p.rec.Tx, ops: p.rec.Ops}
	}

	if w.cfg.Durability == Strict {
		w.synced()
	}

	return true
}

func (w *Writer) prepare(req *request) (Prepared, error) {
	if w.cfg.Prepare != nil {
		return w.cfg.Prepare(req.req)
	}

	ops, ok := req.req.([]wal.Op)
	if !ok {
		return Prepared{}, fmt.Errorf("writer: request is %T, want []wal.Op", req.req)
	}

	return Prepared{Ops: ops}, nil
}

// encode prepares and appends one record to w.buf, rotating the segment
// first when the record does not fit.
func (w *Writer) encode(req *request) (pending, error) {
	prep, err := w.prepare(req)
	if err != nil {
		return pending{}, err
	}

	rec := wal.Record{Tx: w.tx + 1, TS: time.Now().UTC(), Ops: prep.Ops}

	start := len(w.buf)

	line, err := wal.Encode(w.buf, rec)
	if err != nil {
		return pending{}, err
	}

	if len(line)-start > w.cfg.MaxLine {
		w.buf = line[:start]

		return pending{}, wal.ErrLineTooLarge
	}

	w.buf = append(line, '\n')
	w.tx++

	if prep.Stage != nil {
		prep.Stage()
	}

	if w.size+int64(len(w.buf)) > w.cfg.SegmentSize && w.size+int64(start) > 0 {
		if err := w.rotateBefore(start); err != nil {
			return pending{}, &rotateError{err}
		}
	}

	return pending{req: req, rec: rec}, nil
}

// rotateError marks a failed rotation. It is fatal: the current segment is
// in an unknown state.
type rotateError struct {
	err error
}

func (e *rotateError) Error() string {
	return "writer: segment rotation failed: " + e.err.Error()
}

func (e *rotateError) Unwrap() error {
	return e.err
}

// rotateBefore writes and syncs w.buf[:cut] to the current segment, then
// opens a new segment and keeps w.buf[cut:] for it.
func (w *Writer) rotateBefore(cut int) error {
	head := slices.Clone(w.buf[:cut])
	tail := slices.Clone(w.buf[cut:])

	w.buf = head

	if err := w.flushBuf(); err != nil {
		return err
	}

	if err := w.file.Sync(); err != nil {
		return err
	}

	w.synced()

	if err := w.file.Close(); err != nil {
		return err
	}

	if err := w.createSegment(w.seq + 1); err != nil {
		return err
	}

	w.buf = tail

	return nil
}

func (w *Writer) flushBuf() error {
	if len(w.buf) == 0 {
		return nil
	}

	n, err := w.file.Write(w.buf)
	w.size += int64(n)

	if err != nil {
		return err
	}

	w.buf = w.buf[:0]

	return nil
}

func (w *Writer) flushDirty() bool {
	if !w.dirty {
		return true
	}

	if err := w.file.Sync(); err != nil {
		w.closeOnFatal(err)

		return false
	}

	w.dirty = false

	w.synced()

	return true
}

func (w *Writer) synced() {
	if w.cfg.Synced != nil {
		w.cfg.Synced()
	}
}

// fatal closes the writer first, then answers the batch. In the other order
// a new Commit could enter the queue after failRest ran and wait forever.
func (w *Writer) fatal(err error, ok []pending) bool {
	w.closeOnFatal(err)

	for _, p := range ok {
		p.req.done <- result{err: fmt.Errorf("writer: commit not durable: %w", err)}
	}

	return false
}

func (w *Writer) closeOnFatal(err error) {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()

	w.cfg.OnFatal(err)
}

// failRest answers every queued commit after a fatal error.
func (w *Writer) failRest() {
	for {
		select {
		case r := <-w.reqs:
			r.done <- result{err: ErrClosed}
		default:
			return
		}
	}
}

func (w *Writer) openLastSegment() error {
	if err := w.cfg.FS.MkdirAll(w.cfg.Dir); err != nil {
		return err
	}

	files, err := filepath.Glob(filepath.Join(w.cfg.Dir, "*.ndjson"))
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return w.createSegment(1)
	}

	last := files[len(files)-1]

	if _, err := fmt.Sscanf(filepath.Base(last), "%06d.ndjson", &w.seq); err != nil {
		return fmt.Errorf("writer: bad segment name %s", last)
	}

	f, err := w.cfg.FS.OpenAppend(last)
	if err != nil {
		return err
	}

	info, err := f.Stat()
	if err != nil {
		return err
	}

	w.file = f
	w.size = info.Size()

	return nil
}

func (w *Writer) createSegment(seq int) error {
	path := filepath.Join(w.cfg.Dir, fmt.Sprintf("%06d.ndjson", seq))

	f, err := w.cfg.FS.OpenAppend(path)
	if err != nil {
		return err
	}

	_ = f.Fallocate(w.cfg.SegmentSize)

	if err := w.cfg.FS.SyncDir(w.cfg.Dir); err != nil {
		f.Close()

		return err
	}

	w.file = f
	w.seq = seq
	w.size = 0

	return nil
}
