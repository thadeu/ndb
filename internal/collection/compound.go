package collection

import (
	"slices"

	"github.com/thadeu/ndb/internal/index"
	"github.com/thadeu/ndb/internal/query"
)

// The use of a compound index (index.DocKeys) by a query. An index on
// (a, b, c) serves the conditions that bind its fields from the first one:
//
//   - equality or $in on the first fields (a = x; a = x and b = y);
//   - then, at most, range conditions on the next field (b >= t and b < u).
//
// The documents it gives are candidates: the query still tests each one,
// so a condition the index does not bind costs nothing in correctness.

// maxPrefixes bounds the $in values of the bound fields, multiplied: each
// combination is one read of the index.
const maxPrefixes = 64

// binding is how much of a compound index a query binds.
type binding struct {
	s        *secondary
	prefixes [][]byte
	points   int

	// lower and upper are the range conditions on the field after the
	// points, or nil.
	lower, upper *query.Cond
}

// score orders the bindings: more bound fields first, a range after them
// breaks the tie.
func (b binding) score() int {
	n := 2 * b.points

	if b.lower != nil || b.upper != nil {
		n++
	}

	return n
}

// byCompound is the source that the best compound index gives for conds.
// It is used when it binds two fields, or when it binds its first field
// and that field has no index of its own. The caller holds c.mu.
func (c *Collection) byCompound(conds []query.Cond) (source, bool) {
	var best binding

	for _, s := range c.rd.idx {
		if !s.ready || !index.Compound(s.field) {
			continue
		}

		b := bind(s, conds)

		if b.score() > best.score() || b.score() == best.score() && best.s != nil && s.field < best.s.field {
			best = b
		}
	}

	if best.points == 0 {
		return source{}, false
	}

	if first := c.rd.idx[index.Fields(best.s.field)[0]]; best.score() == 2 && first != nil && first.ready {
		return source{}, false
	}

	src := c.byRanges(best.s, best.ranges(best.lower))

	// A range of two sides is two reads, and a document must be in both: an
	// array matches when one element passes each condition, and they need
	// not be the same element, so one read between the two bounds would
	// miss it.
	if best.lower != nil && best.upper != nil {
		src = bothSources(src, c.byRanges(best.s, best.ranges(best.upper)))
	} else if best.upper != nil {
		src = c.byRanges(best.s, best.ranges(best.upper))
	}

	src.plan = "index:" + best.s.field

	return src, true
}

// bothSources keeps the candidates that are in a and in b. Both come from
// byRanges under the same c.mu, so their segments are the same, in order.
func bothSources(a, b source) source {
	in := make(map[string]struct{}, len(b.cands))

	for _, cd := range b.cands {
		in[cd.id] = struct{}{}
	}

	a.cands = slices.DeleteFunc(a.cands, func(cd cand) bool {
		_, ok := in[cd.id]

		return !ok
	})

	for i := range a.segs {
		a.segs[i].live.and(b.segs[i].live)
	}

	return a
}

// bind matches the fields of s, in order, with the conditions.
func bind(s *secondary, conds []query.Cond) binding {
	b := binding{s: s, prefixes: [][]byte{nil}}

	for _, f := range index.Fields(s.field) {
		point := pointOn(conds, f)

		if point == nil || len(b.prefixes)*len(point.Vals) > maxPrefixes {
			b.lower, b.upper = rangeOn(conds, f)

			break
		}

		next := make([][]byte, 0, len(b.prefixes)*len(point.Vals))

		for _, p := range b.prefixes {
			for _, v := range point.Vals {
				next = append(next, append(slices.Clone(p), index.Encode(v)...))
			}
		}

		b.prefixes = next
		b.points++
	}

	if b.points == 0 {
		b.lower, b.upper = nil, nil
	}

	return b
}

// ranges are the reads of the index: one per prefix, cut by one range
// condition (nil for none), and the overflow documents.
func (b binding) ranges(cd *query.Cond) []index.Range {
	out := make([]index.Range, 0, len(b.prefixes)+1)

	for _, p := range b.prefixes {
		r := index.PrefixRange(p)

		if cd != nil {
			r = index.Intersect(r, index.TupleCmp(p, cd.Op, cd.Vals[0]))
		}

		out = append(out, r)
	}

	return append(out, index.Overflow())
}

// pointOn is the first equality or $in condition on field.
func pointOn(conds []query.Cond, field string) *query.Cond {
	for i, cd := range conds {
		if cd.Path == field && isPoint(cd.Op) && len(cd.Vals) > 0 {
			return &conds[i]
		}
	}

	return nil
}

// rangeOn is the first lower bound (gt, gte) and the first upper bound (lt,
// lte) on field.
func rangeOn(conds []query.Cond, field string) (lower, upper *query.Cond) {
	for i, cd := range conds {
		if cd.Path != field || !isRange(cd.Op) || len(cd.Vals) == 0 {
			continue
		}

		switch isLower := cd.Op == "gt" || cd.Op == "gte"; {
		case isLower && lower == nil:
			lower = &conds[i]
		case !isLower && upper == nil:
			upper = &conds[i]
		}
	}

	return lower, upper
}
