// Package engine opens a data dir: it takes the lock, loads the catalog and
// opens one collection per catalog entry. Engine-mode collections are
// written by ndb; direct-mode collections are fed by a tailer (plan §5, §6).
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thadeu/ndb/internal/blob"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/crypt"
	"github.com/thadeu/ndb/internal/cursors"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/lock"
	"github.com/thadeu/ndb/internal/locks"
	"github.com/thadeu/ndb/internal/metrics"
	"github.com/thadeu/ndb/internal/settings"
	"github.com/thadeu/ndb/internal/tailer"
	"github.com/thadeu/ndb/internal/writer"
)

// DefaultMaxLine is the line limit of a collection without max_line_size.
const DefaultMaxLine = 1 << 20

// walSegmentSize is the WAL file size of every engine collection (0 = the
// collection default, 64 MiB). Tests lower it to force rotation.
var walSegmentSize int64

// DB is one open data dir. It is safe for concurrent use.
type DB struct {
	dir  string
	log  *slog.Logger
	lock *lock.Lock
	cat  *catalog.Catalog

	// settings are the settings of the data dir (/_cluster/settings), and
	// key the encryption key of the daemon (internal/settings).
	settings *settings.Store
	key      settings.Key
	keyMu    sync.Mutex // key changes in a rotation (rotate.go)
	box      *crypt.Box

	// blobs is the content-addressed store under <data_dir>/blobs.
	blobs *blob.Store

	// gcRunning is 1 while a blob GC started by a compaction runs.
	gcRunning atomic.Bool
	gcWait    sync.WaitGroup

	// cursors holds the position of each watch consumer.
	cursors *cursors.Store

	ctx    context.Context
	cancel context.CancelFunc

	// groupLimit is the group table memory of one aggregate (0 = default).
	groupLimit int64

	// shared is the write buffer budget and the heavy work slot that every
	// collection of the daemon shares.
	shared *collection.Shared

	// locks is the pessimistic locks of the documents, in memory.
	locks *locks.Table

	// mu guards cols and closed. Opening a collection (recovery) runs
	// under it. After Close, closed is true and no collection opens again.
	mu     sync.RWMutex
	cols   map[string]*Collection
	closed bool

	// swap is held for writing by Truncate and Drop, and for reading by the
	// work that reads the files of many collections (backup, blob GC), so
	// that work sees a collection before or after the swap, never half.
	swap sync.RWMutex

	// trashWait waits for the background delete of truncated and dropped
	// collection dirs.
	trashWait sync.WaitGroup

	// pitr is the default PITR window ("off", "1d"). pitrMu serializes the
	// snapshot loop and the restores, so a restore never reads a snapshot
	// that is being deleted.
	pitr   string
	pitrMu sync.Mutex

	// idleClose is how long an engine collection goes unused before it
	// sleeps (idle.go); 0 keeps every collection open.
	idleClose time.Duration
}

