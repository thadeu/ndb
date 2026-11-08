package collection

import (
	"context"
	"slices"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/segment"
)

// Aggregate (plan §10.2, §11.3). The leading $match goes through the same
// planner as a find: an id or index plan gives candidate entries, a scan
// gives the memtable entries and the live rows of each segment (scan.go).
// Each unit (the candidates, one segment) fills its own group table on the
// bounded pool, and the tables are merged in unit order.
//
// The row engine (candidates, memtable) reads the values of the group paths
// from the decoded document. The column engine (segments) reads only the
// columns of the where, sort and group paths, and falls back to _raw for a
// row whose cell cannot answer (null, missing, mixed types, objects). Both
// feed agg.Table.Add with the same values, so they give the same rows.

// SetGroupLimit sets the memory of the group tables of one aggregate, in
// bytes. Zero or less is the default (256 MiB).
func (c *Collection) SetGroupLimit(n int64) {
	c.groupLimit.Store(n)
}

// ParseAggregate parses an aggregate body with the collection time field.
func (c *Collection) ParseAggregate(body []byte) (*agg.Pipeline, error) {
	p, err := agg.Parse(body, agg.Options{TimeField: c.def.Time, ExpiresField: c.def.ExpiresField})
	if err != nil {
		return nil, err
	}

	return p, c.hide(p.Query)
}

// aggPart is the result of one unit.
type aggPart struct {
	tbl     *agg.Table
	matched int
	ex      Explain
}

// Aggregate runs a pipeline. The answer is a Page: the rows are the output
// rows, Total is set with count: true.
func (c *Collection) Aggregate(ctx context.Context, p *agg.Pipeline) (Page, error) {
	if err := p.CheckCursor(); err != nil {
		return Page{}, err
	}

	part, plan, err := c.aggTable(ctx, p, c.budget())
	if err != nil {
		return Page{}, err
	}

	return finishAgg(p, part, plan)
}

// finishAgg runs the stages after the group stage on the merged table.
func finishAgg(p *agg.Pipeline, part aggPart, plan string) (Page, error) {
	res, err := p.Finish(part.tbl.Rows())
	if err != nil {
		return Page{}, err
	}

	page := Page{Rows: res.Rows, Next: res.Next, Plan: plan, Examined: part.ex.RowsRead}

	if p.Count {
		page.Total = &res.Total
	}

	if p.Explain {
		ex := part.ex
		ex.Plan, ex.RowsMatched = plan, part.matched
		page.Explain = &ex
	}

	return page, nil
}

// budget is the memory budget of the group tables of one aggregate.
func (c *Collection) budget() *agg.Budget {
	limit := c.groupLimit.Load()

	if limit <= 0 {
		limit = defaultGroupLimit
	}

	return agg.NewBudget(limit)
}

// aggTable runs the group stage of p over the collection: one table with
// the groups of every unit, and the plan name.
func (c *Collection) aggTable(ctx context.Context, p *agg.Pipeline, budget *agg.Budget) (aggPart, string, error) {
	src := c.source(p.Query)

	defer src.release()

	parts, err := runPool(ctx, c.scanWorkers, 1+len(src.segs), func(i int) (aggPart, error) {
		if i == 0 {
			return c.aggCands(ctx, p, src, budget)
		}

		return c.aggSegment(ctx, p, src.segs[i-1], budget)
	})
	if err != nil {
		return aggPart{}, "", err
	}

	tbl, matched, ex, err := mergeAgg(parts)
	if err != nil {
		return aggPart{}, "", err
	}

	return aggPart{tbl: tbl, matched: matched, ex: ex}, src.plan, nil
}

func mergeAgg(parts []aggPart) (*agg.Table, int, Explain, error) {
	var (
		matched int
		ex      Explain
	)

	base := parts[0].tbl

	for i, part := range parts {
		matched += part.matched
		ex.add(part.ex)

		if i == 0 {
			continue
		}

		if err := base.Merge(part.tbl); err != nil {
			return nil, 0, Explain{}, err
		}
	}

	return base, matched, ex, nil
}

