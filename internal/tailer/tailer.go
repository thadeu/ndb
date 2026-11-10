// Package tailer follows the NDJSON files of a direct-mode collection and
// feeds each line to a Sink (plan §9).
package tailer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	ndbfs "github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/wal"
)

const (
	defaultPoll       = 5 * time.Second
	defaultMaxLine    = 1 << 20
	defaultBatchLines = 20000
	fullRetry         = 10 * time.Millisecond
)

// Sink receives each complete line and the offset where it starts. The line
// is valid only during the call. A collection.ErrFull answer makes the
// tailer wait and retry the same line; any other error quarantines it.
type Sink interface {
	Apply(file string, off int64, line []byte) error
}

type Config struct {
	// Dir is the collection dir: it holds meta.bolt and quarantine/.
	Dir  string
	Def  catalog.Def
	Sink Sink
	FS   ndbfs.FS

	// Poll is the fallback scan period (default 5 s).
	Poll time.Duration

	// BatchLines is how many lines are applied between cursor saves.
	BatchLines int

	// NoWatch turns fsnotify off: polling only (bind mounts, overlays).
	NoWatch bool

	// Rebuild drops the saved cursors. Use it when the Sink starts empty
	// (the memtable is in memory only).
	Rebuild bool

	// Store keeps the cursors instead of meta.bolt (see Store).
	Store Store

	Logger *slog.Logger
}

type file struct {
	// lagged is the part of the file not read yet, as last added to
	// Tailer.lag. Guarded by Tailer.mu.
	lagged int64

	f     ndbfs.File
	path  string
	ino   uint64
	off   int64
	fplen int
	fp    uint64
}

type Tailer struct {
	cfg     Config
	log     *slog.Logger
	cursors Store
	quar    *quarantine
	maxLine int
	base    string
	depth   int

	mu    sync.Mutex
	files map[uint64]*file
	saved map[uint64]Cursor

	watcher *fsnotify.Watcher
	wake    chan struct{}

	// lag is the bytes that sit in the files after the cursors.
	lag atomic.Int64
}

func Open(cfg Config) (*Tailer, error) {
	if cfg.FS == nil {
		cfg.FS = ndbfs.OS{}
	}

	if cfg.Poll == 0 {
		cfg.Poll = defaultPoll
	}

	if cfg.BatchLines == 0 {
		cfg.BatchLines = defaultBatchLines
	}

	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	cursors, saved, err := openStore(cfg)
	if err != nil {
		return nil, err
	}

	t := &Tailer{
		cfg:     cfg,
		log:     cfg.Logger.With("collection", cfg.Def.Name),
		cursors: cursors,
		quar:    newQuarantine(cfg.FS, cfg.Dir, cfg.Def.Name),
		maxLine: int(cfg.Def.MaxLineSize),
		files:   map[uint64]*file{},
		saved:   saved,
		wake:    make(chan struct{}, 1),
	}

	if t.maxLine == 0 {
		t.maxLine = defaultMaxLine
	}

	t.base, t.depth = globBase(cfg.Def.Path)

	return t, nil
}

// openStore opens the cursor store of cfg and loads its cursors.
func openStore(cfg Config) (Store, map[uint64]Cursor, error) {
	if cfg.Store == nil {
		return openCursors(cfg.Dir, cfg.Rebuild)
	}

	saved, err := cfg.Store.Load()

	return cfg.Store, saved, err
}

// Lag is how many bytes the tailer has not read yet, over all files. It is
// updated while a file drains, so a long catch-up shows.
func (t *Tailer) Lag() int64 {
	return t.lag.Load()
}

func (t *Tailer) setLag(fl *file, size int64) {
	left := size - fl.off

	t.lag.Add(left - fl.lagged)

	fl.lagged = left
}

// Quarantined is how many distinct lines (file and offset) the quarantine
// file holds: lines found at Open plus new ones. A rebuild adds none twice.
func (t *Tailer) Quarantined() int64 {
	return t.quar.count.Load()
}

// QuarantineRows is the last limit quarantined lines, oldest first, and how
// many distinct lines the file holds.
func (t *Tailer) QuarantineRows(limit int) ([]QuarantineRow, int, error) {
	return t.quar.rows(limit)
}

