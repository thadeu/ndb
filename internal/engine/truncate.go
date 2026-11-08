package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/wal"
)

// Truncate and drop swap the collection dir, so their cost does not depend
// on the number of documents.
//
// Truncate writes <name>~new with one WAL record, the truncate marker, whose
// tx follows the last tx of the collection. Then it moves the old dir to the
// trash, renames <name>~new to <name> and opens it. A crash between the two
// renames is finished at the next start (finishSwaps). The old dir is
// deleted in the background.
//
// Drop moves the dir to the trash, then forgets the watch cursors and the
// catalog entry. A crash before the catalog entry is gone leaves an empty
// collection with that name, never the old data.
//
// Both wait for the calls in flight on the collection (its live lock) and
// for a backup or a blob GC that is running (db.swap).

// ErrConfirm: the confirm value is not the collection name.
var ErrConfirm = errors.New("confirm_required: confirm must be the collection name")

const (
	newSuffix = "~new"
	trashDir  = "trash"
)

// Truncate removes every document of an engine-mode collection and keeps
// its definition and indexes. Watch consumers get one truncate event.
func (db *DB) Truncate(name, confirm string) error {
	col, err := db.swapTarget(name, confirm)
	if err != nil {
		return err
	}

	db.swap.Lock()
	defer db.swap.Unlock()

	col.live.Lock()
	defer col.live.Unlock()

	if col.dropped {
		return fmt.Errorf("%w: %s", catalog.ErrUnknown, name)
	}

	// Close syncs and publishes every record first (relaxed mode too), so
	// LastTx after it is the real last tx.
	if err := col.eng.Close(); err != nil {
		return err
	}

	last := col.eng.LastTx()

	staged := col.dir + newSuffix

	if err := writeMarker(staged, last+1); err != nil {
		return db.reopen(col, err)
	}

	trash, err := db.toTrash(col.dir, name)
	if err != nil {
		os.RemoveAll(staged)

		return db.reopen(col, err)
	}

	if err := os.Rename(staged, col.dir); err != nil {
		return fmt.Errorf("truncate %s: %w (the next start finishes it)", name, err)
	}

	if err := syncDir(filepath.Dir(col.dir)); err != nil {
		return err
	}

	def, _ := db.cat.Get(name)

	db.discard(trash, def)

	return db.reopen(col, nil)
}

// Drop removes an engine-mode collection: its documents, its definition
// and the positions of its watch consumers. A collection declared in
// ndb.yml comes back, empty, at the next start.
func (db *DB) Drop(name, confirm string) error {
	col, err := db.swapTarget(name, confirm)
	if err != nil {
		return err
	}

	db.swap.Lock()
	defer db.swap.Unlock()

	col.live.Lock()
	defer col.live.Unlock()

	if col.dropped {
		return fmt.Errorf("%w: %s", catalog.ErrUnknown, name)
	}

	if err := col.eng.Close(); err != nil {
		return err
	}

	col.dropped = true

	// Under db.mu, an insert that creates the collection again waits until
	// the old dir is in the trash and the catalog entry is gone.
	db.mu.Lock()
	defer db.mu.Unlock()

	delete(db.cols, name)

	trash, err := db.toTrash(col.dir, name)
	if err != nil {
		return err
	}

	def, _ := db.cat.Get(name)

	db.discard(trash, def)

	if err := db.cursors.DeleteCollection(name); err != nil {
		return err
	}

	return db.cat.Delete(name)
}

// swapTarget checks confirm and the write mode, and returns the collection.
func (db *DB) swapTarget(name, confirm string) (*Collection, error) {
	if confirm != name {
		return nil, ErrConfirm
	}

	col, err := db.Get(name)
	if err != nil {
		return nil, err
	}

	if col.eng == nil {
		return nil, ErrDirect
	}

	return col, nil
}

// reopen opens the collection dir again and puts the new engine in col.
// cause, when set, is the error of the swap, returned after the reopen. The
// definition is read here, so an index added during the swap is kept.
func (db *DB) reopen(col *Collection, cause error) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	def, ok := db.cat.Get(col.name)
	if !ok {
		return errors.Join(cause, fmt.Errorf("%w: %s", catalog.ErrUnknown, col.name))
	}

	fresh, err := db.open(def)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("reopen %s: %w", def.Name, err))
	}

	col.eng, col.rd, col.wm = fresh.eng, fresh.rd, fresh.wm

	return cause
}

// writeMarker makes dir with one WAL file that holds the truncate marker.
func writeMarker(dir string, tx uint64) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	line, err := wal.Encode(nil, wal.Record{Tx: tx, TS: time.Now().UTC(), Ops: []wal.Op{{Kind: wal.Trunc}}})
	if err != nil {
		return err
	}

	f, err := os.Create(filepath.Join(dir, fmt.Sprintf("%06d.ndjson", 1)))
	if err != nil {
		return err
	}

	_, err = f.Write(append(line, '\n'))

	if err == nil {
		err = f.Sync()
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return err
	}

	return syncDir(dir)
}

// toTrash moves dir to <data_dir>/trash/<name>.<unix nanoseconds>.
func (db *DB) toTrash(dir, name string) (string, error) {
	root := filepath.Join(db.dir, trashDir)

	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}

	dst := filepath.Join(root, name+"."+strconv.FormatInt(time.Now().UnixNano(), 10))

	if err := os.Rename(dir, dst); err != nil {
		return "", err
	}

	return dst, syncDir(filepath.Dir(dir))
}

// deleteLater removes a trash dir in the background. Close waits for it.
func (db *DB) deleteLater(path string) {
	db.trashWait.Add(1)

	go func() {
		defer db.trashWait.Done()

		if err := os.RemoveAll(path); err != nil {
			db.log.Error("trash delete failed", "path", path, "err", err)
		}
	}()
}

// finishSwaps ends a truncate that a crash stopped. <name>~new next to
// <name> means the old dir was not moved yet: the truncate did not happen
// (it was never acked), so ~new goes. <name>~new alone means the old dir is
// in the trash: the rename to <name> finishes it.
func (db *DB) finishSwaps() error {
	root := filepath.Join(db.dir, "collections")

	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	if err := removeStaged(root); err != nil {
		return err
	}

	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), newSuffix)
		if !ok {
			continue
		}

		staged := filepath.Join(root, e.Name())
		dir := filepath.Join(root, name)

		if _, err := os.Stat(dir); err == nil {
			if err := os.RemoveAll(staged); err != nil {
				return err
			}

			continue
		}

		if err := os.Rename(staged, dir); err != nil {
			return err
		}

		db.log.Warn("finished a truncate that a crash stopped", "collection", name)
	}

	return syncDir(root)
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}

	defer f.Close()

	return f.Sync()
}
