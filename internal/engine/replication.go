package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/feed"
	"github.com/thadeu/ndb/internal/wal"
)

// The engine side of replication (docs/dev/replication.md). The owner reads
// the records of each collection from a position (OpenRecords) and the live
// documents for a resync (ReplicaSnapshot). A copy writes them with the
// normal write path (ApplyOps, ApplyDef). A position is the owner tx of the
// last record a copy applied: each collection has its own WAL, so its own
// tx.

// Record is one WAL record of a collection as the owner sends it: its tx,
// its commit time and its ops. A truncate record has one op of kind
// truncate. Ops is empty when every op was left out (the TTL sweep): the
// copy only moves its position.
type Record struct {
	Tx  uint64
	TS  time.Time
	Ops []wal.Op
}

// RecordStream yields the records of one collection after a position.
type RecordStream struct {
	s feed.Stream
}

// buffered is the part of an engine feed stream that tells where a record
// ends.
type buffered interface {
	Buffered() int
}

// nowOps marks a position after every op of its tx (collection/watch.go).
const nowOps = 1 << 30

// OpenRecords opens the records of the engine collection name after tx
// from (0: the whole history). A position whose WAL was deleted is
// feed.ErrHistoryTruncated.
func (db *DB) OpenRecords(name string, from uint64) (*RecordStream, error) {
	col, err := db.Get(name)
	if err != nil {
		return nil, err
	}

	if !col.engine() {
		return nil, fmt.Errorf("%w: %s is a direct-mode collection", catalog.ErrUnknown, name)
	}

	if err := col.enter(); err != nil {
		return nil, err
	}

	defer col.leave()

	start := ""

	if from > 0 {
		start = "e" + strconv.FormatUint(from, 10) + "." + strconv.Itoa(nowOps)
	}

	s, err := col.eng.Feed().Open(start)
	if err != nil {
		return nil, err
	}

	return &RecordStream{s: s}, nil
}

// Next blocks until the next record. io.EOF: the collection closed (a
// truncate, a rename, a drop or a stop); feed.ErrLagged: the reader was too
// slow. Both mean: open again from the last position.
func (rs *RecordStream) Next(ctx context.Context) (Record, error) {
	ev, err := rs.s.Next(ctx)
	if err != nil {
		return Record{}, err
	}

	tx, err := txOfPos(ev.Pos)
	if err != nil {
		return Record{}, err
	}

	rec := Record{Tx: tx, TS: time.Now()}

	for {
		if op, ok := opOf(ev); ok {
			rec.Ops = append(rec.Ops, op)
		}

		b, ok := rs.s.(buffered)
		if !ok || b.Buffered() == 0 {
			return rec, nil
		}

		if ev, err = rs.s.Next(ctx); err != nil {
			return Record{}, err
		}
	}
}

// Close ends the stream.
func (rs *RecordStream) Close() {
	rs.s.Close()
}

// opOf is the op of an event, or false for an op that is not replicated:
// the del of the TTL sweep (each node runs its own sweep).
func opOf(ev feed.Event) (wal.Op, bool) {
	if ev.Expired {
		return wal.Op{}, false
	}

	return wal.Op{Kind: wal.OpKind(ev.Op), ID: ev.ID, V: ev.V, Doc: ev.Doc}, true
}

// txOfPos reads the tx of an engine position "e<tx>.<n>".
func txOfPos(pos string) (uint64, error) {
	if len(pos) < 2 || pos[0] != 'e' {
		return 0, feed.ErrBadPos
	}

	end := 1

	for end < len(pos) && pos[end] != '.' {
		end++
	}

	return strconv.ParseUint(pos[1:end], 10, 64)
}

// LastTx is the last tx of the engine collection name (0 when it has
// none), without waking a collection that sleeps.
func (db *DB) LastTx(name string) uint64 {
	col, err := db.Get(name)
	if err != nil || !col.engine() {
		return 0
	}

	col.live.RLock()
	defer col.live.RUnlock()

	if col.closed || col.dropped {
		return 0
	}

	if col.asleep {
		return col.rest.lastTx
	}

	return col.eng.LastTx()
}

// ApplyOps writes the ops of one owner record into the engine collection
// name (collection.Apply), and makes the collection when it is not there.
// A truncate op truncates the collection.
func (db *DB) ApplyOps(ctx context.Context, name string, ops []wal.Op) error {
	if len(ops) == 1 && ops[0].Kind == wal.Trunc {
		err := db.Truncate(name, name)

		if errors.Is(err, catalog.ErrUnknown) {
			return nil
		}

		return err
	}

	col, err := db.GetOrCreate(name)
	if err != nil {
		return err
	}

	_, err = write(ctx, col, func(eng *collection.Collection) (int, error) {
		return eng.Apply(ctx, ops)
	})

	return err
}

// ApplyDef makes the definition of a collection of the owner the one of
// this database: it creates the collection or merges the definition, and
// drops the indexes that the owner dropped. A direct-mode definition is
// left out (its files are on the owner's machine).
func (db *DB) ApplyDef(def catalog.Def) error {
	if def.Write == catalog.Direct {
		return nil
	}

	cur, had := db.cat.Get(def.Name)

	if _, err := db.ensure(def); err != nil {
		return err
	}

	if !had {
		return nil
	}

	for _, field := range cur.Indexes {
		if !slices.Contains(def.Indexes, field) {
			if _, err := db.DropIndex(def.Name, field); err != nil {
				return err
			}
		}
	}

	return nil
}

// EngineDefs is the definitions of the engine-mode collections.
func (db *DB) EngineDefs() []catalog.Def {
	var out []catalog.Def

	for _, d := range db.cat.List() {
		if d.Write != catalog.Direct {
			out = append(out, d)
		}
	}

	return out
}

// ReplicaSnapshot calls emit with each live document of the engine
// collection name, as a put op with its own _id and _v, at one tx: the tx it
// returns first through start. A copy applies them, then reads the records
// after that tx.
func (db *DB) ReplicaSnapshot(ctx context.Context, name string, start func(tx uint64) error, emit func(op wal.Op) error) error {
	col, err := db.Get(name)
	if err != nil {
		return err
	}

	if !col.engine() {
		return fmt.Errorf("%w: %s is a direct-mode collection", catalog.ErrUnknown, name)
	}

	if err := col.enter(); err != nil {
		return err
	}

	defer col.leave()

	m, snap, err := col.eng.WALMarkSnapshot()
	if err != nil {
		return err
	}

	defer snap.Close()

	if err := start(m.Tx); err != nil {
		return err
	}

	return snap.Each(func(d collection.SnapshotDoc) error {
		if err := ctx.Err(); err != nil {
			return err
		}

		raw, err := d.Raw()
		if err != nil {
			return err
		}

		return emit(wal.Op{Kind: wal.Put, ID: d.ID, V: d.V, Doc: raw})
	})
}
