package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/query"
)

// Partitioned collections (partition_by). A logical collection holds only a
// definition; its documents live in partitions named {name}-{period}, one
// per day, week, month or year of the time field. Each partition is a normal
// collection with the definition of the logical one. With retention, a
// partition whose period ended more than retention ago is left out of reads
// and dropped by the partition sweeper.

// PartitionSweepEvery is how often the sweeper drops expired partitions.
var PartitionSweepEvery = time.Minute

// Partitioned returns the definition of a logical partitioned collection.
func (db *DB) Partitioned(name string) (catalog.Def, bool) {
	def, ok := db.cat.Get(name)

	return def, ok && def.PartitionBy != ""
}

// Partitions lists the partitions of a logical collection, oldest first.
// With live, the partitions past the retention are left out.
func (db *DB) Partitions(def catalog.Def, live bool, now time.Time) []string {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var names []string

	for name := range db.cols {
		if pdef, ok := db.cat.Get(name); ok && pdef.PartitionOf == def.Name {
			if !live || !partitionExpired(def, name, now) {
				names = append(names, name)
			}
		}
	}

	slices.Sort(names)

	return names
}

// PartitionFor returns the partition that holds a document by its time
// field, and creates it when it is new.
func (db *DB) PartitionFor(def catalog.Def, doc []byte) (*Collection, error) {
	name, err := PartitionName(def, doc)

	if err != nil {
		return nil, err
	}

	return db.Partition(def, name)
}

// PartitionName is the name of the partition of a document, from its time
// field. It creates nothing.
func PartitionName(def catalog.Def, doc []byte) (string, error) {
	at, err := docTime(def.Time, doc)

	if err != nil {
		return "", err
	}

	return def.Name + "-" + PeriodOf(def.PartitionBy, at), nil
}

// PartitionAt returns the partition of the period of at, created when new.
func (db *DB) PartitionAt(def catalog.Def, at time.Time) (*Collection, error) {
	return db.Partition(def, def.Name+"-"+PeriodOf(def.PartitionBy, at))
}

// Partition returns the partition name of def, created when new. A
// collection with that name that is not a partition of def is
// catalog_conflict: its documents are not in the reads of def.
func (db *DB) Partition(def catalog.Def, name string) (*Collection, error) {
	if err := db.checkPartition(def, name); err != nil {
		return nil, err
	}

	if col, err := db.Get(name); err == nil {
		return col, nil
	}

	if _, err := db.ensure(catalog.PartitionDef(def, name)); err != nil {
		return nil, err
	}

	return db.Get(name)
}

// checkPartition fails when name exists and is not a partition of def.
func (db *DB) checkPartition(def catalog.Def, name string) error {
	pdef, ok := db.cat.Get(name)

	if ok && pdef.PartitionOf != def.Name {
		return fmt.Errorf("%w: %s is a collection, not a partition of %s: rename or drop it to write this period", catalog.ErrConflict, name, def.Name)
	}

	return nil
}

// CheckPartitionInsert tells whether an insert (or an upsert) of docs into
// the partition name of def would pass now, and writes nothing. A
// partition that does not exist yet is checked as an empty one, and is not
// created.
func (db *DB) CheckPartitionInsert(ctx context.Context, def catalog.Def, name string, expireIn time.Duration, upsert bool, docs [][]byte) error {
	if err := db.checkPartition(def, name); err != nil {
		return err
	}

	col, err := db.Get(name)

	if err != nil {
		return collection.CheckNew(catalog.PartitionDef(def, name), expireIn, upsert, docs...)
	}

	return col.CheckInsert(ctx, expireIn, upsert, docs)
}

// EnsurePartitioned declares a logical collection and gives its whole
// definition (indexes, unique, ttl, durability and the other keys) to the
// partitions it already has. A new unique field is checked on every
// partition before any change (409 unique_violation).
func (db *DB) EnsurePartitioned(decl catalog.Def) (catalog.Def, error) {
	if err := checkDecl(decl); err != nil {
		return catalog.Def{}, err
	}

	next, err := db.cat.Merged(decl, catalog.FromCode)

	if err != nil {
		return catalog.Def{}, err
	}

	cur, _ := db.cat.Get(decl.Name)
	added := newUnique(cur, next)
	names := db.Partitions(next, false, time.Now())

	if len(added) > 0 {
		for _, name := range names {
			if err := db.checkUniqueData(name, added); err != nil {
				return catalog.Def{}, err
			}
		}
	}

	def, err := db.ensure(decl)

	if err != nil {
		return catalog.Def{}, err
	}

	for _, name := range names {
		if _, err := db.ensure(catalog.PartitionDef(def, name)); err != nil {
			return catalog.Def{}, err
		}
	}

	return def, nil
}