// Open takes the lock on cfg.DataDir, opens the catalog, declares the
// collections of ndb.yml and opens every collection in the catalog.
func Open(cfg config.Config, log *slog.Logger) (*DB, error) {
	began := time.Now()

	if log == nil {
		log = slog.Default()
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}

	lk, err := lock.Acquire(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	st, key, err := openSettings(cfg, log)
	if err != nil {
		lk.Release()

		return nil, err
	}

	box, err := crypt.New(key.ID, key.Derive("ndb:e1"), key.Derive("ndb:h1"))

	if err == nil && key.Old != nil {
		err = box.SetOld(key.Old.ID, key.Old.Derive("ndb:e1"), key.Old.Derive("ndb:h1"))
	}

	if err != nil {
		lk.Release()

		return nil, err
	}

	cat, err := catalog.Open(filepath.Join(cfg.DataDir, "catalog.bolt"), catalog.Options{AllowImplicit: cfg.AllowImplicit})
	if err != nil {
		lk.Release()

		return nil, err
	}

	cur, err := cursors.Open(filepath.Join(cfg.DataDir, "cursors.bolt"))
	if err != nil {
		cat.Close()
		lk.Release()

		return nil, err
	}

	blobs, err := blob.Open(fs.OS{}, filepath.Join(cfg.DataDir, "blobs"))
	if err != nil {
		cur.Close()
		cat.Close()
		lk.Release()

		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	db := &DB{
		dir:        cfg.DataDir,
		log:        log,
		lock:       lk,
		cat:        cat,
		settings:   st,
		key:        key,
		box:        box,
		blobs:      blobs,
		cursors:    cur,
		ctx:        ctx,
		cancel:     cancel,
		groupLimit: cfg.GroupLimit,
		shared:     collection.NewShared(cfg.WriteBuffer),
		locks:      locks.New(),
		cols:       map[string]*Collection{},
		pitr:       pitrDefault(cfg),
		idleClose:  idleCloseOf(cfg),
	}

	if err := db.finishSwaps(); err != nil {
		db.Close()

		return nil, err
	}

	if err := db.finishRename(); err != nil {
		db.Close()

		return nil, err
	}

	if err := db.sweepTrash(time.Now()); err != nil {
		db.Close()

		return nil, err
	}

	if err := cfg.Apply(cat); err != nil {
		db.Close()

		return nil, err
	}

	for _, def := range cat.List() {
		if err := db.attach(def); err != nil {
			db.Close()

			return nil, fmt.Errorf("collection %s: %w", def.Name, err)
		}
	}

	db.log.Info("recovery done", "data_dir", cfg.DataDir, "collections", len(db.cols), "ms", time.Since(began).Milliseconds())

	db.gcWait.Add(4)

	go db.sweepPartitionsLoop()
	go db.pitrLoop()
	go db.idleLoop()
	go db.rotationLoop()

	return db, nil
}

// Stop is the first step of a shutdown: the background loops end, and the
// seals and compactions in progress stop (a compaction that a request runs
// returns), so the requests in flight finish soon. Reads and writes still
// work until Close.
func (db *DB) Stop() {
	db.cancel()

	db.mu.RLock()
	defer db.mu.RUnlock()

	for _, col := range db.cols {
		col.stopWork()
	}
}

// ErrClosed is every call on a DB after Close. It is writer.ErrClosed for
// errors.Is, so the API answers 503 closed.
var ErrClosed error = closedError{}

type closedError struct{}

func (closedError) Error() string {
	return "closed: the database is closed"
}

func (closedError) Is(target error) bool {
	return target == writer.ErrClosed
}

// Close stops the tailers, closes the collections and the catalog, and
// releases the lock. Writes that were acked are durable before it returns.
// After Close, every call gives ErrClosed. A second Close does nothing.
func (db *DB) Close() error {
	db.cancel()
	db.gcWait.Wait()
	db.trashWait.Wait()

	db.mu.Lock()

	if db.closed {
		db.mu.Unlock()

		return nil
	}

	db.closed = true
	cols := db.cols
	db.cols = map[string]*Collection{}

	db.mu.Unlock()

	// Take live of each collection without db.mu (the order of rename is
	// live, then db.mu), so the calls in flight end first.
	var errs []error

	for _, col := range cols {
		errs = append(errs, col.shut())
	}

	errs = append(errs, db.cursors.Close(), db.cat.Close(), db.lock.Release())

	return errors.Join(errs...)
}

// checkOpen is ErrClosed after Close. The caller holds db.mu.
func (db *DB) checkOpen() error {
	if db.closed {
		return ErrClosed
	}

	return nil
}

// Get returns an open collection. An unknown name gives catalog.ErrUnknown.
func (db *DB) Get(name string) (*Collection, error) {
	db.mu.RLock()
	defer db.mu.RUnlock()

	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	col, ok := db.cols[name]

	if !ok {
		return nil, fmt.Errorf("%w: %s", catalog.ErrUnknown, name)
	}

	return col, nil
}

// GetOrCreate returns the collection, or creates it with defaults for an
// insert into an unknown name (plan §5.1, rule 3).
func (db *DB) GetOrCreate(name string) (*Collection, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return nil, err
	}

	if col, ok := db.cols[name]; ok {
		return col, nil
	}

	_, known := db.cat.Get(name)

	def, err := db.cat.EnsureImplicit(name)
	if err != nil {
		return nil, err
	}

	if err := db.attach(def); err != nil {
		return nil, err
	}

	if !known {
		db.log.Info("collection created", "collection", name, "implicit", true)
	}

	return db.cols[name], nil
}

// Ensure is ensure_collection: it creates the collection or merges decl into
// it by the rules of plan §5.2, and returns the final definition.
// partition_of is set by ndb only, so a declaration that sends it is
// invalid_request. A new unique field on a collection with data is
// checked first (unique.go).
func (db *DB) Ensure(decl catalog.Def) (catalog.Def, error) {
	if err := checkDecl(decl); err != nil {
		return catalog.Def{}, err
	}

	return db.ensure(decl)
}

// ensure is Ensure, also for the partitions that ndb makes.
func (db *DB) ensure(decl catalog.Def) (catalog.Def, error) {
	if cur, ok := db.cat.Get(decl.Name); ok && cur.Write == catalog.Engine && len(newUnique(cur, decl)) > 0 {
		if col, err := db.Get(decl.Name); err == nil {
			return db.ensureUnique(col, decl)
		}
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return catalog.Def{}, err
	}

	def, err := db.cat.Ensure(decl, catalog.FromCode)
	if err != nil {
		return catalog.Def{}, err
	}

	return def, db.attach(def)
}

// DropIndex is drop_index: the catalog forgets the field and the live
// collection stops using its index.
func (db *DB) DropIndex(name, field string) (catalog.Def, error) {
	db.mu.Lock()
	defer db.mu.Unlock()

	if err := db.checkOpen(); err != nil {
		return catalog.Def{}, err
	}

	def, err := db.cat.DropIndex(name, field)
	if err != nil {
		return catalog.Def{}, err
	}

	if col, ok := db.cols[name]; ok && col.enter() == nil {
		col.rd.DropIndex(field)
		col.leave()
	}

	return def, nil
}

// Dir is the data dir.
func (db *DB) Dir() string {
	return db.dir
}

// Definitions lists the catalog, sorted by name.
func (db *DB) Definitions() []catalog.Def {
	return db.cat.List()
}

// Definition returns one catalog entry.
func (db *DB) Definition(name string) (catalog.Def, bool) {
	return db.cat.Get(name)
}

// MaxLine is the line limit of a collection: its max_line_size, or the
// default for an unknown name.
func (db *DB) MaxLine(name string) int64 {
	def, ok := db.cat.Get(name)

	if !ok || def.MaxLineSize == 0 {
		return DefaultMaxLine
	}

	return def.MaxLineSize
}

// attach opens def if it is not open yet, or adds its indexes to the open
// collection. The caller holds db.mu.
func (db *DB) attach(def catalog.Def) error {
	if err := db.checkOpen(); err != nil {
		return err
	}

	if def.PartitionBy != "" {
		db.encryptPartitions(def)

		return nil // a logical collection: its partitions hold the data
	}

	if col, ok := db.cols[def.Name]; ok {
		if err := col.enter(); err != nil {
			return err
		}

		defer col.leave()

		if col.eng != nil {
			col.eng.SetEncrypted(def)
		}

		for _, f := range def.Indexes {
			if err := col.rd.EnsureIndex(f); err != nil {
				return err
			}
		}

		return nil
	}

	col, err := db.open(def)
	if err != nil {
		return err
	}

	db.cols[def.Name] = col

	return nil
}

// encryptPartitions gives the encrypted fields of a logical collection to
// its open partitions.
func (db *DB) encryptPartitions(def catalog.Def) {
	for name, col := range db.cols {
		if col.eng != nil && strings.HasPrefix(name, def.Name+"-") {
			if part, ok := db.cat.Get(name); ok && part.PartitionOf == def.Name {
				col.eng.SetEncrypted(catalog.PartitionDef(def, name))
			}
		}
	}
}

func (db *DB) open(def catalog.Def) (*Collection, error) {
	dir := filepath.Join(db.dir, "collections", def.Name)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	if def.Write == catalog.Direct {
		return db.openDirect(def, dir)
	}

	return db.openEngine(def, dir, &metrics.Writer{})
}

// openEngine opens an engine collection with the writer metrics wm (a
// collection that wakes keeps its own, so the counters go on).
func (db *DB) openEngine(def catalog.Def, dir string, wm *metrics.Writer) (*Collection, error) {
	eng, err := collection.Open(collection.Config{
		Dir:        dir,
		Data:       dir,
		FS:         fs.OS{},
		Def:        def,
		GroupLimit: db.groupLimit,
		Shared:     db.shared,
		Blobs:      db.blobs,
		Box:        db.box,
		Observer:   wm,
		OnCompact:  db.compacted,
		Locks:      db.locks.Scope(def.Name),

		Cursors:        func() ([]string, error) { return db.cursors.List(def.Name) },
		WALSegmentSize: walSegmentSize,
	})
	if err != nil {
		return nil, err
	}

	col := &Collection{name: def.Name, dir: dir, write: catalog.Engine, eng: eng, rd: eng, wm: wm, db: db}

	col.used.Store(time.Now().UnixNano())

	return col, nil
}

// openDirect reads the files up to their end before it returns, so a ready
// daemon answers from the full data. The rows live in segments (disk first),
// and the tailer goes on from the file positions of the last checkpoint.
func (db *DB) openDirect(def catalog.Def, dir string) (*Collection, error) {
	mem, err := collection.OpenDirect(collection.DirectConfig{Def: def, Data: dir, FS: fs.OS{}, Shared: db.shared})
	if err != nil {
		return nil, err
	}

	mem.SetGroupLimit(db.groupLimit)

	tail, err := tailer.Open(tailer.Config{Dir: dir, Def: def, Sink: mem, Store: sourceStore{mem}, Logger: db.log})
	if err != nil {
		mem.Close()

		return nil, err
	}

	if err := tail.Sync(db.ctx); err != nil {
		tail.Close()
		mem.Close()

		return nil, err
	}

	ctx, stop := context.WithCancel(db.ctx)
	col := &Collection{name: def.Name, dir: dir, write: catalog.Direct, mem: mem, rd: mem, tail: tail, stop: stop, done: make(chan struct{})}

	go func() {
		defer close(col.done)

		if err := tail.Run(ctx); err != nil {
			db.log.Error("tailer stopped", "collection", def.Name, "err", err)
		}
	}()

	return col, nil
}

// sourceStore keeps the tailer cursors of a direct collection in its
// checkpoint: a cursor is saved only with the rows it covers.
type sourceStore struct{ d *collection.Direct }

func (s sourceStore) Load() (map[uint64]tailer.Cursor, error) {
	out := map[uint64]tailer.Cursor{}

	for _, raw := range s.d.Sources() {
		var c tailer.Cursor

		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}

		out[c.Inode] = c
	}

	return out, nil
}