func (t *Tailer) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, fl := range t.files {
		fl.f.Close()
	}

	t.files = map[uint64]*file{}

	if t.watcher != nil {
		t.watcher.Close()
	}

	t.quar.close()

	return t.cursors.Close()
}

// Run follows the files until ctx ends: one scan at start, then a scan on
// each fsnotify event and every Poll.
func (t *Tailer) Run(ctx context.Context) error {
	if !t.cfg.NoWatch {
		w, err := fsnotify.NewWatcher()

		if err != nil {
			t.log.Warn("fsnotify unavailable, polling only", "err", err)
		} else {
			t.mu.Lock()
			t.watcher = w
			t.mu.Unlock()

			go t.pump(ctx, w)
		}
	}

	tick := time.NewTicker(t.cfg.Poll)
	defer tick.Stop()

	for {
		if err := t.Sync(ctx); err != nil && ctx.Err() == nil {
			t.log.Error("sync failed", "err", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-t.wake:
		}
	}
}

func (t *Tailer) pump(ctx context.Context, w *fsnotify.Watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-w.Events:
			if !ok {
				return
			}

			select {
			case t.wake <- struct{}{}:
			default:
			}
		case err, ok := <-w.Errors:
			if !ok {
				return
			}

			t.log.Warn("fsnotify error", "err", err)
		}
	}
}

// Sync does one pass: find the files, read every file to its last full
// line, and drop the files that are gone.
func (t *Tailer) Sync(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	present, err := t.discover()
	if err != nil {
		return err
	}

	paths := make([]uint64, 0, len(t.files))

	for ino := range t.files {
		paths = append(paths, ino)
	}

	slices.SortFunc(paths, func(a, b uint64) int { return compareStr(t.files[a].path, t.files[b].path) })

	for _, ino := range paths {
		if err := t.drain(ctx, t.files[ino]); err != nil {
			return err
		}
	}

	for _, ino := range paths {
		if _, ok := present[ino]; ok {
			continue
		}

		t.lag.Add(-t.files[ino].lagged)
		t.files[ino].f.Close()
		delete(t.files, ino)
		delete(t.saved, ino)

		if err := t.cursors.Remove(ino); err != nil {
			return err
		}
	}

	return nil
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}

	return 0
}

// discover walks the glob, starts to track the files it has not seen, and
// returns the inodes found.
func (t *Tailer) discover() (map[uint64]struct{}, error) {
	present := map[uint64]struct{}{}

	err := filepath.WalkDir(t.base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == t.base {
				return filepath.SkipAll
			}

			return nil
		}

		if d.IsDir() {
			return t.visitDir(p)
		}

		if !GlobMatch(t.cfg.Def.Path, p) {
			return nil
		}

		ino, err := t.attach(p)
		if err != nil {
			t.log.Warn("cannot follow file", "file", p, "err", err)

			return nil
		}

		present[ino] = struct{}{}

		return nil
	})

	return present, err
}

func (t *Tailer) visitDir(p string) error {
	if t.depth >= 0 {
		rel, _ := filepath.Rel(t.base, p)

		if rel != "." && len(splitPath(rel)) >= t.depth {
			return filepath.SkipDir
		}
	}

	if t.watcher != nil {
		if err := t.watcher.Add(p); err != nil {
			t.log.Warn("cannot watch folder", "dir", p, "err", err)
		}
	}

	return nil
}

func splitPath(p string) []string {
	return strings.Split(p, string(filepath.Separator))
}

// attach starts to track the file at p unless its inode is tracked
// already, and returns the inode.
func (t *Tailer) attach(p string) (uint64, error) {
	info, err := t.cfg.FS.Stat(p)
	if err != nil {
		return 0, err
	}

	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("no inode on this platform")
	}

	ino := uint64(st.Ino)

	if fl, ok := t.files[ino]; ok {
		fl.path = p

		return ino, nil
	}

	f, err := t.cfg.FS.Open(p)
	if err != nil {
		return 0, err
	}

	fl := &file{f: f, path: p, ino: ino}

	if cur, ok := t.saved[ino]; ok && t.sameFile(fl, cur) {
		fl.off, fl.fplen, fl.fp = cur.Offset, cur.FPLen, cur.FP
	}

	t.files[ino] = fl

	return ino, nil
}