// aggCands is the row engine: it matches the candidate entries and adds the
// documents that match.
func (c *Collection) aggCands(ctx context.Context, p *agg.Pipeline, src source, budget *agg.Budget) (aggPart, error) {
	part := aggPart{tbl: p.NewTable(budget)}
	q := p.Query

	for i, cd := range src.cands {
		if i&1023 == 0 && ctx.Err() != nil {
			return aggPart{}, ctx.Err()
		}

		part.ex.RowsRead++

		_, m, err := c.docOf(cd.e, false, &part.ex)
		if err != nil {
			return aggPart{}, err
		}

		if m == nil || !q.Match(m) {
			continue
		}

		part.matched++

		pos := query.Pos{Keys: q.SortKeys(m), ID: cd.id, Seq: cd.e.seq}

		if err := part.tbl.Add(p.Values(m), pos); err != nil {
			return aggPart{}, err
		}
	}

	return part, nil
}

// aggSegment is the column engine for one segment. It skips row groups like
// scanSegment does, and reads the where, sort and group columns of the rest.
func (c *Collection) aggSegment(ctx context.Context, p *agg.Pipeline, s segSnap, budget *agg.Budget) (aggPart, error) {
	q := p.Query
	conds := q.Indexable()
	q = visibleQuery(q, s, c.hideBounds(c.clock()))
	sc := newSegScan(q, s.r, query.Pos{}, false)

	groupPaths := p.Paths()
	at := make([]int, len(groupPaths))

	for k, path := range groupPaths {
		at[k] = sc.index(path)
	}

	sc.paths = pathsByIndex(sc.at)

	part := aggPart{tbl: p.NewTable(budget)}
	vals := make([]query.Value, len(groupPaths))

	for gi, g := range s.groups {
		if err := ctx.Err(); err != nil {
			return aggPart{}, err
		}

		if liveIn(s.live, g) == 0 || pruned(s.r, gi, conds) || timePruned(q, s.r, gi, conds) {
			part.ex.RowGroupsSkipped++

			continue
		}

		part.ex.RowGroupsRead++

		rows, err := s.r.ReadGroup(gi, sc.paths)
		if err != nil {
			return aggPart{}, err
		}

		for i := range g.Rows {
			abs := g.Start + i

			if !isLive(s.live, abs) {
				continue
			}

			part.ex.RowsRead++

			st := &rowState{sc: sc, row: abs, rows: rows, i: i}

			matched, err := st.match()
			if err != nil {
				return aggPart{}, err
			}

			if !matched {
				continue
			}

			part.matched++

			if err := fillValues(p, st, at, groupPaths, vals); err != nil {
				return aggPart{}, err
			}

			keys, err := st.keys()
			if err != nil {
				return aggPart{}, err
			}

			if err := part.tbl.Add(vals, query.Pos{Keys: keys, ID: rows.IDs[i], Seq: rows.Seqs[i]}); err != nil {
				return aggPart{}, err
			}
		}
	}

	part.ex.RawFetched += sc.p.ex.RawFetched

	if part.ex.RowGroupsRead > 0 {
		part.ex.SegmentsRead = 1
	} else {
		part.ex.SegmentsSkipped = 1
	}

	return part, nil
}

// pathsByIndex lists the paths of a path-to-index map in index order.
func pathsByIndex(at map[string]int) []string {
	paths := make([]string, len(at))

	for p, i := range at {
		paths[i] = p
	}

	return paths
}

// fillValues reads the group values of one row: from its cells, or from the
// document for a path whose cell cannot answer.
func fillValues(p *agg.Pipeline, st *rowState, at []int, paths []string, vals []query.Value) error {
	for k, path := range paths {
		cell := st.rows.Cells[at[k]][st.i]

		if cell.Known {
			vals[k] = cellValue(cell, p.IsTime(path))

			continue
		}

		if err := st.decode(); err != nil {
			return err
		}

		vals[k] = agg.ValueOf(st.m, path, p.IsTime(path))
	}

	return nil
}

// cellValue is the value of a known cell, as agg.ValueOf gives it for the
// document: a scalar, or an array of the scalars of a list.
func cellValue(c segment.Cell, isTime bool) query.Value {
	if !c.List {
		return query.FromJSON(rawOf(c.Vals[0]), isTime)
	}

	raws := make([]any, len(c.Vals))

	for i, s := range c.Vals {
		raws[i] = rawOf(s)
	}

	return query.FromJSON(slices.Clone(raws), false)
}
