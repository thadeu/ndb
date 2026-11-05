package agg

import (
	"encoding/binary"
	"math"
	"slices"
	"sync/atomic"

	"github.com/thadeu/ndb/internal/query"
)

// Budget is the memory a query may use for group tables, shared by all the
// partial tables of the query. A limit of 0 or less is no limit.
type Budget struct {
	limit int64
	used  atomic.Int64
}

func NewBudget(limit int64) *Budget {
	return &Budget{limit: limit}
}

func (b *Budget) add(n int64) error {
	if b.used.Add(n) > b.limit && b.limit > 0 && n > 0 {
		return query.NewError("group_limit_exceeded", "the group table is over the limit of %d bytes: use a $match, or fewer or coarser group keys", b.limit)
	}

	return nil
}

// accState is the state of one accumulator in one group. For $avg, n counts
// the numbers; for $count, the documents.
type accState struct {
	n   int64
	sum float64
	has bool
	val query.Value
	pos query.Pos
}

type bucket struct {
	keys []query.Value
	accs []accState
	cost int64
}

// Table is a group table: the partial result of one segment or of the
// memtable. Tables of one query merge into one. A table is for one
// goroutine.
type Table struct {
	p      *Pipeline
	groups map[string]*bucket
	budget *Budget
	key    []byte
	keyVal []query.Value
}

// NewTable makes an empty table that charges its groups to budget.
func (p *Pipeline) NewTable(budget *Budget) *Table {
	return &Table{
		p:      p,
		groups: map[string]*bucket{},
		budget: budget,
		keyVal: make([]query.Value, len(p.group.keys)),
	}
}

// Add counts one document in its group. vals are the values of Paths, and
// pos is the position of the document, which $first and $last use. Add keeps
// no reference to vals.
func (t *Table) Add(vals []query.Value, pos query.Pos) error {
	g := t.p.group
	t.key = t.key[:0]

	for i, k := range g.keys {
		v := k.expr.eval(vals)

		if v.Kind == query.Missing {
			v = query.Value{Kind: query.Null}
		}

		t.keyVal[i] = v
		t.key = appendKey(t.key, v)
	}

	b := t.groups[string(t.key)]

	if b == nil {
		b = &bucket{keys: slices.Clone(t.keyVal), accs: make([]accState, len(g.accs))}
		b.cost = int64(160 + len(t.key)*2 + 96*len(g.accs))

		if err := t.budget.add(b.cost); err != nil {
			return err
		}

		t.groups[string(t.key)] = b
	}

	for i, def := range g.accs {
		var v query.Value

		if def.arg != nil {
			v = def.arg.eval(vals)
		}

		b.accs[i].add(def.op, v, pos, t.p.Query)
	}

	return nil
}

// appendKey adds an injective encoding of v: equal values (by the cross-type
// order) have equal bytes, different values do not.
func appendKey(buf []byte, v query.Value) []byte {
	buf = append(buf, byte(v.Kind))

	switch v.Kind {
	case query.Number:
		buf = binary.LittleEndian.AppendUint64(buf, math.Float64bits(v.Num))
	case query.Time:
		buf = binary.LittleEndian.AppendUint64(buf, uint64(v.Int))
	case query.String, query.Object, query.Array:
		buf = binary.AppendUvarint(buf, uint64(len(v.Str)))
		buf = append(buf, v.Str...)
	case query.Bool:
		buf = append(buf, boolByte(v.Bool))
	}

	return buf
}

func boolByte(b bool) byte {
	if b {
		return 1
	}

	return 0
}

func (a *accState) add(op accOp, v query.Value, pos query.Pos, order *query.Query) {
	switch op {
	case opCount:
		a.n++
	case opSum:
		if v.Kind == query.Number {
			a.sum += v.Num
		}
	case opAvg:
		if v.Kind == query.Number {
			a.sum += v.Num
			a.n++
		}
	case opMin, opMax:
		a.extreme(op, v)
	case opFirst, opLast:
		a.edge(op, v, pos, order)
	}
}

// extreme keeps the lowest or highest value. Missing and null are ignored.
func (a *accState) extreme(op accOp, v query.Value) {
	if v.Kind <= query.Null {
		return
	}

	c := query.Compare(v, a.val)

	if !a.has || (op == opMin && c < 0) || (op == opMax && c > 0) {
		a.val, a.has = v, true
	}
}

// edge keeps the value of the first or last document in the order.
func (a *accState) edge(op accOp, v query.Value, pos query.Pos, order *query.Query) {
	if a.has {
		c := order.Cmp(pos, a.pos)

		if (op == opFirst && c >= 0) || (op == opLast && c <= 0) {
			return
		}
	}

	a.val, a.pos, a.has = v, pos, true
}

func (a *accState) merge(op accOp, o accState, order *query.Query) {
	switch op {
	case opCount:
		a.n += o.n
	case opSum:
		a.sum += o.sum
	case opAvg:
		a.sum += o.sum
		a.n += o.n
	case opMin, opMax:
		if o.has {
			a.extreme(op, o.val)
		}
	case opFirst, opLast:
		if o.has {
			a.edge(op, o.val, o.pos, order)
		}
	}
}

func (a *accState) result(op accOp) any {
	switch op {
	case opCount:
		return float64(a.n)
	case opSum:
		return a.sum
	case opAvg:
		if a.n == 0 {
			return nil
		}

		return a.sum / float64(a.n)
	}

	if !a.has {
		return nil
	}

	return outRaw(a.val)
}

// Merge adds the groups of o into t. o is not used after.
func (t *Table) Merge(o *Table) error {
	g := t.p.group

	for k, ob := range o.groups {
		b := t.groups[k]

		if b == nil {
			t.groups[k] = ob

			continue
		}

		for i, def := range g.accs {
			b.accs[i].merge(def.op, ob.accs[i], t.p.Query)
		}

		_ = t.budget.add(-ob.cost)
	}

	o.groups = nil

	return nil
}

// Len is the number of groups.
func (t *Table) Len() int {
	return len(t.groups)
}

// Rows are the output rows of the group stage, sorted by _id.
func (t *Table) Rows() []map[string]any {
	g := t.p.group
	list := make([]*bucket, 0, len(t.groups))

	for _, b := range t.groups {
		list = append(list, b)
	}

	slices.SortFunc(list, func(a, b *bucket) int { return cmpKeys(a.keys, b.keys) })

	out := make([]map[string]any, len(list))

	for i, b := range list {
		row := make(map[string]any, len(g.accs)+1)

		row["_id"] = g.idOf(b.keys)

		for j, def := range g.accs {
			row[def.name] = b.accs[j].result(def.op)
		}

		out[i] = row
	}

	return out
}

func (g *group) idOf(keys []query.Value) any {
	if g.single {
		return outRaw(keys[0])
	}

	id := make(map[string]any, len(keys))

	for i, k := range g.keys {
		id[k.name] = outRaw(keys[i])
	}

	return id
}

func cmpKeys(a, b []query.Value) int {
	for i := range a {
		if c := query.Compare(a[i], b[i]); c != 0 {
			return c
		}
	}

	return 0
}
