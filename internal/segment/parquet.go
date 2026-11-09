package segment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

const rowGroupRows = 1024

// columnsKey is the file metadata key that holds the columns of the segment
// as JSON (Column). Readers look columns up by name through it.
const columnsKey = "ndb.columns"

// metaColumns are written for every segment, before the document columns.
var metaColumns = []Column{
	{Name: "_id", Type: "string"},
	{Name: "_v", Type: "int"},
	{Name: "_seq", Type: "int"},
	{Name: "_tx", Type: "int"},
	{Name: "_del", Type: "bool"},
	{Name: "_raw", Type: "string"},
	{Name: "_rest", Type: "string"},
}

type parquetWriter struct{}

// Rows is a source of rows that can be read more than once: encoding reads
// it twice (first the columns, then the values), so a segment is never held
// in memory, only one row group of it.
type Rows func(fn func(Row) error) error

// RowsOf is the source of rows already in memory.
func RowsOf(rows []Row) Rows {
	return func(fn func(Row) error) error {
		for _, r := range rows {
			if err := fn(r); err != nil {
				return err
			}
		}

		return nil
	}
}

func (parquetWriter) Encode(rows []Row, indexed []string) ([]byte, Info, error) {
	var buf bytes.Buffer

	info, err := encodeTo(&buf, RowsOf(rows), indexed)
	if err != nil {
		return nil, Info{}, err
	}

	return buf.Bytes(), info, nil
}

// encodeTo writes the segment of rows to w, one row group at a time.
func encodeTo(w io.Writer, rows Rows, indexed []string) (Info, error) {
	columns, n, err := infer(rows)
	if err != nil {
		return Info{}, err
	}

	schema := buildSchema(columns)
	enc := newEncoder(schema, columns)
	info := Info{Rows: n, Columns: describe(columns)}

	meta, err := json.Marshal(info.Columns)
	if err != nil {
		return Info{}, err
	}

	pw := parquet.NewWriter(w, schema,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
		parquet.BloomFilters(bloomFilters(schema, indexed)...),
		parquet.KeyValueMetadata(columnsKey, string(meta)),
	)

	batch := make([]parquet.Row, 0, rowGroupRows)

	flush := func() error {
		_, err := pw.WriteRows(batch)
		batch = batch[:0]

		return err
	}

	err = rows(func(r Row) error {
		cells, err := cellsOf(r)
		if err != nil {
			return err
		}

		batch = append(batch, enc.row(r, cells))

		if len(batch) < rowGroupRows {
			return nil
		}

		return flush()
	})
	if err != nil {
		return Info{}, err
	}

	if err := flush(); err != nil {
		return Info{}, err
	}

	return info, pw.Close()
}

func describe(columns []column) []Column {
	out := append([]Column{}, metaColumns...)

	for _, c := range columns {
		out = append(out, Column{Name: c.name, Type: kindNames[c.kind], Mixed: c.mixed})
	}

	return out
}

func buildSchema(columns []column) *parquet.Schema {
	group := parquet.Group{
		"_id":   parquet.String(),
		"_v":    parquet.Int(64),
		"_seq":  parquet.Int(64),
		"_tx":   parquet.Int(64),
		"_del":  parquet.Leaf(parquet.BooleanType),
		"_raw":  parquet.String(),
		"_rest": parquet.Optional(parquet.String()),
	}

	for _, c := range columns {
		group[c.name] = nodeOf(c.kind)
	}

	return parquet.NewSchema("segment", group)
}

func nodeOf(k kind) parquet.Node {
	switch k {
	case kBool:
		return parquet.Optional(parquet.Leaf(parquet.BooleanType))
	case kInt:
		return parquet.Optional(parquet.Int(64))
	case kFloat:
		return parquet.Optional(parquet.Leaf(parquet.DoubleType))
	case kString, kJSON:
		return parquet.Optional(dictString())
	case kListBool:
		return parquet.Repeated(parquet.Leaf(parquet.BooleanType))
	case kListInt:
		return parquet.Repeated(parquet.Int(64))
	case kListFloat:
		return parquet.Repeated(parquet.Leaf(parquet.DoubleType))
	}

	return parquet.Repeated(dictString())
}

func dictString() parquet.Node {
	return parquet.Encoded(parquet.String(), &parquet.RLEDictionary)
}

func bloomFilters(schema *parquet.Schema, indexed []string) []parquet.BloomFilterColumn {
	var out []parquet.BloomFilterColumn

	for _, name := range indexed {
		if _, ok := schema.Lookup(name); ok {
			out = append(out, parquet.SplitBlockFilter(10, name))
		}
	}

	return out
}

// encoder builds parquet rows. Every leaf gets one value per row, and a
// null is a value with definition level 0.
type encoder struct {
	columns []column
	leaves  map[string]int
	n       int
}

