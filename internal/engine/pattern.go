package engine

import (
	"context"
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
	return strings.Contains(name, "*")
}

// PatternPrefix checks a pattern and returns its prefix.
func PatternPrefix(pattern string) (string, error) {
	prefix, ok := strings.CutSuffix(pattern, "*")

	if !ok || !prefixRe.MatchString(prefix) {
		return "", query.NewError("invalid_pattern", "a pattern is a name prefix and one * at the end, like logs-2026-*")
	}

	return prefix, nil
}

// Match lists the collections of a pattern, sorted by name.
func (db *DB) Match(pattern string) ([]string, error) {
	prefix, err := PatternPrefix(pattern)
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

	if len(names) > MaxPatternCollections {
		return nil, query.NewError("pattern_too_wide", "the pattern matches more than 256 collections")
	}

	slices.Sort(names)

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
