package segment

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/parquet-go/parquet-go"
)

type bloomRow struct {
	S  string  `parquet:"s"`
	I  int64   `parquet:"i"`
	D  float64 `parquet:"d"`
	I3 int32   `parquet:"i3"`
	F  float32 `parquet:"f"`
	B  bool    `parquet:"b"`
}

// The filter in memory answers as the filter of parquet-go, for every kind
// of value, present or not.
func TestMemBloomAnswersAsParquet(t *testing.T) {
	var buf bytes.Buffer

	cols := []string{"s", "i", "d", "i3", "f", "b"}
	filters := make([]parquet.BloomFilterColumn, len(cols))

	for i, c := range cols {
		filters[i] = parquet.SplitBlockFilter(10, c)
	}

	w := parquet.NewGenericWriter[bloomRow](&buf, parquet.BloomFilters(filters...))

	rows := make([]bloomRow, 300)

	for i := range rows {
		rows[i] = bloomRow{S: fmt.Sprintf("s%d", i), I: int64(i * 7), D: float64(i) / 3, I3: int32(i * 5), F: float32(i) / 7, B: i%2 == 0}
	}

	if _, err := w.Write(rows); err != nil {
		t.Fatal(err)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	f, err := parquet.OpenFile(bytes.NewReader(buf.Bytes()), int64(buf.Len()))

	if err != nil {
		t.Fatal(err)
	}

	for _, c := range cols {
		leaf, _ := f.Schema().Lookup(c)
		fb := f.RowGroups()[0].ColumnChunks()[leaf.ColumnIndex].BloomFilter()
		mem := loadBloom(fb)

		if mem == nil {
			t.Fatalf("%s: the filter was not read", c)
		}

		for i := range 2000 {
			v := probeValue(c, i)
			want, _ := fb.Check(v)

			if got := mem.Check(v); got != want {
				t.Fatalf("%s = %v: memory says %v, parquet says %v", c, v, got, want)
			}
		}
	}
}

// probeValue is a value of column c: the first 300 are in the file.
func probeValue(c string, i int) parquet.Value {
	switch c {
	case "s":
		return parquet.ValueOf(fmt.Sprintf("s%d", i))
	case "i":
		return parquet.ValueOf(int64(i * 7))
	case "d":
		return parquet.ValueOf(float64(i) / 3)
	case "i3":
		return parquet.ValueOf(int32(i * 5))
	case "f":
		return parquet.ValueOf(float32(i) / 7)
	default:
		return parquet.ValueOf(i%2 == 0)
	}
}

func TestLoadBloomOfAFilterThatCannotBeRead(t *testing.T) {
	if loadBloom(brokenBloom{}) != nil {
		t.Fatal("a filter that cannot be read must give nil")
	}
}

type brokenBloom struct{}

func (brokenBloom) ReadAt([]byte, int64) (int, error) { return 0, fmt.Errorf("broken") }
func (brokenBloom) Size() int64                       { return 64 }
func (brokenBloom) Check(parquet.Value) (bool, error) { return true, nil }
