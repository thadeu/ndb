package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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
//
// The backup of the default database holds every database of the daemon
// (format 2 when there is one). The backup of another database holds that
// database alone, as the default database of a restored daemon.
func (db *DB) Backup(ctx context.Context, w io.Writer, opts backup.Options) error {
	var done []func()

	defer func() {
		for _, fn := range slices.Backward(done) {
			fn()
		}
	}()

	src, err := db.backupSource(ctx, opts.Streams, &done)
	if err != nil {
		return err
	}

	if db.parent == nil {
		for _, child := range db.databaseList() {
			var streams func(string) ([]byte, error)

			if opts.DatabaseStreams != nil {
				streams = opts.DatabaseStreams(child.name)
			}

			csrc, err := child.backupSource(ctx, streams, &done)
			if err != nil {
				return fmt.Errorf("database %s: %w", child.name, err)
			}

			src.Databases = append(src.Databases, backup.Database{Name: child.name, Source: csrc})
		}
	}

	return backup.Write(w, src, opts)
}

// backupSource is what Backup saves of db alone. Each step that holds a
// collection (or a snapshot) adds its release to done.
func (db *DB) backupSource(ctx context.Context, streams func(string) ([]byte, error), done *[]func()) (backup.Source, error) {
	db.swap.RLock()
	*done = append(*done, db.swap.RUnlock)

	defs := db.cat.List()

	entries, err := db.cursors.All()
	if err != nil {
		return backup.Source{}, err
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
			return backup.Source{}, fmt.Errorf("backup: collection %s is not open", d.Name)
		}

		if err := col.enter(); err != nil {
			return backup.Source{}, fmt.Errorf("backup: %s: %w", d.Name, err)
		}

		*done = append(*done, col.leave)

		dir := filepath.Join(db.dir, "collections", d.Name)

		first, has, err := backup.FirstTx(dir)
		if err != nil {
			return backup.Source{}, fmt.Errorf("backup: %s: %w", d.Name, err)
		}

		if !has || first <= 1 {
			m, err := col.eng.WALMark()
			if err != nil {
				return backup.Source{}, fmt.Errorf("backup: %s: %w", d.Name, err)
			}

			marks[d.Name] = markOf(m)

			continue
		}

		m, snap, err := col.eng.WALMarkSnapshot()
		if err != nil {
			return backup.Source{}, fmt.Errorf("backup: %s: %w", d.Name, err)
		}

		*done = append(*done, func() { snap.Close() })

		snaps[d.Name] = backup.Snapshot{Tx: m.Tx, Lines: snapshotLines(ctx, snap)}
	}

	if streams == nil {
		streams = func(name string) ([]byte, error) {
			data, err := os.ReadFile(filepath.Join(db.dir, "collections", name, backup.StreamFile))

			if errors.Is(err, os.ErrNotExist) {
				return nil, nil
			}

			return data, err
		}
	}

	return backup.Source{
		Settings:  db.settings.All(),
		Streams:   streams,
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
	}, nil
}

// databaseList is the other databases, sorted by name.
func (db *DB) databaseList() []*DB {
	var out []*DB

	db.eachDatabase(func(child *DB) { out = append(out, child) })

	return out
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

	db, err := Open(config.Config{DataDir: dataDir, AllowImplicit: true, EncryptionKey: os.Getenv("NDB_ENCRYPTION_KEY"), EncryptionKeyOld: os.Getenv("NDB_ENCRYPTION_KEY_OLD")}, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}

	err = db.Backup(context.Background(), w, opts)

	return errors.Join(err, db.Close())
}