func (s sourceStore) Save(c tailer.Cursor) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}

	s.d.MarkSource(strconv.FormatUint(c.Inode, 10), raw)

	return nil
}

func (s sourceStore) Remove(inode uint64) error {
	s.d.ForgetSource(strconv.FormatUint(inode, 10))

	return nil
}

func (s sourceStore) Close() error { return nil }

// AllStats is the Stats of every open collection, sorted by name.
func (db *DB) AllStats() []Stats {
	db.mu.RLock()
	defer db.mu.RUnlock()

	out := make([]Stats, 0, len(db.cols))

	for _, col := range db.cols {
		out = append(out, col.Stats())
	}

	slices.SortFunc(out, func(a, b Stats) int { return strings.Compare(a.Name, b.Name) })

	return out
}

// openSettings reads the settings of the data dir and the encryption key.
// A key made now is logged once, with no part of it.
func openSettings(cfg config.Config, log *slog.Logger) (*settings.Store, settings.Key, error) {
	st, err := settings.Open(cfg.DataDir)
	if err != nil {
		return nil, settings.Key{}, err
	}

	key, err := settings.LoadKeys(st, cfg.EncryptionKey, cfg.EncryptionKeyOld)
	if err != nil {
		return nil, settings.Key{}, fmt.Errorf("encryption key: %w", err)
	}

	if key.Source == "made" {
		log.Warn("made an encryption key for this data dir; it is kept in the settings and in every backup. To hold it outside the data dir, set NDB_ENCRYPTION_KEY",
			"key", key.Masked(), "key_id", key.ID)
	}

	return st, key, nil
}

// Settings are the settings of the data dir.
func (db *DB) Settings() *settings.Store {
	return db.settings
}

// Box seals and opens the encrypted fields, with keys derived once.
func (db *DB) Box() *crypt.Box {
	return db.box
}

// Key is the encryption key of the daemon.
func (db *DB) Key() settings.Key {
	db.keyMu.Lock()
	defer db.keyMu.Unlock()

	return db.key
}
