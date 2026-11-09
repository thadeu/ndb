package collection

import (
	"context"

	"github.com/thadeu/ndb/internal/agg"
)

// AggregateFind runs an aggregate body with no $group through find, the
// read that the pipeline is (see agg.Find). ok is false when the pipeline
// has a $group: the caller runs it as an aggregate.
func AggregateFind(ctx context.Context, body []byte, find func(context.Context, []byte) (Page, error)) (page Page, ok bool, err error) {
	f, ok, err := agg.ParseFind(body)
	if !ok || err != nil {
		return Page{}, ok, err
	}

	pg, err := f.Page()
	if err != nil {
		return Page{}, true, err
	}

	page, err = find(ctx, pg.Body)
	if err != nil {
		return page, true, err
	}

	page.Rows, page.Next, page.Total, err = pg.Finish(page.Rows, page.Next, page.Total)

	return page, true, err
}
