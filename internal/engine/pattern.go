package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"regexp"
	"slices"
	"strings"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/query"
)

// Collection patterns: a name prefix and one "*" at the end (logs-2026-*)
// reads every collection whose name starts with the prefix. Reads only.

// MaxPatternCollections is the most collections one pattern read opens.
const MaxPatternCollections = 256

var prefixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// IsPattern tells if name is a collection pattern and not a name: a name
// never has "*".
func IsPattern(name string) bool {
	return strings.ContainsAny(name, "*,")
}

// PatternPrefix checks a pattern and returns its prefix.
func PatternPrefix(pattern string) (string, error) {
	prefix, ok := strings.CutSuffix(pattern, "*")

	if !ok || !prefixRe.MatchString(prefix) {
		return "", query.NewError("invalid_pattern", "a pattern is a name prefix and one * at the end, like logs-2026-*")
	}

	return prefix, nil
}

// Match lists the collections of a pattern, sorted by name, each once. The
// pattern is a list of items split by commas: a name prefix with a * at the
// end (calls-2026-*), the name of a collection, or the name of a partitioned
// collection (its live partitions). The caller picks the collections; ndb
// does not choose them from the query. A name that does not exist is an
// error: the caller asked for it.
func (db *DB) Match(pattern string) ([]string, error) {
	seen := map[string]bool{}

	var names []string

	for item := range strings.SplitSeq(pattern, ",") {
		matched, err := db.matchItem(strings.TrimSpace(item))
		if err != nil {
			return nil, err
		}

		for _, name := range matched {
			if !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
	}

	if len(names) > MaxPatternCollections {
		return nil, query.NewError("pattern_too_wide", "the pattern matches more than 256 collections")
	}

	slices.Sort(names)

	return names, nil
}

// matchItem is the collections of one item of a pattern list.
func (db *DB) matchItem(item string) ([]string, error) {
	if item == "" {
		return nil, query.NewError("invalid_pattern", "a list of collections has an empty item: names are split by commas, like calls-2026-11,calls-2026-10")
	}

	if strings.Contains(item, "*") {
		return db.matchPrefix(item)
	}

	db.mu.RLock()
	_, open := db.cols[item]
	db.mu.RUnlock()

	if open {
		return []string{item}, nil
	}

	if def, ok := db.cat.Get(item); ok && def.PartitionBy != "" {
		return db.Partitions(def, true, time.Now()), nil
	}

	return nil, fmt.Errorf("%w: %s", catalog.ErrUnknown, item)
}

// matchPrefix is the collections whose name starts with the prefix of a
// pattern item (calls-2026-*).
func (db *DB) matchPrefix(item string) ([]string, error) {
	prefix, err := PatternPrefix(item)
	if err != nil {
		return nil, err
	}

	db.mu.RLock()
	defer db.mu.RUnlock()

	var names []string

	for name := range db.cols {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}

	return names, nil
}

// FindPattern is a find over every collection of the pattern. Each row has
// _coll, the name of its collection.
func (db *DB) FindPattern(ctx context.Context, pattern string, body []byte) (collection.Page, error) {
	return db.readPattern(pattern, func(cols []collection.Named) (collection.Page, error) {
		if len(cols) == 0 {
			q, err := query.Parse(body, query.Options{})
			if err != nil {
				return collection.Page{}, err
			}

			return emptyPage(q.Count), nil
		}

		return collection.FindMany(ctx, pattern, cols, body)
	})
}

// AggregatePattern runs a pipeline over every collection of the pattern, as
// if they were one collection.
func (db *DB) AggregatePattern(ctx context.Context, pattern string, body []byte) (collection.Page, error) {
	return db.readPattern(pattern, func(cols []collection.Named) (collection.Page, error) {
		if len(cols) == 0 {
			p, err := agg.Parse(body, agg.Options{})
			if err != nil {
				return collection.Page{}, err
			}

			return emptyPage(p.Count), nil
		}

		return collection.AggregateMany(ctx, cols, body)
	})
}

// readPattern holds every matched collection for the call, so a truncate or
// a drop waits for it. A collection dropped before the call starts is left
// out.
func (db *DB) readPattern(pattern string, read func([]collection.Named) (collection.Page, error)) (collection.Page, error) {
	names, err := db.Match(pattern)
	if err != nil {
		return collection.Page{}, err
	}

	return db.readNames(names, read)
}

// readNames holds the named collections for the call (see readPattern).
func (db *DB) readNames(names []string, read func([]collection.Named) (collection.Page, error)) (collection.Page, error) {
	cols := make([]collection.Named, 0, len(names))

	for _, name := range names {
		col, err := db.Get(name)
		if err != nil || col.enter() != nil {
			continue
		}

		defer col.leave()

		cols = append(cols, collection.Named{Name: name, C: col.rd.Reader()})
	}

	return read(cols)
}

func emptyPage(count bool) collection.Page {
	page := collection.Page{Plan: "pattern"}

	if count {
		page.Total = new(int)
	}

	return page
}

// FindPartitioned is a find over the live partitions of a logical
// collection. Each row has _coll, the name of its partition.
func (db *DB) FindPartitioned(ctx context.Context, def catalog.Def, body []byte) (collection.Page, error) {
	return db.readNames(db.Partitions(def, true, time.Now()), func(cols []collection.Named) (collection.Page, error) {
		if len(cols) == 0 {
			q, err := query.Parse(body, query.Options{TimeField: def.Time})
			if err != nil {
				return collection.Page{}, err
			}

			return emptyPage(q.Count), nil
		}

		return collection.FindMany(ctx, def.Name, cols, body)
	})
}

// AggregatePartitioned runs a pipeline over the live partitions.
func (db *DB) AggregatePartitioned(ctx context.Context, def catalog.Def, body []byte) (collection.Page, error) {
	return db.readNames(db.Partitions(def, true, time.Now()), func(cols []collection.Named) (collection.Page, error) {
		if len(cols) == 0 {
			p, err := agg.Parse(body, agg.Options{TimeField: def.Time})
			if err != nil {
				return collection.Page{}, err
			}

			return emptyPage(p.Count), nil
		}

		return collection.AggregateMany(ctx, cols, body)
	})
}
