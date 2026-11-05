package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/collection"
)

// Backup copies a consistent state of the running daemon to dest, an empty or
// new dir that a restore opens as a data dir. It returns the last tx of each
// engine-mode collection in the copy.
//
// What is consistent:
//   - The WAL of each engine collection up to a mark taken between two
//     commits: every write acked before Backup started is in, no write is
//     half in, and the copy is a prefix of the commit order. Commits go on
//     while the files are copied; later ones are not in the copy.
//   - The blobs, copied after the marks, so every blob that a copied WAL
//     line refers to is there (a blob is durable before its line).
//   - catalog.bolt and cursors.bolt as bbolt snapshots. They are taken
//     last and may be a little newer than the WAL marks; a newer catalog
//     only declares collections or indexes, and a cursor ahead of the
//     copied WAL is the caller's to reset.
//
// Left out: segments and meta.bolt. They are derived from the WAL; the
// restored daemon rebuilds them. Direct-mode collections keep their data in
// the apps' own files; only their definition (in the catalog) is copied.
func (db *DB) Backup(ctx context.Context, dest string) (map[string]uint64, error) {
	if err := backup.PrepareDest(dest); err != nil {
		return nil, err
	}

	cols := db.engineCollections()
	marks := map[string]collection.WALMark{}

	for name, col := range cols {
		m, err := col.eng.WALMark()
		if err != nil {
			return nil, fmt.Errorf("backup: %s: %w", name, err)
		}

		marks[name] = m
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	err := backup.CopyTree(filepath.Join(db.dir, "blobs"), filepath.Join(dest, "blobs"), func(rel string) bool {
		return rel == "tmp"
	})
	if err != nil {
		return nil, err
	}

	last := map[string]uint64{}

	for name, m := range marks {
		if err := copyWAL(filepath.Join(db.dir, "collections", name), filepath.Join(dest, "collections", name), m); err != nil {
			return nil, fmt.Errorf("backup: %s: %w", name, err)
		}

		last[name] = m.Tx
	}

	if err := db.snapshotFiles(dest); err != nil {
		return nil, err
	}

	return last, nil
}

func (db *DB) engineCollections() map[string]*Collection {
	db.mu.RLock()
	defer db.mu.RUnlock()

	out := map[string]*Collection{}

	for name, col := range db.cols {
		if col.eng != nil {
			out[name] = col
		}
	}

	return out
}

// copyWAL copies the WAL files of one collection: the rotated ones whole,
// the one at the mark up to its offset, and nothing after.
func copyWAL(src, dst string, m collection.WALMark) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	for _, e := range entries {
		var seq int

		if _, err := fmt.Sscanf(e.Name(), "%06d.ndjson", &seq); err != nil || seq > m.Seq {
			continue
		}

		limit := int64(-1)

		if seq == m.Seq {
			limit = m.Offset
		}

		if err := backup.CopyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name()), limit); err != nil {
			return err
		}
	}

	return nil
}

// snapshotFiles writes catalog.bolt and cursors.bolt into dest.
func (db *DB) snapshotFiles(dest string) error {
	snaps := []struct {
		name string
		take func(*bytes.Buffer) error
	}{
		{"catalog.bolt", func(b *bytes.Buffer) error { return db.cat.Snapshot(b) }},
		{"cursors.bolt", func(b *bytes.Buffer) error { return db.cursors.Snapshot(b) }},
	}

	for _, s := range snaps {
		var buf bytes.Buffer

		if err := s.take(&buf); err != nil {
			return fmt.Errorf("backup: %s: %w", s.name, err)
		}

		if err := backup.WriteFile(filepath.Join(dest, s.name), buf.Bytes()); err != nil {
			return err
		}
	}

	return nil
}
