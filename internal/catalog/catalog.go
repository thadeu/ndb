// Package catalog stores collection definitions and merges declarations
// from code, ndb.yml and implicit creation (plan §5).
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrConflict = errors.New("catalog_conflict")

	// ErrNotStream: "stream": true on a collection that is not a stream.
	ErrNotStream = errors.New("not_a_stream")
	ErrInvalid   = errors.New("catalog_invalid")
	ErrUnknown   = errors.New("unknown_collection")
)

type WriteMode string

const (
	Engine WriteMode = "engine"
	Direct WriteMode = "direct"
)

type Durability string

const (
	Strict  Durability = "strict"
	Relaxed Durability = "relaxed"
)

type Source int

const (
	FromCode Source = iota
	FromYAML
	FromImplicit
)

const maxLineLimit = 16 << 20

// Def is one collection. An empty field in a declaration means "not
// specified": it keeps the current value.
type Def struct {
	Name string `json:"name" yaml:"-"`

	// LegacyID only exists to reject the removed "id" setting with a clear
	// message. The primary key is always _id.
	LegacyID     string     `json:"id,omitempty" yaml:"id"`
	Time         string     `json:"time,omitempty" yaml:"time"`
	Write        WriteMode  `json:"write,omitempty" yaml:"write"`
	Path         string     `json:"path,omitempty" yaml:"path"`
	Indexes      []string   `json:"indexes,omitempty" yaml:"indexes"`
	Unique       []string   `json:"unique,omitempty" yaml:"unique"`
	Durability   Durability `json:"durability,omitempty" yaml:"durability"`
	Retention    string     `json:"retention,omitempty" yaml:"retention"`
	WALRetention string     `json:"wal_retention,omitempty" yaml:"wal_retention"`
	TTL          string     `json:"ttl,omitempty" yaml:"ttl"`
	ExpiresField string     `json:"expires_field,omitempty" yaml:"expires_field"`
	MaxLineSize  int64      `json:"max_line_size,omitempty" yaml:"-"`

	// PITR is the point in time recovery window ("off", "6h", at most 7d).
	// Empty uses the daemon default (pitr in ndb.yml, 1d). A partition uses
	// the window of its logical collection.
	PITR string `json:"pitr,omitempty" yaml:"pitr"`

	// PartitionBy makes a logical collection that splits its documents by
	// the period of the time field (day, week, month, year) into partitions
	// named {name}-{period}. PartitionOf is set on each partition.
	PartitionBy string `json:"partition_by,omitempty" yaml:"partition_by"`
	PartitionOf string `json:"partition_of,omitempty" yaml:"-"`

	BlobThreshold int64 `json:"blob_threshold,omitempty" yaml:"-"`

	// Stream makes an append-only log with consumer groups (docs/dev/
	// streams.md). Its time field is StreamTime; it is set at creation and
	// never changes.
	Stream bool `json:"stream,omitempty" yaml:"stream"`

	// OpsFromYAML: durability/retention were set by ndb.yml, so code
	// declarations cannot change them.
	OpsFromYAML bool `json:"ops_from_yaml,omitempty" yaml:"-"`
}

type Options struct {
	AllowImplicit bool
}

type Catalog struct {
	opts Options
	db   *bolt.DB

	mu   sync.RWMutex
	defs map[string]Def
}

var bucket = []byte("collections")

func Open(path string, opts Options) (*Catalog, error) {
	db, err := bolt.Open(path, 0o644, nil)
	if err != nil {
		return nil, err
	}

	c := &Catalog{opts: opts, db: db, defs: map[string]Def{}}

	if err := c.load(); err != nil {
		db.Close()

		return nil, err
	}

	return c, nil
}

func (c *Catalog) load() error {
	return c.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists(bucket)
		if err != nil {
			return err
		}

		return b.ForEach(func(_, v []byte) error {
			var d Def

			if err := json.Unmarshal(v, &d); err != nil {
				return err
			}

			c.defs[d.Name] = d

			return nil
		})
	})
}

// Snapshot writes a consistent copy of the catalog file (one read
// transaction), while the catalog is in use.
func (c *Catalog) Snapshot(w io.Writer) error {
	return c.db.View(func(tx *bolt.Tx) error {
		_, err := tx.WriteTo(w)

		return err
	})
}

func (c *Catalog) Close() error {
	return c.db.Close()
}

func (c *Catalog) Get(name string) (Def, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	d, ok := c.defs[name]

	return d, ok
}

func (c *Catalog) List() []Def {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]Def, 0, len(c.defs))

	for _, d := range c.defs {
		out = append(out, d)
	}

	slices.SortFunc(out, func(a, b Def) int { return strings.Compare(a.Name, b.Name) })

	return out
}

