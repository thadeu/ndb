package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/thadeu/ndb/internal/catalog"
)

// Rename gives a collection a new name: its dir, its catalog entry and the
// positions of its watch consumers move together. A partitioned collection
// moves its partitions with it. With replace, an existing target goes aside
// first, like a drop.
//
// It is crash safe: the operation is written to rename.json before any file
// moves, and every step can run again. A crash in the middle is finished at
// the next start (finishRename), so the data has one name or the other,
// never none.

// ErrExists: the target name is taken.
var ErrExists = errors.New("collection_exists")

const renameFile = "rename.json"

// renameOp is the rename in progress. Stage "aside" sends the target away;
// stage "move" moves the source to the new name.
type renameOp struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Replace bool   `json:"replace"`
	Stage   string `json:"stage"`
}

// Rename renames the collection from to to. Calls in flight on the
// collections end first; new calls wait.
func (db *DB) Rename(from, to string, replace bool) error {
	if err := db.checkRename(from, to, replace); err != nil {
		return err
	}

	for {
		done, err := db.tryRename(from, to, replace)
		if done || err != nil {
			return err
		}
	}
}

// checkRename validates the names before any lock.
func (db *DB) checkRename(from, to string, replace bool) error {
	src, ok := db.cat.Get(from)

	switch {
	case !ok:
		return fmt.Errorf("%w: %s", catalog.ErrUnknown, from)
	case src.PartitionOf != "":
		return fmt.Errorf("%w: %s is a partition of %s: rename %s", catalog.ErrInvalid, from, src.PartitionOf, src.PartitionOf)
	case IsPattern(to) || !catalog.ValidName(to):
		return fmt.Errorf("%w: bad collection name %q", catalog.ErrInvalid, to)
	case from == to:
		return fmt.Errorf("%w: the new name is the current name", catalog.ErrInvalid)
	case src.PartitionBy != "" && len(to) > 53:
		return fmt.Errorf("%w: a partitioned name has at most 53 characters (the period is added)", catalog.ErrInvalid)
	}

	dst, exists := db.cat.Get(to)

	switch {
	case exists && dst.PartitionOf != "":
		return fmt.Errorf("%w: %s is a partition of %s", catalog.ErrInvalid, to, dst.PartitionOf)
	case exists && !replace:
		return fmt.Errorf("%w: %s exists (send replace to put %s in its place)", ErrExists, to, from)
	}

	skip := db.familyOf(from)

	if replace {
		skip = append(skip, db.familyOf(to)...)
	}

	return db.checkTargets(to, src.PartitionBy, skip)
}

// checkTargets checks the new names of a rename or a restore into to,
// before any change: to must not be a partition name of a logical
// collection (400 catalog_invalid), and for a partitioned source (by set),
// no collection may hold a partition name of to (409 collection_exists).
// The names in skip (the source, and what a replace sends to the trash)
// do not count.
func (db *DB) checkTargets(to, by string, skip []string) error {
	if owner, ok := db.cat.PartitionOwner(to); ok && !slices.Contains(skip, owner.Name) {
		return fmt.Errorf("%w: %s is a partition name of %s", catalog.ErrInvalid, to, owner.Name)
	}

	if by == "" {
		return nil
	}

	for _, def := range db.cat.List() {
		suffix, ok := strings.CutPrefix(def.Name, to+"-")

		if ok && catalog.IsPeriod(by, suffix) && !slices.Contains(skip, def.Name) {
			return fmt.Errorf("%w: %s exists and is not a partition of %s: rename or drop it first", ErrExists, def.Name, to)
		}
	}

	return nil
}