// sameFile checks the saved cursor against the file now: the inode may be
// reused, or the file may have been replaced while ndb was down.
func (t *Tailer) sameFile(fl *file, cur Cursor) bool {
	info, err := fl.f.Stat()
	if err != nil || info.Size() < cur.Offset {
		return false
	}

	return matchesFingerprint(fl.f, info.Size(), cur.FPLen, cur.FP)
}

func matchesFingerprint(f io.ReaderAt, size int64, fplen int, want uint64) bool {
	if fplen == 0 {
		return true
	}

	if size < int64(fplen) {
		return false
	}

	n, got, err := fingerprint(f, int64(fplen))

	return err == nil && n == fplen && got == want
}

// drain reads the file from its offset to the last full line.
func (t *Tailer) drain(ctx context.Context, fl *file) error {
	info, err := fl.f.Stat()
	if err != nil {
		return err
	}

	size := info.Size()

	if size < fl.off || !matchesFingerprint(fl.f, size, fl.fplen, fl.fp) {
		t.log.Warn("file was truncated or replaced, reading again from 0", "file", fl.path, "offset", fl.off, "size", size)

		fl.off, fl.fplen, fl.fp = 0, 0, 0
	}

	t.setLag(fl, size)

	if size == fl.off {
		return nil
	}

	lr := wal.NewLineReader(io.NewSectionReader(fl.f, fl.off, size-fl.off), fl.off, t.maxLine)
	batch := 0

	for {
		l, start, err := lr.Next()

		if errors.Is(err, io.EOF) || errors.Is(err, wal.ErrPartialLine) {
			break
		}

		if err != nil && !errors.Is(err, wal.ErrLineTooLarge) {
			return err
		}

		if err := t.handle(ctx, fl, l, start, err); err != nil {
			t.save(fl, size)

			return err
		}

		fl.off = lr.Offset()
		batch++

		if batch%t.cfg.BatchLines == 0 {
			t.setLag(fl, size)

			if err := t.save(fl, size); err != nil {
				return err
			}
		}
	}

	t.setLag(fl, size)

	return t.save(fl, size)
}

// handle applies one line, or quarantines it. It waits while the sink is
// full: a line is never dropped.
func (t *Tailer) handle(ctx context.Context, fl *file, l []byte, start int64, readErr error) error {
	if readErr != nil {
		head := make([]byte, quarantineLineMax)
		n, _ := fl.f.ReadAt(head, start)

		return t.reject(fl, start, "line over max_line_size", head[:n], false)
	}

	if len(l) == 0 {
		return nil
	}

	waited := false

	for {
		err := t.cfg.Sink.Apply(fl.path, start, l)

		if err == nil {
			return nil
		}

		if !errors.Is(err, collection.ErrFull) {
			return t.reject(fl, start, err.Error(), l, true)
		}

		if !waited {
			t.log.Warn("memtable full, the tail waits for a seal", "file", fl.path, "offset", start)

			waited = true
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(fullRetry):
		}
	}
}

func (t *Tailer) reject(fl *file, off int64, reason string, l []byte, whole bool) error {
	added, err := t.quar.add(fl.path, off, reason, l, whole)

	if added {
		t.log.Warn("line quarantined", "file", fl.path, "offset", off, "reason", reason, "bytes", len(l))
	}

	if err != nil {
		t.log.Error("cannot write quarantine row", "file", fl.path, "offset", off, "err", err)
	}

	return nil
}

func (t *Tailer) save(fl *file, size int64) error {
	var err error

	fl.fplen, fl.fp, err = fingerprint(fl.f, size)
	if err != nil {
		return fmt.Errorf("fingerprint %s: %w", fl.path, err)
	}

	cur := Cursor{Path: fl.path, Inode: fl.ino, Offset: fl.off, FPLen: fl.fplen, FP: fl.fp}

	if prev, ok := t.saved[fl.ino]; ok && prev == cur {
		return nil
	}

	t.saved[fl.ino] = cur

	return t.cursors.Save(cur)
}
