package server

import (
	"context"
	"net/http"

	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/lookup"
)

// withLookup runs a _search with its lookup key (M18): the body goes to run
// without it, then the rows get the relations. A scoped token must read
// every collection of the lookup.
func (s *Server) withLookup(r *http.Request, db *engine.DB, run func(context.Context, []byte) (collection.Page, error)) func(context.Context, []byte) (collection.Page, error) {
	return func(ctx context.Context, body []byte) (collection.Page, error) {
		rest, specs, added, err := lookup.Split(body)
		if err != nil || len(specs) == 0 {
			if err != nil {
				return collection.Page{}, err
			}

			return run(ctx, body)
		}

		for _, name := range targets(specs) {
			if err := s.mayAlso(r, name, accessRead); err != nil {
				return collection.Page{}, err
			}
		}

		page, err := run(ctx, rest)
		if err != nil {
			return page, err
		}

		if page.Rows, err = db.Lookup(ctx, page.Rows, specs); err != nil {
			return collection.Page{}, err
		}

		for i := range page.Rows {
			page.Rows[i] = lookup.Without(page.Rows[i], added)
		}

		return page, nil
	}
}

// targets are the collections a lookup reads, nested ones included.
func targets(specs []lookup.Spec) []string {
	var out []string

	for _, s := range specs {
		out = append(out, s.From)
		out = append(out, targets(s.Nested)...)
	}

	return out
}