func newEncoder(schema *parquet.Schema, columns []column) encoder {
	leaves := map[string]int{}

	for _, m := range metaColumns {
		leaf, _ := schema.Lookup(m.Name)
		leaves[m.Name] = leaf.ColumnIndex
	}

	for _, c := range columns {
		leaf, _ := schema.Lookup(c.name)
		leaves[c.name] = leaf.ColumnIndex
	}

	return encoder{columns: columns, leaves: leaves, n: len(leaves)}
}

func (e encoder) row(r Row, cells map[string]cell) parquet.Row {
	vals := make([][]parquet.Value, e.n)

	vals[e.leaves["_id"]] = []parquet.Value{parquet.ValueOf(r.ID).Level(0, 0, e.leaves["_id"])}
	vals[e.leaves["_v"]] = []parquet.Value{parquet.ValueOf(int64(r.V)).Level(0, 0, e.leaves["_v"])}
	vals[e.leaves["_seq"]] = []parquet.Value{parquet.ValueOf(int64(r.Seq)).Level(0, 0, e.leaves["_seq"])}
	vals[e.leaves["_tx"]] = []parquet.Value{parquet.ValueOf(int64(r.Tx)).Level(0, 0, e.leaves["_tx"])}
	vals[e.leaves["_del"]] = []parquet.Value{parquet.ValueOf(r.Del).Level(0, 0, e.leaves["_del"])}
	vals[e.leaves["_raw"]] = []parquet.Value{parquet.ValueOf(string(r.Raw)).Level(0, 0, e.leaves["_raw"])}

	rest := map[string]json.RawMessage{}

	for _, c := range e.columns {
		idx := e.leaves[c.name]
		cl, ok := cells[c.name]

		switch {
		case !ok:
			vals[idx] = []parquet.Value{parquet.Value{}.Level(0, 0, idx)}
		case cl.k != c.kind:
			vals[idx] = []parquet.Value{parquet.Value{}.Level(0, 0, idx)}
			rest[c.name] = restJSON(cl)
		default:
			vals[idx] = valuesOf(cl, idx)
		}
	}

	vals[e.leaves["_rest"]] = []parquet.Value{restValue(rest, e.leaves["_rest"])}

	var out parquet.Row

	for _, v := range vals {
		out = append(out, v...)
	}

	return out
}

func valuesOf(cl cell, idx int) []parquet.Value {
	if list, ok := cl.v.([]any); ok {
		out := make([]parquet.Value, len(list))

		for i, x := range list {
			rep := 0

			if i > 0 {
				rep = 1
			}

			out[i] = parquet.ValueOf(x).Level(rep, 1, idx)
		}

		return out
	}

	return []parquet.Value{parquet.ValueOf(plain(cl.v)).Level(0, 1, idx)}
}

func plain(v any) any {
	if raw, ok := v.(json.RawMessage); ok {
		return string(raw)
	}

	return v
}

func restJSON(cl cell) json.RawMessage {
	if raw, ok := cl.v.(json.RawMessage); ok {
		return raw
	}

	b, _ := json.Marshal(cl.v)

	return b
}

func restValue(rest map[string]json.RawMessage, idx int) parquet.Value {
	if len(rest) == 0 {
		return parquet.Value{}.Level(0, 0, idx)
	}

	b, _ := json.Marshal(rest)

	return parquet.ValueOf(string(b)).Level(0, 1, idx)
}

type parquetReader struct {
	file    *parquet.File
	cols    readCols
	starts  []int
	n       int
	closer  io.Closer
	columns map[string]Column

	// zones and blooms keep the column index and the bloom filter of each
	// chunk read so far (chunkKey -> zoneCached, *memBloom).
	zones  sync.Map
	blooms sync.Map
}

type readCols struct {
	id, v, seq, tx, del, raw, rest int
}

// decode opens a segment file. The page index (zone maps) and the bloom
// filters are read on first use of each column chunk, not at open: a query
// reads those of the columns it filters on, and opening a segment (at
// start, or when a sleeping collection wakes) reads only the footer.
func decode(ra io.ReaderAt, size int64) (*parquetReader, error) {
	f, err := parquet.OpenFile(ra, size, parquet.SkipPageIndex(true), parquet.SkipBloomFilters(true))
	if err != nil {
		return nil, fmt.Errorf("segment: %w", err)
	}

	cols, err := leafIndexes(f.Schema())
	if err != nil {
		return nil, err
	}

	r := &parquetReader{file: f, cols: cols, columns: readColumns(f)}

	for _, rg := range f.RowGroups() {
		r.starts = append(r.starts, r.n)
		r.n += int(rg.NumRows())
	}

	return r, nil
}

func leafIndexes(s *parquet.Schema) (readCols, error) {
	var out readCols

	targets := map[string]*int{
		"_id": &out.id, "_v": &out.v, "_seq": &out.seq, "_tx": &out.tx, "_del": &out.del, "_raw": &out.raw, "_rest": &out.rest,
	}

	for name, dst := range targets {
		leaf, ok := s.Lookup(name)

		if !ok {
			return out, fmt.Errorf("segment: column %s missing", name)
		}

		*dst = leaf.ColumnIndex
	}

	return out, nil
}