// Ensure creates the collection or merges decl into it, and returns the
// final definition.
func (c *Catalog) Ensure(decl Def, src Source) (Def, error) {
	if err := validateDecl(decl); err != nil {
		return Def{}, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	next, err := c.merged(decl, src)

	if err != nil {
		return Def{}, err
	}

	return next, c.save(next)
}

// Merged is the definition that Ensure of decl would save, with the same
// checks. It saves nothing.
func (c *Catalog) Merged(decl Def, src Source) (Def, error) {
	if err := validateDecl(decl); err != nil {
		return Def{}, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.merged(decl, src)
}

// merged runs the merge of Ensure. The caller holds c.mu.
func (c *Catalog) merged(decl Def, src Source) (Def, error) {
	cur, exists := c.defs[decl.Name]

	if !exists {
		if err := c.checkNewName(decl); err != nil {
			return Def{}, err
		}
	}

	next := withDefaults(decl, src)

	if exists {
		var err error

		next, err = merge(cur, decl, src)

		if err != nil {
			return Def{}, err
		}
	}

	if err := validateFinal(next); err != nil {
		return Def{}, err
	}

	return next, nil
}

// checkNewName keeps the partition names for the partitions: a new
// collection named {logical}-{period} must be that partition, and a new
// logical collection must not find a collection that holds one of its
// partition names. The caller holds c.mu.
func (c *Catalog) checkNewName(decl Def) error {
	if owner, ok := c.partitionOwner(decl.Name); ok && decl.PartitionOf != owner.Name {
		return fmt.Errorf("%w: %s is a partition name of %s: write to %s, which makes its partitions", ErrConflict, decl.Name, owner.Name, owner.Name)
	}

	if decl.PartitionBy == "" {
		return nil
	}

	for name := range c.defs {
		suffix, ok := strings.CutPrefix(name, decl.Name+"-")

		if ok && IsPeriod(decl.PartitionBy, suffix) {
			return fmt.Errorf("%w: %s exists and would be a partition name of %s: rename or drop it first", ErrConflict, name, decl.Name)
		}
	}

	return nil
}

// PartitionOwner returns the logical collection that name is a partition
// name of ({logical}-{period}), whether or not that partition exists.
func (c *Catalog) PartitionOwner(name string) (Def, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.partitionOwner(name)
}

// partitionOwner is PartitionOwner. The caller holds c.mu.
func (c *Catalog) partitionOwner(name string) (Def, bool) {
	for _, d := range c.defs {
		if d.PartitionBy == "" {
			continue
		}

		if suffix, ok := strings.CutPrefix(name, d.Name+"-"); ok && IsPeriod(d.PartitionBy, suffix) {
			return d, true
		}
	}

	return Def{}, false
}

// PartitionDef is the definition of the partition name of the logical
// collection logical: the same keys, with partition_of set.
func PartitionDef(logical Def, name string) Def {
	part := logical
	part.Name, part.PartitionBy, part.PartitionOf = name, "", logical.Name
	part.OpsFromYAML = false

	return part
}

// IsPeriod tells if suffix is a period of partition_by by, as the
// partition names use it: 2026-11-07 (day), 2026-w45 (week), 2026-11
// (month) or 2026 (year).
func IsPeriod(by, suffix string) bool {
	layout := map[string]string{"day": "2006-01-02", "month": "2006-01", "year": "2006"}[by]

	if by == "week" {
		var y, w int

		if _, err := fmt.Sscanf(suffix, "%04d-w%02d", &y, &w); err != nil {
			return false
		}

		return w >= 1 && w <= 53 && fmt.Sprintf("%04d-w%02d", y, w) == suffix
	}

	if layout == "" {
		return false
	}

	at, err := time.Parse(layout, suffix)

	return err == nil && at.Format(layout) == suffix
}

// EnsureImplicit returns the collection, creating it with defaults when
// implicit creation is allowed.
func (c *Catalog) EnsureImplicit(name string) (Def, error) {
	if d, ok := c.Get(name); ok {
		return d, nil
	}

	// A partition name of a logical collection becomes that partition, so
	// its documents are in the reads of the logical name.
	if owner, ok := c.PartitionOwner(name); ok {
		return c.Ensure(PartitionDef(owner, name), FromCode)
	}

	if !c.opts.AllowImplicit {
		return Def{}, fmt.Errorf("%w: %s", ErrUnknown, name)
	}

	return c.Ensure(Def{Name: name}, FromImplicit)
}

func (c *Catalog) DropIndex(name, field string) (Def, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	d, ok := c.defs[name]
	if !ok {
		return Def{}, fmt.Errorf("%w: %s", ErrUnknown, name)
	}

	d.Indexes = slices.DeleteFunc(slices.Clone(d.Indexes), func(f string) bool { return f == field })

	return d, c.save(d)
}

// Delete removes the definition of name. An unknown name is not an error.
func (c *Catalog) Delete(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Delete([]byte(name))
	})
	if err != nil {
		return err
	}

	delete(c.defs, name)

	return nil
}

