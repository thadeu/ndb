package collection

import (
	"encoding/json"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/recovery"
	"github.com/thadeu/ndb/internal/segment"
)

// WriteSnapshot makes dir (a collection dir with no data yet) start from the
// given rows as if a seal had covered every tx up to tx: one segment and a
// checkpoint at WAL file 0, offset 0. The WAL that follows must start at tx+1.
// A restore uses it for a backup taken when the WAL had lost its first files.
func WriteSnapshot(fsys fs.FS, dir string, def catalog.Def, tx uint64, rows []segment.Row) error {
	s := &sealer{dir: dir, fs: fsys, enc: segment.NewWriter()}

	if err := fsys.MkdirAll(filepath.Join(dir, segmentDir)); err != nil {
		return err
	}

	db, err := bolt.Open(filepath.Join(dir, metaFile), 0o644, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return err
	}

	defer db.Close()

	s.meta = db

	cp := checkpoint{Pos: recovery.Position{Tx: tx}, Next: 1, Format: formatSidecars}

	if len(rows) == 0 {
		return db.Update(func(t *bolt.Tx) error {
			b, err := t.CreateBucketIfNotExists(bucketCheckpoint)
			if err != nil {
				return err
			}

			raw, err := json.Marshal(cp)
			if err != nil {
				return err
			}

			return b.Put(keyLive, raw)
		})
	}

	const name = "000000.parquet"

	info, err := segment.PublishRows(fsys, s.path(name), segment.RowsOf(rows), def.Indexes)
	if err != nil {
		return err
	}

	c := &Collection{def: def, seal: s}

	if err := c.writeSidecars(s.path(name), c.declaredFields(), snapRows(rows)); err != nil {
		return err
	}

	live := newBitset(len(rows))

	for i, r := range rows {
		if !r.Del {
			live.set(i)
		}
	}

	cp.Segments = []string{name}
	cp.Format = formatSidecars

	return s.commit(info, name, cp, map[string][]byte{name: live.bytes()})
}