func (r *parquetReader) Len() int {
	return r.n
}

// Entries reads the metadata columns one column chunk at a time, so the
// documents are not decoded.
func (r *parquetReader) Entries(fn func(row int, rec Row) error) error {
	i := 0

	for g, rg := range r.file.RowGroups() {
		cols, err := r.metaOf(rg)
		if err != nil {
			return err
		}

		for k := range r.rowsIn(g) {
			rec := Row{
				ID:  string(cols.id[k].ByteArray()),
				V:   uint64(cols.v[k].Int64()),
				Seq: uint64(cols.seq[k].Int64()),
				Tx:  uint64(cols.tx[k].Int64()),
				Del: cols.del[k].Boolean(),
			}

			if err := fn(i, rec); err != nil {
				return err
			}

			i++
		}
	}

	return nil
}

// metaValues holds the values of the metadata columns of one row group.
type metaValues struct {
	id, v, seq, tx, del []parquet.Value
}

func (r *parquetReader) metaOf(rg parquet.RowGroup) (metaValues, error) {
	var (
		out metaValues
		err error
	)

	chunks := rg.ColumnChunks()

	if out.id, err = columnValues(chunks[r.cols.id]); err != nil {
		return out, err
	}

	if out.v, err = columnValues(chunks[r.cols.v]); err != nil {
		return out, err
	}

	if out.seq, err = columnValues(chunks[r.cols.seq]); err != nil {
		return out, err
	}

	if out.tx, err = columnValues(chunks[r.cols.tx]); err != nil {
		return out, err
	}

	if out.del, err = columnValues(chunks[r.cols.del]); err != nil {
		return out, err
	}

	return out, nil
}

// eachValue calls fn with every value of a column chunk, in order, with its
// index. A value is only valid during the call (byte arrays point into the
// page): fn copies what it keeps. A read of a group copies each value once,
// into what the caller keeps, instead of cloning all of them first.
func eachValue(chunk parquet.ColumnChunk, fn func(k int, v parquet.Value)) error {
	pages := chunk.Pages()

	defer pages.Close()

	bp := valueBufs.Get().(*[]parquet.Value)
	defer valueBufs.Put(bp)

	k := 0

	for {
		page, err := pages.ReadPage()

		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		buf := (*bp)[:min(page.NumValues(), int64(len(*bp)))]
		vr := page.Values()

		for {
			n, err := vr.ReadValues(buf)

			for _, v := range buf[:n] {
				fn(k, v)
				k++
			}

			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				return err
			}
		}
	}
}

// valueBufs are the read buffers of eachValue: one per read in flight, not
// one per page.
var valueBufs = sync.Pool{New: func() any {
	b := make([]parquet.Value, 1024)

	return &b
}}

// columnValues reads every value of a column chunk. The metadata columns are
// required, so each row gives one value.
func columnValues(chunk parquet.ColumnChunk) ([]parquet.Value, error) {
	var out []parquet.Value

	pages := chunk.Pages()

	defer pages.Close()

	for {
		page, err := pages.ReadPage()

		if errors.Is(err, io.EOF) {
			return out, nil
		}

		if err != nil {
			return nil, err
		}

		buf := make([]parquet.Value, page.NumValues())
		vr := page.Values()

		for {
			k, err := vr.ReadValues(buf)

			for _, v := range buf[:k] {
				out = append(out, v.Clone())
			}

			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				return nil, err
			}
		}
	}
}

// Raw returns the _raw value of one row. The row group that holds it is kept
// decoded in the cache of the daemon (groupcache.go), so reads of rows near
// each other cost one decode per group, even from many queries at once.
func (r *parquetReader) Raw(row int) ([]byte, error) {
	if row < 0 || row >= r.n {
		return nil, fmt.Errorf("segment: row %d out of range", row)
	}

	g := sort.Search(len(r.starts), func(j int) bool { return r.starts[j] > row }) - 1
	k := groupKey{r, g}

	raws, ok := groups.get(k)
	if !ok {
		var err error

		if raws, err = r.readGroup(g); err != nil {
			return nil, err
		}

		groups.put(k, raws)
	}

	return raws[row-r.starts[g]], nil
}

// readGroup decodes the _raw column of one row group. Only that column is
// read, so a row costs its group's raw bytes, not the other columns.
func (r *parquetReader) readGroup(g int) ([][]byte, error) {
	out := make([][]byte, 0, r.rowsIn(g))

	err := eachValue(r.file.RowGroups()[g].ColumnChunks()[r.cols.raw], func(_ int, v parquet.Value) {
		out = append(out, bytes.Clone(v.ByteArray()))
	})

	return out, err
}

func (r *parquetReader) rowsIn(g int) int {
	if g+1 < len(r.starts) {
		return r.starts[g+1] - r.starts[g]
	}

	return r.n - r.starts[g]
}

func (r *parquetReader) Close() error {
	groups.drop(r)

	if r.closer == nil {
		return nil
	}

	return r.closer.Close()
}