// Rename moves the definition of from to the name to, in one transaction.
// A partition gets partitionOf as its logical collection when it is set.
// When from is gone and to exists, the rename was done before: nothing
// changes, so a rename that a crash stopped can run again.
func (c *Catalog) Rename(from, to, partitionOf string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	d, ok := c.defs[from]
	if !ok {
		if _, done := c.defs[to]; done {
			return nil
		}

		return fmt.Errorf("%w: %s", ErrUnknown, from)
	}

	d.Name = to

	if partitionOf != "" {
		d.PartitionOf = partitionOf
	}

	data, err := json.Marshal(d)
	if err != nil {
		return err
	}

	err = c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bucket)

		if err := b.Put([]byte(to), data); err != nil {
			return err
		}

		return b.Delete([]byte(from))
	})
	if err != nil {
		return err
	}

	c.defs[to] = d
	delete(c.defs, from)

	return nil
}

// ValidName tells if name can be a collection name.
func ValidName(name string) bool {
	return nameRe.MatchString(name)
}

// Put stores a definition exactly as given, with no merge and no default.
// A restore uses it to rebuild the catalog from a backup.
func (c *Catalog) Put(d Def) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.save(d)
}

func (c *Catalog) save(d Def) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}

	err = c.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).Put([]byte(d.Name), data)
	})
	if err != nil {
		return err
	}

	c.defs[d.Name] = d

	return nil
}

// StreamTime is the time field of a stream: the ms of each entry ID.
const StreamTime = "_ts"

func withDefaults(d Def, src Source) Def {
	if d.Stream && d.Time == "" {
		d.Time = StreamTime
	}

	switch {
	case d.Write == "" && d.Path != "":
		d.Write = Direct
	case d.Write == "":
		d.Write = Engine
	}

	if d.Durability == "" {
		d.Durability = Strict
	}

	d.Indexes = normIndexes(d.Indexes)
	d.Unique = normIndexes(d.Unique)
	d.OpsFromYAML = src == FromYAML && (d.Durability != Strict || d.Retention != "" || d.WALRetention != "" || d.PITR != "")

	return d
}

func merge(cur, decl Def, src Source) (Def, error) {
	next := cur

	if decl.Stream && !cur.Stream {
		return Def{}, fmt.Errorf("%w: %s is a collection, not a stream", ErrNotStream, cur.Name)
	}

	if decl.Stream && decl.Time == "" {
		decl.Time = StreamTime
	}

	fixed := []struct {
		name      string
		cur, decl string
		set       func(string)
	}{
		{"time", cur.Time, decl.Time, func(v string) { next.Time = v }},
		{"ttl", cur.TTL, decl.TTL, func(v string) { next.TTL = v }},
		{"expires_field", cur.ExpiresField, decl.ExpiresField, func(v string) { next.ExpiresField = v }},
		{"write", string(cur.Write), string(decl.Write), func(v string) { next.Write = WriteMode(v) }},
		{"path", cur.Path, decl.Path, func(v string) { next.Path = v }},
		{"partition_of", cur.PartitionOf, decl.PartitionOf, func(v string) { next.PartitionOf = v }},
	}

	// partition_by is set at creation only: a collection that exists has
	// its documents under its own name, not in partitions.
	if decl.PartitionBy != "" && decl.PartitionBy != cur.PartitionBy {
		if cur.PartitionBy == "" {
			return Def{}, fmt.Errorf("%w: %s exists without partition_by: partition_by is set when the collection is made", ErrConflict, cur.Name)
		}

		return Def{}, fmt.Errorf("%w: %s.partition_by is %q, declared %q", ErrConflict, cur.Name, cur.PartitionBy, decl.PartitionBy)
	}

	for _, f := range fixed {
		if f.decl == "" || f.decl == f.cur {
			continue
		}

		if f.cur != "" {
			return Def{}, fmt.Errorf("%w: %s.%s is %q, declared %q", ErrConflict, cur.Name, f.name, f.cur, f.decl)
		}

		f.set(f.decl)
	}

	next.Indexes = normIndexes(append(slices.Clone(cur.Indexes), decl.Indexes...))
	next.Unique = normIndexes(append(slices.Clone(cur.Unique), decl.Unique...))

	if decl.MaxLineSize != 0 {
		next.MaxLineSize = decl.MaxLineSize
	}

	if decl.BlobThreshold != 0 {
		next.BlobThreshold = decl.BlobThreshold
	}

	mergeOps(&next, decl, src)

	return next, nil
}

