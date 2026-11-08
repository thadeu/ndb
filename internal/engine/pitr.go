package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/diskusage"
)

// Point in time recovery (M17). Each engine collection with a window gets a
// snapshot when it has none, and a new one when the newest is older than
// the window (at most a day) and there were writes since. The snapshots
// that no instant of the window needs are deleted. With the WAL kept from
// the oldest snapshot on, a restore rebuilds the collection at any instant
// of the window into a new collection.
//
// Truncate, drop and rename with replace move the old dir to the trash; with
// a window it stays there for the window, with trash.json (its definition
// and when it went), so a restore can read it.

// PITRTick is how often the snapshot loop runs.
var PITRTick = time.Minute

// ErrOutOfWindow: the instant is not in the PITR window of the collection.
var ErrOutOfWindow = collection.ErrOutOfWindow

// ErrPITROff: the collection has no PITR window.
var ErrPITROff = errors.New("pitr_off")

const (
	trashMeta     = "trash.json"
	restoreSuffix = "~restore"
	maxSnapEvery  = 24 * time.Hour
)

// trashEntry is a dir in the trash, as trash.json describes it.
type trashEntry struct {
	Def    catalog.Def   `json:"def"`
	At     time.Time     `json:"at"`
	Window time.Duration `json:"window"`

	dir string
}

// window is the PITR window of a collection: its pitr, or the one of its
// logical collection, or the daemon default. 0 is off. Direct collections
// have none.
func (db *DB) window(def catalog.Def) time.Duration {
	if def.Write == catalog.Direct {
		return 0
	}

	if def.PartitionOf != "" {
		if logical, ok := db.cat.Get(def.PartitionOf); ok {
			def = logical
		}
	}

	s := def.PITR

	if s == "" {
		s = db.pitr
	}

	d, err := catalog.ParsePITR(s)
	if err != nil {
		return 0
	}

	return d
}

func pitrDefault(cfg config.Config) string {
	if cfg.PITR == "" {
		return config.DefaultPITR
	}

	return cfg.PITR
}

// pitrLoop runs SnapshotPITR until the DB closes.
func (db *DB) pitrLoop() {
	defer db.gcWait.Done()

	tick := time.NewTicker(PITRTick)
	defer tick.Stop()

	for {
		select {
		case <-db.ctx.Done():
			return
		case now := <-tick.C:
			if err := db.SnapshotPITR(now); err != nil && db.ctx.Err() == nil {
				db.log.Error("pitr snapshot failed", "err", err)
			}
		}
	}
}

// SnapshotPITR takes the snapshots that are due, deletes the ones the
// windows do not need, and empties the trash entries past their window.
func (db *DB) SnapshotPITR(now time.Time) error {
	db.swap.RLock()
	defer db.swap.RUnlock()

	db.pitrMu.Lock()
	defer db.pitrMu.Unlock()

	var errs []error

	for _, col := range db.engineList() {
		errs = append(errs, db.snapshotOne(col, now))
	}

	errs = append(errs, db.sweepTrash(now))

	return errors.Join(errs...)
}

// engineList is the open engine collections.
func (db *DB) engineList() []*Collection {
	db.mu.RLock()
	defer db.mu.RUnlock()

	var out []*Collection

	for _, col := range db.cols {
		if col.engine() {
			out = append(out, col)
		}
	}

	return out
}

func (db *DB) snapshotOne(col *Collection, now time.Time) error {
	def, ok := db.cat.Get(col.name)
	if !ok {
		return nil
	}

	win := db.window(def)

	if tx, asleep := col.restingTx(); asleep {
		snaps, err := collection.Snaps(col.dir)
		if err != nil {
			return err
		}

		if win == 0 {
			return removeSnaps(snaps)
		}

		if !snapDue(snaps, tx, now, min(win, maxSnapEvery)) {
			return removeSnaps(unneeded(snaps, now.Add(-win)))
		}
	}

	if err := col.enter(); err != nil {
		return nil
	}

	defer col.leave()

	snaps, err := collection.Snaps(col.dir)
	if err != nil {
		return err
	}

	if win == 0 {
		return removeSnaps(snaps)
	}

	if snapDue(snaps, col.eng.LastTx(), now, min(win, maxSnapEvery)) {
		if _, err := col.eng.PITRSnapshot(now); err != nil {
			return fmt.Errorf("%s: %w", col.name, err)
		}

		if snaps, err = collection.Snaps(col.dir); err != nil {
			return err
		}
	}

	return removeSnaps(unneeded(snaps, now.Add(-win)))
}

