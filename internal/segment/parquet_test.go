package segment_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/format"
	"github.com/thadeu/ndb/internal/segment"
)

func TestRoundTripEntriesAndRaw(t *testing.T) {
	rows := []segment.Row{
		{ID: "a", V: 1, Seq: 10, Tx: 5, Raw: []byte(`{"id":"a","status":"x"}`)},
		{ID: "b", V: 7, Del: true},
		{ID: "c", V: 2, Seq: 11, Tx: 6, Raw: []byte(`{"id":"c"}`)},
	}
	data, info := encode(t, rows, "status")

	if info.Rows != 3 {
		t.Fatalf("Info.Rows = %d", info.Rows)
	}

	reader, err := segment.Decode(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	var got []segment.Row

	if err := reader.Entries(func(_ int, r segment.Row) error {
		got = append(got, r)

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	for i, want := range rows {
		g := got[i]

		if g.ID != want.ID || g.V != want.V || g.Seq != want.Seq || g.Tx != want.Tx || g.Del != want.Del {
			t.Fatalf("row %d = %+v, want %+v", i, g, want)
		}

		if g.Raw != nil {
			t.Fatalf("Entries must not load raw, row %d", i)
		}

		raw, err := reader.Raw(i)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(raw, want.Raw) {
			t.Fatalf("raw %d = %q, want %q", i, raw, want.Raw)
		}
	}
}

// rowGroupSize is the number of rows per row group in segments (parquet.go).
const rowGroupSize = 1024

func TestZoneMapsAndNullCountPerRowGroup(t *testing.T) {
	var rows []segment.Row

	for i := range 10000 {
		doc := fmt.Sprintf(`{"id":"r%d","n":%d}`, i, i)

		if i%2 == 0 {
			doc = fmt.Sprintf(`{"id":"r%d","n":%d,"score":1.5}`, i, i)
		}

		rows = append(rows, row(fmt.Sprintf("r%d", i), uint64(i+1), doc))
	}

	data, _ := encode(t, rows)

	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	if n := len(f.RowGroups()); n < 2 {
		t.Fatalf("row groups = %d, want several", n)
	}

	vcol, _ := f.Schema().Lookup("_v")
	chunk := f.RowGroups()[0].ColumnChunks()[vcol.ColumnIndex].(*parquet.FileColumnChunk)

	minV, maxV, ok := chunk.Bounds()

	if !ok || minV.Int64() != 1 || maxV.Int64() != 1024 {
		t.Fatalf("zone map of _v in row group 0 = [%v, %v]", minV, maxV)
	}

	scol, _ := f.Schema().Lookup("score")
	last := f.RowGroups()[len(f.RowGroups())-1].ColumnChunks()[scol.ColumnIndex].(*parquet.FileColumnChunk)

	if last.NullCount() == 0 {
		t.Fatal("null count of a sparse column must be counted")
	}
}

func TestBloomFilterOnIndexedColumn(t *testing.T) {
	var rows []segment.Row

	for i := range 500 {
		rows = append(rows, row(fmt.Sprintf("r%d", i), uint64(i+1), fmt.Sprintf(`{"id":"r%d","status":"s%d"}`, i, i)))
	}

	data, _ := encode(t, rows, "status")

	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	col, _ := f.Schema().Lookup("status")
	bf := f.RowGroups()[0].ColumnChunks()[col.ColumnIndex].(*parquet.FileColumnChunk).BloomFilter()

	if bf == nil {
		t.Fatal("indexed column has no bloom filter")
	}

	if ok, _ := bf.Check(parquet.ValueOf("s42")); !ok {
		t.Fatal("bloom filter lost a value that is present")
	}

	if ok, _ := bf.Check(parquet.ValueOf("never-written")); ok {
		t.Fatal("bloom filter says a missing value may exist")
	}
}

func TestZstdAndDictionaryEncoding(t *testing.T) {
	var rows []segment.Row

	for i := range 100 {
		rows = append(rows, row(fmt.Sprintf("r%d", i), uint64(i+1), fmt.Sprintf(`{"id":"r%d","status":"%s"}`, i, strings.Repeat("ok", i%3))))
	}

	data, _ := encode(t, rows, "status")

	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	md := f.Metadata()

	for _, cc := range md.RowGroups[0].Columns {
		if cc.MetaData.PathInSchema[0] != "status" {
			continue
		}

		if cc.MetaData.Codec != format.Zstd {
			t.Fatalf("codec = %v, want zstd", cc.MetaData.Codec)
		}

		if !hasEncoding(cc.MetaData.Encoding, format.RLEDictionary) {
			t.Fatalf("encodings = %v, want RLE_DICTIONARY", cc.MetaData.Encoding)
		}

		return
	}

	t.Fatal("status column not found in metadata")
}

func hasEncoding(list []format.Encoding, want format.Encoding) bool {
	for _, e := range list {
		if e == want {
			return true
		}
	}

	return false
}

// Field names are free text in JSON. Empty, dotted, unicode and reserved
// names must still encode and read back as raw documents.
func TestOddFieldNamesEncode(t *testing.T) {
	docs := []string{
		`{"id":"a","":1,"x.y":2}`,
		`{"id":"b","a b":"ü","日本":true,"q\"k":[1,2]}`,
		`{"id":"c","n":{"":{"":"deep"}}}`,
		`{"id":"d","$ref":1.5,"_id":"shadow","_rest":"r"}`,
	}

	var rows []segment.Row

	for i, d := range docs {
		rows = append(rows, row(fmt.Sprintf("d%d", i), uint64(i+1), d))
	}

	data, _ := encode(t, rows)

	reader, err := segment.Decode(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	for i, d := range docs {
		raw, err := reader.Raw(i)
		if err != nil {
			t.Fatal(err)
		}

		if string(raw) != d {
			t.Fatalf("raw %d = %s", i, raw)
		}
	}
}
