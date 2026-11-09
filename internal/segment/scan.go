package segment

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"

	"github.com/parquet-go/parquet-go"
)

// Column reads for a scan (plan §11.2). A scan reads only the columns of the
// paths it names, and a row group can be skipped with its zone map or bloom
// filter. A cell that the column cannot answer is not guessed: the caller
// reads _raw for that row.

// Group is one row group: rows Start to Start+Rows-1 of the segment.
type Group struct {
	Start int
	Rows  int
}

// ScalarKind is the type of a Scalar.
type ScalarKind uint8

const (
	ScalarBool ScalarKind = iota + 1
	ScalarNumber
	ScalarString
)

// Scalar is one value of a column. Numbers are float64: the query compares
// numbers as float64 too.
type Scalar struct {
	Kind ScalarKind
	Num  float64
	Str  string
	Bool bool
}

// Zone is the lowest and highest value of a column in one row group. Empty
// is set when the group holds no value of the column.
type Zone struct {
	Kind   ScalarKind
	Lo, Hi Scalar
	Empty  bool
}

// Cell is the value of one path in one row. Known is false when the column
// cannot answer the row. List is set for an array of scalars, whose elements
// are in Vals.
type Cell struct {
	Known bool
	List  bool
	Vals  []Scalar
}

// GroupRows holds the id and insertion position of every row of a row group,
// and the cells of the requested paths: Cells[path][row].
type GroupRows struct {
	IDs   []string
	Seqs  []uint64
	Vs    []uint64
	Cells [][]Cell
}

// Groups lists the row groups in row order.
func (r *parquetReader) Groups() []Group {
	out := make([]Group, len(r.starts))

	for g := range out {
		out[g] = Group{Start: r.starts[g], Rows: r.rowsIn(g)}
	}

	return out
}

// ColumnOf returns the column of a document path. A path that is not a
// column in this segment has values only in _raw.
func (r *parquetReader) ColumnOf(path string) (Column, bool) {
	c, ok := r.columns[path]

	return c, ok
}

// Zone returns the lowest and highest number or string of a column in one
// row group, from the page index. ok is false when the stats cannot be used:
// a mixed column, a bool or json column, or no page index.
func (r *parquetReader) Zone(group int, path string) (Zone, bool) {
	col, ok := r.columns[path]

	if !ok || col.Mixed || !zoneType(col.Type) {
		return Zone{}, false
	}

	k := chunkKey{group, path}

	if z, ok := r.zones.Load(k); ok {
		zc := z.(zoneCached)

		return zc.z, zc.ok
	}

	z, ok := r.readZone(group, path)
	r.zones.Store(k, zoneCached{z, ok})

	return z, ok
}

// readZone reads the column index of one chunk. It is read on first use
// (the file opens without its page index) and kept by Zone: the chunks that
// parquet-go gives are copies, so its own cache does not last.
func (r *parquetReader) readZone(group int, path string) (Zone, bool) {
	chunk, err := r.chunkOf(group, path)
	if err != nil {
		return Zone{}, false
	}

	idx, err := chunk.ColumnIndex()
	if err != nil || idx.NumPages() == 0 {
		return Zone{}, false
	}

	return zoneOf(idx, chunk.Type().Kind()), true
}

// chunkKey is one column chunk: a row group and a path.
type chunkKey struct {
	group int
	path  string
}

type zoneCached struct {
	z  Zone
	ok bool
}

func zoneType(t string) bool {
	switch strings.TrimPrefix(t, "list<") {
	case "int", "float", "string":
		return true
	}

	return false
}

// zoneOf folds the page bounds into one zone. A page with only nulls adds
// nothing.
func zoneOf(idx parquet.ColumnIndex, kind parquet.Kind) Zone {
	z := Zone{Kind: ScalarString}

	if kind == parquet.Int64 || kind == parquet.Double {
		z.Kind = ScalarNumber
	}

	seen := false

	for p := 0; p < idx.NumPages(); p++ {
		if idx.NullPage(p) {
			continue
		}

		lo, hi := scalarOf(idx.MinValue(p), kind), scalarOf(idx.MaxValue(p), kind)

		if !seen {
			z.Lo, z.Hi, seen = lo, hi, true

			continue
		}

		if less(lo, z.Lo) {
			z.Lo = lo
		}

		if less(z.Hi, hi) {
			z.Hi = hi
		}
	}

	z.Empty = !seen

	return z
}

func less(a, b Scalar) bool {
	if a.Kind == ScalarString {
		return a.Str < b.Str
	}

	return a.Num < b.Num
}

// Bloom tells whether the filter of a column in one row group may hold v.
// known is false when there is no usable filter (no index on the path, a
// mixed column, or a value the filter cannot hash), and then the group must
// be read.
func (r *parquetReader) Bloom(group int, path string, v Scalar) (may, known bool) {
	col, ok := r.columns[path]

	if !ok || col.Mixed {
		return true, false
	}

	probe, ok := probeOf(col.Type, v)

	if !ok {
		return true, false
	}

	bf := r.bloomOf(group, path)

	if bf == nil {
		return true, false
	}

	has, err := bf.Check(probe)
	if err != nil {
		return true, false
	}

	return has, true
}