// snapDue: no snapshot yet (and something to keep), or the newest is older
// than every and there were writes since.
func snapDue(snaps []collection.Snap, last uint64, now time.Time, every time.Duration) bool {
	if len(snaps) == 0 {
		return last > 0
	}

	newest := snaps[len(snaps)-1]

	return last > newest.Tx && now.Sub(newest.At) >= every
}

// unneeded are the snapshots before the newest one at or before from: no
// instant after from starts from them.
func unneeded(snaps []collection.Snap, from time.Time) []collection.Snap {
	for i := len(snaps) - 1; i > 0; i-- {
		if !snaps[i].At.After(from) {
			return snaps[:i]
		}
	}

	return nil
}

func removeSnaps(snaps []collection.Snap) error {
	for _, s := range snaps {
		if err := os.RemoveAll(s.Dir); err != nil {
			return err
		}
	}

	return nil
}

// discard deals with a dir that went to the trash: with a window it stays
// for the window, else it is deleted in the background.
func (db *DB) discard(path string, def catalog.Def) {
	win := db.window(def)

	if win == 0 {
		db.deleteLater(path)

		return
	}

	data, err := json.Marshal(trashEntry{Def: def, At: time.Now().UTC(), Window: win})

	if err == nil {
		err = os.WriteFile(filepath.Join(path, trashMeta), data, 0o644)
	}

	if err != nil {
		db.log.Error("trash: cannot keep for pitr, deleting", "path", path, "err", err)
		db.deleteLater(path)
	}
}

// keepLogical puts the definition of a dropped logical collection in the
// trash, alone, for its window: a restore needs it to find the partitions.
func (db *DB) keepLogical(def catalog.Def) error {
	if db.window(def) == 0 {
		return nil
	}

	dir := filepath.Join(db.dir, trashDir, def.Name+"."+strconv.FormatInt(time.Now().UnixNano(), 10))

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	db.discard(dir, def)

	return nil
}

// trashed lists the trash entries kept for a window, oldest first.
func (db *DB) trashed() []trashEntry {
	root := filepath.Join(db.dir, trashDir)

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var out []trashEntry

	for _, e := range entries {
		dir := filepath.Join(root, e.Name())

		data, err := os.ReadFile(filepath.Join(dir, trashMeta))
		if err != nil {
			continue
		}

		var t trashEntry

		if json.Unmarshal(data, &t) == nil {
			t.dir = dir
			out = append(out, t)
		}
	}

	slices.SortFunc(out, func(a, b trashEntry) int { return a.At.Compare(b.At) })

	return out
}

// sweepTrash deletes the trash entries past their window, and the ones with
// no trash.json (a delete that a restart stopped).
func (db *DB) sweepTrash(now time.Time) error {
	root := filepath.Join(db.dir, trashDir)

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	kept := map[string]bool{}

	for _, t := range db.trashed() {
		if now.Sub(t.At) <= t.Window {
			kept[t.dir] = true
		}
	}

	for _, e := range entries {
		if dir := filepath.Join(root, e.Name()); !kept[dir] {
			db.deleteLater(dir)
		}
	}

	return nil
}

// source is the collection dir that held name at until: the trash entry
// that left soonest after until, or the live dir.
func (db *DB) source(name string, until time.Time) (string, catalog.Def, bool) {
	for _, t := range db.trashed() {
		if t.Def.Name == name && t.At.After(until) {
			return t.dir, t.Def, true
		}
	}

	def, ok := db.cat.Get(name)
	if !ok {
		return "", catalog.Def{}, false
	}

	return filepath.Join(db.dir, "collections", name), def, true
}

// lastDef is the definition of name now, or the one of its newest trash
// entry when it was dropped.
func (db *DB) lastDef(name string) (catalog.Def, bool) {
	if def, ok := db.cat.Get(name); ok {
		return def, true
	}

	trash := db.trashed()

	for i := len(trash) - 1; i >= 0; i-- {
		if trash[i].Def.Name == name {
			return trash[i].Def, true
		}
	}

	return catalog.Def{}, false
}