// tryRename locks the open collections of both names and renames. It
// reports false when a partition was created meanwhile: the caller tries
// again with the new set.
func (db *DB) tryRename(from, to string, replace bool) (bool, error) {
	names := db.familyOf(from)

	if replace {
		names = append(names, db.familyOf(to)...)
	}

	cols := db.openOf(names)

	db.swap.Lock()
	defer db.swap.Unlock()

	for _, col := range cols {
		col.live.Lock()
		defer col.live.Unlock()
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	// A Close that ran while this call waited for the locks ends it.
	if err := db.checkOpen(); err != nil {
		return false, err
	}

	now := db.familyOf(from)

	if replace {
		now = append(now, db.familyOf(to)...)
	}

	if !slices.Equal(names, now) {
		return false, nil
	}

	if err := db.checkRename(from, to, replace); err != nil {
		return true, err
	}

	for _, col := range cols {
		if col.dropped {
			return true, fmt.Errorf("%w: %s", catalog.ErrUnknown, col.name)
		}
	}

	var errs []error

	for _, col := range cols {
		errs = append(errs, col.close())
		col.dropped = true

		delete(db.cols, col.name)
	}

	if err := errors.Join(errs...); err != nil {
		return true, err
	}

	op := renameOp{From: from, To: to, Replace: replace, Stage: "aside"}

	if err := db.runRename(op); err != nil {
		return true, fmt.Errorf("rename %s: %w (the next start finishes it)", from, err)
	}

	for _, name := range db.familyOf(to) {
		if def, ok := db.cat.Get(name); ok {
			if err := db.attach(def); err != nil {
				return true, err
			}
		}
	}

	return true, nil
}

// familyOf is name and, for a logical collection, its partitions, from the
// catalog, sorted.
func (db *DB) familyOf(name string) []string {
	var out []string

	for _, def := range db.cat.List() {
		if def.Name == name || def.PartitionOf == name {
			out = append(out, def.Name)
		}
	}

	return out
}

// openOf is the open collections among names.
func (db *DB) openOf(names []string) []*Collection {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var out []*Collection

	for _, name := range names {
		if col, ok := db.cols[name]; ok {
			out = append(out, col)
		}
	}

	return out
}

// runRename saves op, runs its stages and removes the file. Every step can
// run again after a crash.
func (db *DB) runRename(op renameOp) error {
	if op.Stage == "aside" {
		if err := db.saveRename(op); err != nil {
			return err
		}

		if op.Replace {
			if err := db.aside(op.To); err != nil {
				return err
			}
		}

		op.Stage = "move"
	}

	if err := db.saveRename(op); err != nil {
		return err
	}

	if err := db.move(op.From, op.To); err != nil {
		return err
	}

	if err := os.Remove(filepath.Join(db.dir, renameFile)); err != nil {
		return err
	}

	return syncDir(db.dir)
}

// aside sends name and its partitions to the trash, with their watch
// positions and catalog entries.
func (db *DB) aside(name string) error {
	for _, n := range db.familyOf(name) {
		dir := filepath.Join(db.dir, "collections", n)

		if _, err := os.Stat(dir); err == nil {
			trash, err := db.toTrash(dir, n)
			if err != nil {
				return err
			}

			def, _ := db.cat.Get(n)

			db.discard(trash, def)
		}

		if err := db.cursors.DeleteCollection(n); err != nil {
			return err
		}

		if err := db.cat.Delete(n); err != nil {
			return err
		}
	}

	return nil
}

// move renames from and its partitions to to. A logical collection gets its
// new definition first and loses the old one last, so the partitions are
// always under a logical name.
func (db *DB) move(from, to string) error {
	src, ok := db.cat.Get(from)

	if ok && src.PartitionBy != "" {
		def := src
		def.Name = to

		if err := db.cat.Put(def); err != nil {
			return err
		}
	}

	for _, def := range db.cat.List() {
		if def.PartitionOf != from {
			continue
		}

		suffix, ok := strings.CutPrefix(def.Name, from+"-")
		if !ok {
			continue
		}

		if err := db.moveOne(def.Name, to+"-"+suffix, to); err != nil {
			return err
		}
	}

	if ok && src.PartitionBy != "" {
		return db.cat.Delete(from)
	}

	return db.moveOne(from, to, "")
}

// moveOne renames the dir, the catalog entry and the watch positions of
// one collection. Each step is skipped when done.
func (db *DB) moveOne(from, to, partitionOf string) error {
	root := filepath.Join(db.dir, "collections")
	src, dst := filepath.Join(root, from), filepath.Join(root, to)

	if _, err := os.Stat(src); err == nil {
		if err := os.Rename(src, dst); err != nil {
			return err
		}

		if err := syncDir(root); err != nil {
			return err
		}
	}

	if err := db.cat.Rename(from, to, partitionOf); err != nil {
		return err
	}

	return db.cursors.Rename(from, to)
}

// saveRename writes op to rename.json atomically.
func (db *DB) saveRename(op renameOp) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}

	path := filepath.Join(db.dir, renameFile)
	tmp := path + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	_, err = f.Write(data)

	if err == nil {
		err = f.Sync()
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return err
	}

	if err := os.Rename(tmp, path); err != nil {
		return err
	}

	return syncDir(db.dir)
}

// finishRename ends a rename that a crash stopped, before the collections
// open.
func (db *DB) finishRename() error {
	data, err := os.ReadFile(filepath.Join(db.dir, renameFile))
	if os.IsNotExist(err) {
		return nil
	}

	if err != nil {
		return err
	}

	var op renameOp

	if err := json.Unmarshal(data, &op); err != nil {
		return fmt.Errorf("%s: %w", renameFile, err)
	}

	if err := db.runRename(op); err != nil {
		return err
	}

	db.log.Warn("finished a rename that a crash stopped", "from", op.From, "to", op.To)

	return nil
}
