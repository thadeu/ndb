package segment_test

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/parquet-go/parquet-go"
	"github.com/thadeu/ndb/internal/segment"
)

func row(id string, v uint64, doc string) segment.Row {
	return segment.Row{ID: id, V: v, Seq: v, Tx: v, Raw: []byte(doc)}
}

func encode(t *testing.T, rows []segment.Row, indexed ...string) ([]byte, segment.Info) {
	t.Helper()

	data, info, err := segment.NewWriter().Encode(rows, indexed)
	if err != nil {
		t.Fatal(err)
	}

	return data, info
}

func columnType(info segment.Info, name string) (string, bool) {
	for _, c := range info.Columns {
		if c.Name == name {
			return c.Type, true
		}
	}

	return "", false
}

// columnOf returns the values of one column for each row, nulls included.
func columnOf(t *testing.T, data []byte, name string) [][]parquet.Value {
	t.Helper()

	f, err := parquet.OpenFile(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	leaf, ok := f.Schema().Lookup(name)

	if !ok {
		t.Fatalf("no column %q", name)
	}

	var out [][]parquet.Value

	for _, rg := range f.RowGroups() {
		rows := rg.Rows()
		buf := make([]parquet.Row, 64)

		for {
			n, err := rows.ReadRows(buf)

			for _, r := range buf[:n] {
				var vals []parquet.Value

				for _, v := range r {
					if v.Column() == leaf.ColumnIndex {
						vals = append(vals, v.Clone())
					}
				}

				out = append(out, vals)
			}

			if err == io.EOF {
				break
			}

			if err != nil {
				t.Fatal(err)
			}
		}

		rows.Close()
	}

	return out
}

func TestFlattenNestedObjectsToDepthFour(t *testing.T) {
	rows := []segment.Row{row("a", 1, `{"id":"a","address":{"city":"SP","geo":{"lat":{"x":1}}},"deep":{"a":{"b":{"c":{"d":{"e":1}}}}}}`)}
	data, info := encode(t, rows)

	if typ, _ := columnType(info, "address.city"); typ != "string" {
		t.Fatalf("address.city type = %q, want string", typ)
	}

	if typ, _ := columnType(info, "address.geo.lat.x"); typ != "int" {
		t.Fatalf("address.geo.lat.x type = %q, want int", typ)
	}

	if typ, _ := columnType(info, "deep.a.b.c"); typ != "json" {
		t.Fatalf("object at depth 4 type = %q, want json", typ)
	}

	if _, ok := columnType(info, "deep.a.b.c.d.e"); ok {
		t.Fatal("value below depth 4 must not be a column")
	}

	if got := columnOf(t, data, "deep.a.b.c"); string(got[0][0].ByteArray()) != `{"d":{"e":1}}` {
		t.Fatalf("json column = %s", got[0][0].ByteArray())
	}
}

func TestArraysOfScalarsAreLists(t *testing.T) {
	rows := []segment.Row{
		row("a", 1, `{"id":"a","tags":["x","y"],"items":[{"k":1}]}`),
		row("b", 2, `{"id":"b","tags":[],"items":[]}`),
	}
	data, info := encode(t, rows)

	if typ, _ := columnType(info, "tags"); typ != "list<string>" {
		t.Fatalf("tags type = %q, want list<string>", typ)
	}

	if typ, _ := columnType(info, "items"); typ != "json" {
		t.Fatalf("items type = %q, want json", typ)
	}

	got := columnOf(t, data, "tags")

	if len(got[0]) != 2 || string(got[0][1].ByteArray()) != "y" {
		t.Fatalf("tags row 0 = %v", got[0])
	}

	if !got[1][0].IsNull() {
		t.Fatal("empty array must be a null list")
	}
}

func TestTypeConflictKeepsDominantTypeAndRest(t *testing.T) {
	rows := []segment.Row{
		row("a", 1, `{"id":"a","age":30}`),
		row("b", 2, `{"id":"b","age":31}`),
		row("c", 3, `{"id":"c","age":32}`),
		row("d", 4, `{"id":"d","age":"31"}`),
	}
	data, info := encode(t, rows)

	if typ, _ := columnType(info, "age"); typ != "int" {
		t.Fatalf("age type = %q, want int (dominant)", typ)
	}

	age := columnOf(t, data, "age")

	if age[0][0].Int64() != 30 || !age[3][0].IsNull() {
		t.Fatalf("age column = %v", age)
	}

	rest := columnOf(t, data, "_rest")

	if got := string(rest[3][0].ByteArray()); got != `{"age":"31"}` {
		t.Fatalf("_rest row 3 = %s", got)
	}

	if len(rest[0]) != 1 || !rest[0][0].IsNull() {
		t.Fatalf("_rest row 0 must be null, got %v", rest[0])
	}
}

func TestMissingFieldIsNull(t *testing.T) {
	rows := []segment.Row{
		row("a", 1, `{"id":"a","status":"ok"}`),
		row("b", 2, `{"id":"b"}`),
	}
	data, _ := encode(t, rows)

	got := columnOf(t, data, "status")

	if got[0][0].ByteArray() == nil || string(got[0][0].ByteArray()) != "ok" {
		t.Fatalf("status row 0 = %v", got[0])
	}

	if !got[1][0].IsNull() {
		t.Fatal("missing field must be null")
	}
}

func TestLargeStringIsNotAColumn(t *testing.T) {
	big := strings.Repeat("x", 5000)
	doc := `{"id":"a","payload":"` + big + `","note":"small"}`
	rows := []segment.Row{row("a", 1, doc)}
	data, info := encode(t, rows)

	if _, ok := columnType(info, "payload"); ok {
		t.Fatal("string above 4 KiB must not be a column")
	}

	if typ, _ := columnType(info, "note"); typ != "string" {
		t.Fatalf("note type = %q", typ)
	}

	reader, err := segment.Decode(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	raw, err := reader.Raw(0)
	if err != nil {
		t.Fatal(err)
	}

	if string(raw) != doc {
		t.Fatal("_raw must keep the full document")
	}
}

func TestInfoListsEveryColumnWithItsType(t *testing.T) {
	_, info := encode(t, []segment.Row{row("a", 1, `{"id":"a","n":1,"f":1.5,"b":true,"s":"x"}`)})

	want := map[string]string{
		"_id": "string", "_v": "int", "_seq": "int", "_tx": "int", "_del": "bool", "_raw": "string", "_rest": "string",
		"n": "int", "f": "float", "b": "bool", "s": "string",
	}

	for name, typ := range want {
		if got, ok := columnType(info, name); !ok || got != typ {
			t.Fatalf("column %s = %q (found %v), want %q", name, got, ok, typ)
		}
	}

	if info.Rows != 1 {
		t.Fatalf("Info.Rows = %d", info.Rows)
	}
}

func TestSchemaJSONIsStable(t *testing.T) {
	_, info := encode(t, []segment.Row{row("a", 1, `{"id":"a","z":1,"a":2}`)})

	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Contains(b, []byte(`"name":"a"`)) {
		t.Fatalf("Info must be JSON-serializable with names: %s", b)
	}
}