// Restore makes the collection into hold name as it was at until. name may
// be dropped or replaced, while it is in the window. A partitioned
// collection restores its partitions.
func (db *DB) Restore(name, into string, until, now time.Time) error {
	def, ok := db.lastDef(name)

	switch {
	case !ok:
		return fmt.Errorf("%w: %s", catalog.ErrUnknown, name)
	case def.PartitionOf != "":
		return fmt.Errorf("%w: %s is a partition of %s: restore %s", catalog.ErrInvalid, name, def.PartitionOf, def.PartitionOf)
	case IsPattern(into) || !catalog.ValidName(into) || into == name:
		return fmt.Errorf("%w: bad collection name %q", catalog.ErrInvalid, into)
	}

	win := db.window(def)

	switch {
	case win == 0:
		return fmt.Errorf("%w: %s has no PITR window", ErrPITROff, name)
	case until.After(now):
		return fmt.Errorf("%w: %s is in the future", ErrOutOfWindow, until.UTC().Format(time.RFC3339))
	case until.Before(now.Add(-win)):
		return fmt.Errorf("%w: the window of %s starts at %s", ErrOutOfWindow, name, now.Add(-win).UTC().Format(time.RFC3339))
	}

	if _, taken := db.cat.Get(into); taken {
		return fmt.Errorf("%w: %s", ErrExists, into)
	}

	db.swap.RLock()
	defer db.swap.RUnlock()

	db.pitrMu.Lock()
	defer db.pitrMu.Unlock()

	plan, err := db.restorePlan(def, into, until)
	if err != nil {
		return err
	}

	defer func() {
		for _, p := range plan {
			os.RemoveAll(p.staged)
		}
	}()

	for _, p := range plan {
		if p.src == "" {
			continue
		}

		if err := collection.BuildPITR(p.src, p.staged, until); err != nil {
			return fmt.Errorf("%s: %w", p.def.Name, err)
		}
	}

	return db.commitRestore(plan)
}

// restoreItem is one collection a restore makes: its definition, the dir it
// reads (empty for a logical collection) and the dir it builds.
type restoreItem struct {
	def    catalog.Def
	src    string
	staged string
}

func (db *DB) restorePlan(def catalog.Def, into string, until time.Time) ([]restoreItem, error) {
	root := filepath.Join(db.dir, "collections")

	if def.PartitionBy == "" {
		src, _, ok := db.source(def.Name, until)
		if !ok {
			return nil, fmt.Errorf("%w: %s", catalog.ErrUnknown, def.Name)
		}

		out := def
		out.Name = into

		return []restoreItem{{def: out, src: src, staged: filepath.Join(root, into+restoreSuffix)}}, nil
	}

	logical := def
	logical.Name = into

	plan := []restoreItem{{def: logical}}

	for _, name := range db.partitionNames(def) {
		if start, ok := periodStart(def, name); !ok || start.After(until) {
			continue
		}

		src, pdef, ok := db.source(name, until)
		if !ok {
			continue
		}

		part := pdef
		part.Name = into + strings.TrimPrefix(name, def.Name)
		part.PartitionOf = into

		plan = append(plan, restoreItem{def: part, src: src, staged: filepath.Join(root, part.Name+restoreSuffix)})
	}

	return plan, nil
}

// partitionNames are the partitions of a logical collection, live or in the
// trash.
func (db *DB) partitionNames(def catalog.Def) []string {
	seen := map[string]bool{}

	for _, d := range db.cat.List() {
		if d.PartitionOf == def.Name {
			seen[d.Name] = true
		}
	}

	for _, t := range db.trashed() {
		if t.Def.PartitionOf == def.Name {
			seen[t.Def.Name] = true
		}
	}

	names := make([]string, 0, len(seen))

	for n := range seen {
		names = append(names, n)
	}

	slices.Sort(names)

	return names
}

// commitRestore gives the built dirs their names and opens them. The
// catalog entries go first: a crash after them leaves empty collections,
// never data under a name the catalog does not know.
func (db *DB) commitRestore(plan []restoreItem) error {
	db.mu.Lock()
	defer db.mu.Unlock()

	for _, p := range plan {
		if _, taken := db.cat.Get(p.def.Name); taken {
			return fmt.Errorf("%w: %s", ErrExists, p.def.Name)
		}
	}

	root := filepath.Join(db.dir, "collections")

	for _, p := range plan {
		if err := db.cat.Put(p.def); err != nil {
			return err
		}

		if p.src == "" {
			continue
		}

		if err := os.Rename(p.staged, filepath.Join(root, p.def.Name)); err != nil {
			return err
		}
	}

	if err := syncDir(root); err != nil {
		return err
	}

	for _, p := range plan {
		if err := db.attach(p.def); err != nil {
			return fmt.Errorf("open %s: %w", p.def.Name, err)
		}
	}

	return nil
}

