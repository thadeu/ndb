// Package catalog stores collection definitions and merges declarations
// from code, ndb.yml and implicit creation (plan §5).
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	bolt "go.etcd.io/bbolt"
)

var (
	ErrConflict = errors.New("catalog_conflict")
	ErrInvalid  = errors.New("catalog_invalid")
	ErrUnknown  = errors.New("unknown_collection")
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
	Name          string     `json:"name" yaml:"-"`
	ID            string     `json:"id,omitempty" yaml:"id"`
	Time          string     `json:"time,omitempty" yaml:"time"`
	Write         WriteMode  `json:"write,omitempty" yaml:"write"`
	Path          string     `json:"path,omitempty" yaml:"path"`
	Indexes       []string   `json:"indexes,omitempty" yaml:"indexes"`
	Unique        []string   `json:"unique,omitempty" yaml:"unique"`
	Durability    Durability `json:"durability,omitempty" yaml:"durability"`
	Retention     string     `json:"retention,omitempty" yaml:"retention"`
	MaxLineSize   int64      `json:"max_line_size,omitempty" yaml:"-"`
	BlobThreshold int64      `json:"blob_threshold,omitempty" yaml:"-"`

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

	cur, exists := c.defs[decl.Name]

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

	return next, c.save(next)
}

// EnsureImplicit returns the collection, creating it with defaults when
// implicit creation is allowed.
func (c *Catalog) EnsureImplicit(name string) (Def, error) {
	if d, ok := c.Get(name); ok {
		return d, nil
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

func withDefaults(d Def, src Source) Def {
	if d.ID == "" {
		d.ID = "_id"
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
	d.OpsFromYAML = src == FromYAML && (d.Durability != Strict || d.Retention != "")

	return d
}

func merge(cur, decl Def, src Source) (Def, error) {
	next := cur

	fixed := []struct {
		name      string
		cur, decl string
		set       func(string)
	}{
		{"id", cur.ID, decl.ID, func(v string) { next.ID = v }},
		{"time", cur.Time, decl.Time, func(v string) { next.Time = v }},
		{"write", string(cur.Write), string(decl.Write), func(v string) { next.Write = WriteMode(v) }},
		{"path", cur.Path, decl.Path, func(v string) { next.Path = v }},
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

// mergeOps applies durability/retention. ndb.yml wins: they are operator
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

	if src == FromYAML && (decl.Durability != "" || decl.Retention != "") {
		next.OpsFromYAML = true
	}
}

func normIndexes(in []string) []string {
	out := slices.Clone(in)

	slices.Sort(out)

	return slices.Compact(out)
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// validateDecl checks one declaration on its own.
func validateDecl(d Def) error {
	switch {
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
	}

	return nil
}

// validateFinal checks the definition after defaults and merge.
func validateFinal(d Def) error {
	switch {
	case d.Write == Direct && d.Path == "":
		return fmt.Errorf("%w: direct mode needs a path", ErrInvalid)
	case d.Write == Engine && d.Path != "":
		return fmt.Errorf("%w: engine mode has no path (ndb owns the files)", ErrInvalid)
	}

	return nil
}