// mergeOps applies durability, retention, wal_retention and pitr. ndb.yml wins: they are operator
// decisions.
func mergeOps(next *Def, decl Def, src Source) {
	if src != FromYAML && next.OpsFromYAML {
		return
	}

	if decl.Durability != "" {
		next.Durability = decl.Durability
	}

	if decl.Retention != "" {
		next.Retention = decl.Retention
	}

	if decl.WALRetention != "" {
		next.WALRetention = decl.WALRetention
	}

	if decl.PITR != "" {
		next.PITR = decl.PITR
	}

	if src == FromYAML && (decl.Durability != "" || decl.Retention != "" || decl.WALRetention != "" || decl.PITR != "") {
		next.OpsFromYAML = true
	}
}

func normIndexes(in []string) []string {
	out := slices.Clone(in)

	slices.Sort(out)

	return slices.Compact(out)
}

// PartitionPeriods are the values of partition_by.
var PartitionPeriods = []string{"day", "week", "month", "year"}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// validateDecl checks one declaration on its own.
func validateDecl(d Def) error {
	switch {
	case d.LegacyID != "":
		return fmt.Errorf("%w: the primary key is always _id: declare %q with unique: [%s] (or name the field _id)", ErrInvalid, d.LegacyID, d.LegacyID)
	case !nameRe.MatchString(d.Name):
		return fmt.Errorf("%w: bad collection name %q", ErrInvalid, d.Name)
	case d.Write != "" && d.Write != Engine && d.Write != Direct:
		return fmt.Errorf("%w: write must be engine or direct", ErrInvalid)
	case d.Durability != "" && d.Durability != Strict && d.Durability != Relaxed:
		return fmt.Errorf("%w: durability must be strict or relaxed", ErrInvalid)
	case d.MaxLineSize < 0 || d.MaxLineSize > maxLineLimit:
		return fmt.Errorf("%w: max_line_size must be at most 16 MiB", ErrInvalid)
	case slices.Contains(d.Indexes, "") || slices.Contains(d.Unique, ""):
		return fmt.Errorf("%w: empty index field", ErrInvalid)
	case d.PartitionBy != "" && !slices.Contains(PartitionPeriods, d.PartitionBy):
		return fmt.Errorf("%w: partition_by must be day, week, month or year", ErrInvalid)
	case d.PartitionBy != "" && len(d.Name) > 53:
		return fmt.Errorf("%w: a partitioned name has at most 53 characters (the period is added)", ErrInvalid)
	}

	return validateDurations(d)
}

// validateDurations checks ttl and retention, and that a declaration does
// not set both ttl and expires_field.
func validateDurations(d Def) error {
	if d.TTL != "" && d.ExpiresField != "" {
		return fmt.Errorf("%w: set ttl or expires_field, not both", ErrInvalid)
	}

	for name, v := range map[string]string{"ttl": d.TTL, "retention": d.Retention, "wal_retention": d.WALRetention} {
		if v == "" {
			continue
		}

		if _, err := ParseDuration(v); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalid, name, err)
		}
	}

	if d.PITR != "" {
		if _, err := ParsePITR(d.PITR); err != nil {
			return fmt.Errorf("%w: pitr: %v", ErrInvalid, err)
		}
	}

	return nil
}

// validateFinal checks the definition after defaults and merge.
func validateFinal(d Def) error {
	switch {
	case d.Write == Direct && d.Path == "":
		return fmt.Errorf("%w: direct mode needs a path", ErrInvalid)
	case d.Write == Direct && d.PITR != "" && d.PITR != "off":
		return fmt.Errorf("%w: pitr is for engine mode (the app files of a direct collection are its history)", ErrInvalid)
	case d.Write == Direct && d.WALRetention != "":
		return fmt.Errorf("%w: wal_retention is for engine mode (direct mode has no WAL)", ErrInvalid)
	case d.Write == Engine && d.Path != "":
		return fmt.Errorf("%w: engine mode has no path (ndb owns the files)", ErrInvalid)
	case d.TTL != "" && d.ExpiresField != "":
		return fmt.Errorf("%w: set ttl or expires_field, not both", ErrInvalid)
	case d.TTL != "" && d.Time == "":
		return fmt.Errorf("%w: ttl needs a time field", ErrInvalid)
	case d.PartitionBy != "" && d.Time == "":
		return fmt.Errorf("%w: partition_by needs a time field", ErrInvalid)
	case d.PartitionBy != "" && d.Write == Direct:
		return fmt.Errorf("%w: partition_by is for engine mode", ErrInvalid)
	case d.Stream && (d.Write != Engine || d.Time != StreamTime || len(d.Unique) > 0 || d.PartitionBy != "" || d.TTL != "" || d.ExpiresField != ""):
		return fmt.Errorf("%w: a stream is in engine mode, its time is %s, with no unique, partition_by, ttl or expires_field", ErrInvalid, StreamTime)
	}

	return nil
}