// removeStaged deletes the dirs of a restore that a crash stopped.
func removeStaged(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), restoreSuffix) {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				return err
			}
		}
	}

	return nil
}

// PITRInfo is the window of a collection: the oldest and newest instant a
// restore can reach, and the disk its snapshots and trash hold apart from
// the live files.
type PITRInfo struct {
	Collection string    `json:"collection"`
	Window     string    `json:"window"`
	Oldest     time.Time `json:"oldest,omitzero"`
	Newest     time.Time `json:"newest,omitzero"`
	Snapshots  int       `json:"snapshots"`
	Bytes      int64     `json:"bytes"`
	Dropped    bool      `json:"dropped,omitempty"`
}

// PITR reports the window of name, which may be dropped.
func (db *DB) PITR(name string, now time.Time) (PITRInfo, error) {
	def, ok := db.lastDef(name)
	if !ok {
		return PITRInfo{}, fmt.Errorf("%w: %s", catalog.ErrUnknown, name)
	}

	win := db.window(def)
	info := PITRInfo{Collection: name, Window: "off"}

	if win == 0 {
		return info, nil
	}

	info.Window = windowText(win)
	_, live := db.cat.Get(name)
	info.Dropped = !live

	dirs := db.familyDirs(def)
	oldest := time.Time{}

	for _, dir := range dirs {
		snaps, _ := collection.Snaps(dir)
		info.Snapshots += len(snaps)
		info.Bytes += extraBytes(dir)

		if at, ok := collection.Oldest(dir); ok && (oldest.IsZero() || at.Before(oldest)) {
			oldest = at
		}
	}

	if oldest.IsZero() {
		return info, nil
	}

	info.Oldest = maxTime(oldest, now.Add(-win)).UTC()
	info.Newest = now.UTC()

	if !live {
		for _, t := range db.trashed() {
			if t.Def.Name == name {
				info.Newest = t.At
			}
		}
	}

	return info, nil
}

// familyDirs are the dirs of name and its partitions, live and in the trash.
func (db *DB) familyDirs(def catalog.Def) []string {
	names := []string{def.Name}

	if def.PartitionBy != "" {
		names = db.partitionNames(def)
	}

	var out []string

	for _, name := range names {
		if _, ok := db.cat.Get(name); ok {
			out = append(out, filepath.Join(db.dir, "collections", name))
		}

		for _, t := range db.trashed() {
			if t.Def.Name == name {
				out = append(out, t.dir)
			}
		}
	}

	return out
}

// extraBytes is what dir holds for PITR beyond its live files: its pitr
// dir less the links to live segments, or all of it when it is in the trash.
func extraBytes(dir string) int64 {
	if strings.Contains(dir, string(filepath.Separator)+trashDir+string(filepath.Separator)) {
		return diskusage.UniqueSize(dir, "")
	}

	return diskusage.UniqueSize(filepath.Join(dir, collection.PITRDir), filepath.Join(dir, "segments"))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}

	return b
}

// periodStart is when the period of a partition name starts.
func periodStart(def catalog.Def, name string) (time.Time, bool) {
	end, ok := periodEnd(def, name)
	if !ok {
		return time.Time{}, false
	}

	switch def.PartitionBy {
	case "day":
		return end.AddDate(0, 0, -1), true
	case "week":
		return end.AddDate(0, 0, -7), true
	case "year":
		return end.AddDate(-1, 0, 0), true
	}

	return end.AddDate(0, -1, 0), true
}

// AllPITR is the window of every collection that has one, and of the
// dropped ones still in the trash, sorted by name. Partitions are left out:
// their logical collection stands for them.
func (db *DB) AllPITR(now time.Time) []PITRInfo {
	seen := map[string]bool{}

	var out []PITRInfo

	add := func(def catalog.Def) {
		if seen[def.Name] || def.PartitionOf != "" || db.window(def) == 0 {
			return
		}

		seen[def.Name] = true

		if info, err := db.PITR(def.Name, now); err == nil {
			out = append(out, info)
		}
	}

	for _, def := range db.cat.List() {
		add(def)
	}

	for _, t := range db.trashed() {
		add(t.Def)
	}

	slices.SortFunc(out, func(a, b PITRInfo) int { return strings.Compare(a.Collection, b.Collection) })

	return out
}

// PITRDefault is the daemon PITR window ("off", "1d").
func (db *DB) PITRDefault() string {
	return db.pitr
}

// windowText writes a window the way ndb.yml does: 1d, 6h, 90m.
func windowText(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	}

	return d.String()
}