// probeOf is the bloom probe of v for a column of type t. A number that is
// not an integer cannot equal an int column, so it has no probe but is known
// to be absent; that case returns ok with a false probe.
func probeOf(t string, v Scalar) (parquet.Value, bool) {
	base := strings.TrimPrefix(t, "list<")
	base = strings.TrimSuffix(base, ">")

	switch {
	case v.Kind == ScalarNumber && base == "int":
		if v.Num != math.Trunc(v.Num) || math.Abs(v.Num) > 1<<53 {
			return parquet.Value{}, false
		}

		return parquet.ValueOf(int64(v.Num)), true
	case v.Kind == ScalarNumber && base == "float":
		return parquet.ValueOf(v.Num), true
	case v.Kind == ScalarString && base == "string":
		return parquet.ValueOf(v.Str), true
	}

	return parquet.Value{}, false
}

// ReadGroup reads the ids, positions and cells of paths for one row group.
// Only the chunks of the paths and of _id, _seq, _v and _rest are read.
func (r *parquetReader) ReadGroup(group int, paths []string) (GroupRows, error) {
	return r.readRows(group, paths, true, nil)
}

// ReadCells is ReadGroup without the ids and the versions (IDs and Vs are
// nil): a read that only counts or groups rows, with no row order to keep,
// does not decode an id string for each row. keep, when set, tells the
// rows (in the group) whose cells the caller reads: the others stay
// unknown, so a filter that keeps one row in seven builds one cell in seven.
func (r *parquetReader) ReadCells(group int, paths []string, keep func(row int) bool) (GroupRows, error) {
	return r.readRows(group, paths, false, keep)
}

func (r *parquetReader) readRows(group int, paths []string, ids bool, keep func(int) bool) (GroupRows, error) {
	rg := r.file.RowGroups()[group]
	chunks := rg.ColumnChunks()

	n := int(rg.NumRows())

	out := GroupRows{
		Seqs:  make([]uint64, n),
		Cells: make([][]Cell, len(paths)),
	}

	reads := []error{eachValue(chunks[r.cols.seq], func(k int, v parquet.Value) { out.Seqs[k] = uint64(v.Int64()) })}

	if ids {
		out.IDs, out.Vs = make([]string, n), make([]uint64, n)

		reads = append(reads,
			eachValue(chunks[r.cols.id], func(k int, v parquet.Value) { out.IDs[k] = string(v.ByteArray()) }),
			eachValue(chunks[r.cols.v], func(k int, v parquet.Value) { out.Vs[k] = uint64(v.Int64()) }),
		)
	}

	if err := errors.Join(reads...); err != nil {
		return GroupRows{}, err
	}

	if len(paths) == 0 {
		return out, nil
	}

	for i, p := range paths {
		cells, err := r.cellsOf(rg, p, n, keep)
		if err != nil {
			return GroupRows{}, err
		}

		out.Cells[i] = cells
	}

	return out, r.fromRest(chunks[r.cols.rest], paths, out.Cells, n, keep)
}

// fromRest fills the kept cells that their column did not answer from
// _rest, where a conflicting kind of a path goes. _rest is read only when
// such a cell exists.
func (r *parquetReader) fromRest(chunk parquet.ColumnChunk, paths []string, cells [][]Cell, n int, keep func(int) bool) error {
	var rest [][]byte

	for i, p := range paths {
		for k := range cells[i] {
			if cells[i][k].Known || (keep != nil && !keep(k)) {
				continue
			}

			if rest == nil {
				var err error

				if rest, err = restRows(chunk, n); err != nil {
					return err
				}
			}

			if rest[k] != nil {
				cells[i][k] = restCell(rest[k], p)
			}
		}
	}

	return nil
}

// cellsOf reads the cells of one path. A row with no column value takes its
// value from _rest, where a conflicting kind of the path went.
func (r *parquetReader) cellsOf(rg parquet.RowGroup, path string, n int, keep func(int) bool) ([]Cell, error) {
	cells := make([]Cell, n)
	col, ok := r.columns[path]

	if ok && col.Type != "json" {
		leaf, found := r.file.Schema().Lookup(path)

		if !found {
			return cells, nil
		}

		if err := fillCells(cells, rg.ColumnChunks()[leaf.ColumnIndex], strings.HasPrefix(col.Type, "list<"), keep); err != nil {
			return nil, err
		}
	}

	return cells, nil
}

// fillCells groups the values of a column chunk by row: a value with
// repetition level 0 starts a row. A null (definition level 0) adds nothing,
// so the row stays unknown.
//
// A plain column has one value a row: the cells share one block of
// scalars, so a group costs one allocation, not one a row.
func fillCells(cells []Cell, chunk parquet.ColumnChunk, list bool, keep func(int) bool) error {
	row := -1

	var one []Scalar

	if !list {
		one = make([]Scalar, len(cells))
	}

	intern := map[string]string{}

	return eachValue(chunk, func(_ int, v parquet.Value) {
		if v.RepetitionLevel() == 0 {
			row++
		}

		if v.DefinitionLevel() == 0 || row < 0 || row >= len(cells) || (keep != nil && !keep(row)) {
			return
		}

		cells[row].Known = true
		cells[row].List = list

		s := internScalar(v, intern)

		if list || len(cells[row].Vals) > 0 {
			cells[row].Vals = append(cells[row].Vals, s)

			return
		}

		one[row] = s
		cells[row].Vals = one[row : row+1 : row+1]
	})
}

