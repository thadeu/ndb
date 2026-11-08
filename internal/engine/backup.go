package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/wal"
)

// Backup writes a consistent state of the running daemon to w as one backup
// file (see package backup).
//
// What is consistent:
//   - The catalog and the watch positions are read first. A position is only
//     acked after its tx is in the WAL, so the WAL marks taken next are never
//     behind a saved position.
//   - The WAL of each engine collection up to a mark taken between two
//     commits: every write acked before Backup started is in, no write is
//     half in, and the copy is a prefix of the commit order. Commits go on
//     while the lines are written; later ones are not in the copy.
//   - The blobs that the saved WAL lines refer to, read after the marks, so
//     each one is there (a blob is durable before its line).
//
// Left out: segments and meta.bolt. They are derived from the WAL; the
// restored daemon rebuilds them. Direct-mode collections keep their data in
// the apps' own files; only their definition is saved.
func (db *DB) Backup(ctx context.Context, w io.Writer, opts backup.Options) error {
	db.swap.RLock()
	defer db.swap.RUnlock()

	defs := db.cat.List()

	entries, err := db.cursors.All()
	if err != nil {
		return err
	}

	cols := db.engineCollections()
	marks := map[string]backup.Mark{}
	snaps := map[string]backup.Snapshot{}

	for _, d := range defs {
		if d.Write == catalog.Direct {
			continue
		}

		col, ok := cols[d.Name]
		if !ok {
			return fmt.Errorf("backup: collection %s is not open", d.Name)
		}

		if err := col.enter(); err != nil {
			return fmt.Errorf("backup: %s: %w", d.Name, err)
		}

		defer col.leave()

		dir := filepath.Join(db.dir, "collections", d.Name)

		first, has, err := backup.FirstTx(dir)
		if err != nil {
			return fmt.Errorf("backup: %s: %w", d.Name, err)
		}

		if !has || first <= 1 {
			m, err := col.eng.WALMark()
			if err != nil {
				return fmt.Errorf("backup: %s: %w", d.Name, err)
			}

			marks[d.Name] = markOf(m)

			continue
		}

		m, snap, err := col.eng.WALMarkSnapshot()
		if err != nil {
			return fmt.Errorf("backup: %s: %w", d.Name, err)
		}

		defer snap.Close()

		snaps[d.Name] = backup.Snapshot{Tx: m.Tx, Lines: snapshotLines(ctx, snap)}
	}

	src := backup.Source{
		Defs:      defs,
		Cursors:   entries,
		Blobs:     db.blobs,
		Snapshots: snaps,
		WAL: func(name string, emit func([]byte) error) error {
			m := marks[name]

			return backup.ScanWAL(filepath.Join(db.dir, "collections", name), &m, func(line []byte) error {
				if err := ctx.Err(); err != nil {
					return err
				}

				return emit(line)
			})
		},
	}

	return backup.Write(w, src, opts)
}

func markOf(m collection.WALMark) backup.Mark {
	return backup.Mark{Seq: m.Seq, Offset: m.Offset}
}

func (db *DB) engineCollections() map[string]*Collection {
	db.mu.RLock()
	defer db.mu.RUnlock()

	out := map[string]*Collection{}

	for name, col := range db.cols {
		if col.engine() {
			out[name] = col
		}
	}

	return out
}

// snapshotLines encodes each document as a WAL put record, with its own tx
// and _v, so the restore checks the same CRC as for a WAL line.
func snapshotLines(ctx context.Context, snap *collection.Snapshot) func(func([]byte) error) error {
	return func(emit func([]byte) error) error {
		var buf []byte

		return snap.Each(func(d collection.SnapshotDoc) error {
			if err := ctx.Err(); err != nil {
				return err
			}

			raw, err := d.Raw()
			if err != nil {
				return err
			}

			rec := wal.Record{Tx: max(d.Tx, 1), TS: time.Now(), Ops: []wal.Op{{Kind: wal.Put, ID: d.ID, V: d.V, Doc: raw}}}

			buf, err = wal.Encode(buf[:0], rec)
			if err != nil {
				return err
			}

			return emit(buf)
		})
	}
}

// Offline saves the data dir of a stopped daemon. It opens the data dir like
// a daemon would (it takes the lock, so a running daemon gives lock.ErrLocked),
// which recovers the WAL and gives the same consistent state as an online
// backup, snapshots included.
func Offline(dataDir string, w io.Writer, opts backup.Options) error {
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		return fmt.Errorf("backup: data dir %s does not exist", dataDir)
	}

	db, err := Open(config.Config{DataDir: dataDir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}

	err = db.Backup(context.Background(), w, opts)

	return errors.Join(err, db.Close())
}