// DropPartitioned removes every partition, and with drop the logical
// definition too (a truncate keeps it).
func (db *DB) DropPartitioned(def catalog.Def, drop bool) error {
	for _, name := range db.Partitions(def, false, time.Now()) {
		if err := db.Drop(name, name); err != nil {
			return err
		}
	}

	if !drop {
		return nil
	}

	if err := db.keepLogical(def); err != nil {
		return err
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	return db.cat.Delete(def.Name)
}

// SweepPartitions drops the partitions past their retention, and returns
// their names.
func (db *DB) SweepPartitions(now time.Time) ([]string, error) {
	var dropped []string

	for _, def := range db.cat.List() {
		if def.PartitionBy == "" || def.Retention == "" {
			continue
		}

		for _, name := range db.Partitions(def, false, now) {
			if !partitionExpired(def, name, now) {
				continue
			}

			if err := db.Drop(name, name); err != nil {
				return dropped, err
			}

			dropped = append(dropped, name)
		}
	}

	return dropped, nil
}

// sweepPartitionsLoop runs SweepPartitions until the DB closes.
func (db *DB) sweepPartitionsLoop() {
	defer db.gcWait.Done()

	tick := time.NewTicker(PartitionSweepEvery)
	defer tick.Stop()

	for {
		select {
		case <-db.ctx.Done():
			return
		case now := <-tick.C:
			dropped, err := db.SweepPartitions(now)

			if err != nil {
				db.log.Error("partition sweep failed", "err", err)
			}

			for _, name := range dropped {
				db.log.Info("dropped a partition past its retention", "partition", name)
			}
		}
	}
}

// PeriodOf is the partition suffix of at: 2026-11-07, 2026-w45, 2026-11 or
// 2026, in UTC. The names sort by time.
func PeriodOf(by string, at time.Time) string {
	at = at.UTC()

	switch by {
	case "day":
		return at.Format("2006-01-02")
	case "week":
		y, w := at.ISOWeek()

		return fmt.Sprintf("%04d-w%02d", y, w)
	case "year":
		return at.Format("2006")
	}

	return at.Format("2006-01")
}

// periodEnd is when the period of a partition name ends; ok is false for a
// name that is not a partition of def.
func periodEnd(def catalog.Def, name string) (time.Time, bool) {
	suffix, ok := strings.CutPrefix(name, def.Name+"-")
	if !ok {
		return time.Time{}, false
	}

	var (
		start time.Time
		err   error
	)

	switch def.PartitionBy {
	case "day":
		start, err = time.Parse("2006-01-02", suffix)

		return start.AddDate(0, 0, 1), err == nil
	case "week":
		var y, w int

		if _, err := fmt.Sscanf(suffix, "%04d-w%02d", &y, &w); err != nil {
			return time.Time{}, false
		}

		return isoWeekStart(y, w).AddDate(0, 0, 7), true
	case "year":
		start, err = time.Parse("2006", suffix)

		return start.AddDate(1, 0, 0), err == nil
	}

	start, err = time.Parse("2006-01", suffix)

	return start.AddDate(0, 1, 0), err == nil
}

func isoWeekStart(year, week int) time.Time {
	jan4 := time.Date(year, 1, 4, 0, 0, 0, 0, time.UTC)
	monday := jan4.AddDate(0, 0, -((int(jan4.Weekday()) + 6) % 7))

	return monday.AddDate(0, 0, 7*(week-1))
}

// partitionExpired tells if the period of the partition ended more than the
// retention of def ago.
func partitionExpired(def catalog.Def, name string, now time.Time) bool {
	if def.Retention == "" {
		return false
	}

	keep, err := catalog.ParseDuration(def.Retention)
	if err != nil {
		return false
	}

	end, ok := periodEnd(def, name)

	return ok && end.Add(keep).Before(now)
}

// docTime reads the time field of a document (a dot path).
func docTime(field string, doc []byte) (time.Time, error) {
	m, err := query.Decode(doc)
	if err != nil {
		return time.Time{}, query.NewError("invalid_doc", "the document is not a JSON object")
	}

	var v any = m

	for _, part := range strings.Split(field, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			v = nil

			break
		}

		v = obj[part]
	}

	s, ok := v.(string)
	if !ok {
		return time.Time{}, query.NewError("invalid_doc", "a partitioned collection needs the time field %q as an RFC 3339 string in every document", field)
	}

	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, query.NewError("invalid_doc", "the time field %q is not RFC 3339: %v", field, err)
	}

	return at, nil
}

// timeOfOps is the time field set by the operators of an update, for an
// upsert that creates a document in a partitioned collection.
func timeOfOps(field string, ops map[string]any) (time.Time, bool) {
	for _, op := range []string{"$set", "$setOnInsert"} {
		fields, _ := ops[op].(map[string]any)

		if s, ok := fields[field].(string); ok {
			if at, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return at, true
			}
		}
	}

	return time.Time{}, false
}

// TimeOfUpsert is the time an upsert by id uses to pick the partition of a
// new document: the time field of its operators, or now.
func TimeOfUpsert(def catalog.Def, update map[string]any) time.Time {
	if at, ok := timeOfOps(def.Time, update); ok {
		return at
	}

	return time.Now()
}

// CheckInsert tells whether Insert (Upsert with upsert) of docs would pass
// now, and writes nothing.
func (c *Collection) CheckInsert(ctx context.Context, expireIn time.Duration, upsert bool, docs [][]byte) error {
	if !c.engine() {
		return ErrDirect
	}

	if err := c.enter(); err != nil {
		return err
	}

	defer c.leave()

	return c.eng.CheckInsert(ctx, expireIn, upsert, docs...)
}