// internLimit is how many distinct strings one chunk shares: a column of
// few values (a status, a type) makes one string per value, not per row.
const internLimit = 4096

// internScalar is scalarOf, with the strings of the chunk shared.
func internScalar(v parquet.Value, intern map[string]string) Scalar {
	k := v.Kind()

	if k == parquet.Boolean || k == parquet.Int64 || k == parquet.Double {
		return scalarOf(v, k)
	}

	b := v.ByteArray()

	if s, ok := intern[string(b)]; ok {
		return Scalar{Kind: ScalarString, Str: s}
	}

	s := string(b)

	if len(intern) < internLimit {
		intern[s] = s
	}

	return Scalar{Kind: ScalarString, Str: s}
}

// restRows returns the _rest JSON of each row, nil when the row has none.
func restRows(chunk parquet.ColumnChunk, n int) ([][]byte, error) {
	out := make([][]byte, n)

	err := eachValue(chunk, func(k int, v parquet.Value) {
		if k < n && v.DefinitionLevel() > 0 {
			out[k] = bytes.Clone(v.ByteArray())
		}
	})

	return out, err
}

// restCell reads the value of path from one row's _rest object. Only scalars
// and arrays of scalars are cells; anything else is left unknown.
func restCell(rest []byte, path string) Cell {
	var m map[string]json.RawMessage

	if json.Unmarshal(rest, &m) != nil {
		return Cell{}
	}

	raw, ok := m[path]

	if !ok {
		return Cell{}
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()

	var v any

	if dec.Decode(&v) != nil {
		return Cell{}
	}

	if arr, ok := v.([]any); ok {
		return listCell(arr)
	}

	s, ok := scalarAny(v)

	if !ok {
		return Cell{}
	}

	return Cell{Known: true, Vals: []Scalar{s}}
}

func listCell(arr []any) Cell {
	if len(arr) == 0 {
		return Cell{}
	}

	vals := make([]Scalar, len(arr))

	for i, x := range arr {
		s, ok := scalarAny(x)

		if !ok {
			return Cell{}
		}

		vals[i] = s
	}

	return Cell{Known: true, List: true, Vals: vals}
}

func scalarAny(v any) (Scalar, bool) {
	switch x := v.(type) {
	case bool:
		return Scalar{Kind: ScalarBool, Bool: x}, true
	case string:
		return Scalar{Kind: ScalarString, Str: x}, true
	case json.Number:
		f, err := x.Float64()

		return Scalar{Kind: ScalarNumber, Num: f}, err == nil
	}

	return Scalar{}, false
}

// scalarOf converts a parquet value of physical kind k.
func scalarOf(v parquet.Value, k parquet.Kind) Scalar {
	switch k {
	case parquet.Boolean:
		return Scalar{Kind: ScalarBool, Bool: v.Boolean()}
	case parquet.Int64:
		return Scalar{Kind: ScalarNumber, Num: float64(v.Int64())}
	case parquet.Double:
		return Scalar{Kind: ScalarNumber, Num: v.Double()}
	}

	return Scalar{Kind: ScalarString, Str: string(v.ByteArray())}
}

// bloomOf is the bloom filter of one chunk, read on first use and kept
// (nil when the chunk has none).
func (r *parquetReader) bloomOf(group int, path string) parquet.BloomFilter {
	k := chunkKey{group, path}

	if bf, ok := r.blooms.Load(k); ok {
		f, _ := bf.(parquet.BloomFilter)

		return f
	}

	var bf parquet.BloomFilter

	if chunk, err := r.chunkOf(group, path); err == nil {
		bf = chunk.BloomFilter()
	}

	r.blooms.Store(k, bf)

	return bf
}

// chunkOf is the column chunk of a document path in one row group.
func (r *parquetReader) chunkOf(group int, path string) (parquet.ColumnChunk, error) {
	leaf, ok := r.file.Schema().Lookup(path)

	if !ok {
		return nil, errors.New("segment: no column " + path)
	}

	return r.file.RowGroups()[group].ColumnChunks()[leaf.ColumnIndex], nil
}

// readColumns maps the document columns of a file by name. A file without
// the metadata key, from before zone maps, has no columns: every path is then
// read from _raw.
func readColumns(f *parquet.File) map[string]Column {
	raw, ok := f.Lookup(columnsKey)

	if !ok {
		return nil
	}

	var cols []Column

	if json.Unmarshal([]byte(raw), &cols) != nil {
		return nil
	}

	out := make(map[string]Column, len(cols))

	for _, c := range cols {
		if !reserved[c.Name] {
			out[c.Name] = c
		}
	}

	return out
}
