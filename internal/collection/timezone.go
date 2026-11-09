package collection

import (
	"math"
	"slices"

	"github.com/thadeu/ndb/internal/query"
	"github.com/thadeu/ndb/internal/segment"
)

// Time zone maps. The zone maps of a segment hold the min and max of each
// column as written (a time field holds strings), so they cannot answer a
// condition on time instants. A time zone is the earliest and latest
// instant of a time field in one row group, read from its cells the way the
// query reads them, once per segment and kept with it (16 bytes a group).
// A group with a value that is not a time, or a row without one, is never
// pruned by time.

type timeZone struct {
	lo, hi int64
	ok     bool
}

// timeZones are the time zones of path in r, one per row group, or nil when
// a group cannot be read.
func (r *segRef) timeZones(path string) []timeZone {
	r.timeMu.Lock()
	defer r.timeMu.Unlock()

	if z, ok := r.times[path]; ok {
		return z
	}

	zones := make([]timeZone, len(r.Groups()))

	for gi := range zones {
		rows, err := r.ReadGroup(gi, []string{path})
		if err != nil {
			zones = nil

			break
		}

		zones[gi] = groupTimeZone(rows.Seqs, rows.Cells[0])
	}

	if r.times == nil {
		r.times = map[string][]timeZone{}
	}

	r.times[path] = zones

	return zones
}

func groupTimeZone(seqs []uint64, cells []segment.Cell) timeZone {
	z := timeZone{lo: math.MaxInt64, hi: math.MinInt64, ok: true}

	for i, cell := range cells {
		if seqs[i] == 0 {
			continue
		}

		if !cell.Known || len(cell.Vals) == 0 {
			return timeZone{}
		}

		elems, _ := cellValues(cell, true)

		for _, v := range elems {
			if v.Kind != query.Time {
				return timeZone{}
			}

			z.lo, z.hi = min(z.lo, v.Int), max(z.hi, v.Int)
		}
	}

	return z
}

// timePruned tells whether a time condition of q rules out row group g of r.
func timePruned(q *query.Query, r any, g int, conds []query.Cond) bool {
	ref, ok := r.(*segRef)
	if !ok {
		return false
	}

	for _, cd := range conds {
		if !q.IsTime(cd.Path) || !allTimes(cd.Vals) {
			continue
		}

		zones := ref.timeZones(cd.Path)

		if g < len(zones) && timeExcludes(zones[g], cd) {
			return true
		}
	}

	return false
}

func allTimes(vals []query.Value) bool {
	for _, v := range vals {
		if v.Kind != query.Time {
			return false
		}
	}

	return len(vals) > 0
}

// timeExcludes is true when no instant of the zone satisfies cd.
func timeExcludes(z timeZone, cd query.Cond) bool {
	if !z.ok {
		return false
	}

	if z.lo > z.hi {
		return true
	}

	v := cd.Vals[0].Int

	switch cd.Op {
	case "eq", "in":
		for _, val := range cd.Vals {
			if val.Int >= z.lo && val.Int <= z.hi {
				return false
			}
		}

		return true
	case "gt":
		return z.hi <= v
	case "gte":
		return z.hi < v
	case "lt":
		return z.lo >= v
	case "lte":
		return z.lo > v
	}

	return false
}

// timeCovered tells whether every instant of row group g of r satisfies the
// time conditions of q: the whole group matches a query whose only
// condition is on time (TimeOnly).
func timeCovered(q *query.Query, r any, g int, conds []query.Cond) bool {
	ref, ok := r.(*segRef)
	if !ok {
		return false
	}

	covered := false

	for _, cd := range conds {
		if !q.IsTime(cd.Path) || !allTimes(cd.Vals) {
			continue
		}

		zones := ref.timeZones(cd.Path)

		if g >= len(zones) || !timeIncludes(zones[g], cd) {
			return false
		}

		covered = true
	}

	return covered
}

// timeIncludes is true when every instant of the zone satisfies cd.
func timeIncludes(z timeZone, cd query.Cond) bool {
	if !z.ok || z.lo > z.hi {
		return false
	}

	v := cd.Vals[0].Int

	switch cd.Op {
	case "gt":
		return z.lo > v
	case "gte":
		return z.lo >= v
	case "lt":
		return z.hi < v
	case "lte":
		return z.hi <= v
	case "eq", "in":
		return z.lo == z.hi && slices.ContainsFunc(cd.Vals, func(val query.Value) bool { return val.Int == z.lo })
	}

	return false
}
