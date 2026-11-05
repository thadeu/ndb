package segment_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/thadeu/ndb/internal/segment"
)

// scanRows makes n live rows. "n" is the row number, "status" is "closed"
// on every third row, "tag" is a number except on row 5, where it is a string
// (a type conflict), and "tags" is a two-element list on even rows.
func scanRows(n int) []segment.Row {
	rows := make([]segment.Row, n)

	for i := range rows {
		status := "open"

		if i%3 == 0 {
			status = "closed"
		}

		tag := fmt.Sprintf(`%d`, i)

		if i == 5 {
			tag = `"five"`
		}

		tags := ""

		if i%2 == 0 {
			tags = fmt.Sprintf(`,"tags":["even","r%d"]`, i)
		}

		raw := fmt.Sprintf(`{"id":"r%d","n":%d,"status":%q,"tag":%s%s}`, i, i, status, tag, tags)

		rows[i] = segment.Row{ID: fmt.Sprintf("r%d", i), V: 1, Seq: uint64(i + 1), Tx: 1, Raw: []byte(raw)}
	}

	return rows
}

func openReader(t *testing.T, rows []segment.Row, indexed ...string) segment.Reader {
	t.Helper()

	data, _ := encode(t, rows, indexed...)

	r, err := segment.Decode(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func TestGroupsAndColumnInfo(t *testing.T) {
	r := openReader(t, scanRows(2500), "status")

	want := []segment.Group{{Start: 0, Rows: 1024}, {Start: 1024, Rows: 1024}, {Start: 2048, Rows: 452}}

	if got := r.Groups(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}

	if c, ok := r.ColumnOf("n"); !ok || c.Type != "int" || c.Mixed {
		t.Fatalf("n = %+v %v", c, ok)
	}

	if c, ok := r.ColumnOf("tag"); !ok || c.Type != "int" || !c.Mixed {
		t.Fatalf("tag = %+v %v, want mixed", c, ok)
	}

	if _, ok := r.ColumnOf("missing"); ok {
		t.Fatal("missing path must not be a column")
	}
}

func TestZoneMapsPerGroup(t *testing.T) {
	r := openReader(t, scanRows(2500), "status")

	z, ok := r.Zone(0, "n")

	if !ok || z.Lo.Num != 0 || z.Hi.Num != 1023 {
		t.Fatalf("zone 0 n = %+v %v", z, ok)
	}

	z, ok = r.Zone(2, "n")

	if !ok || z.Lo.Num != 2048 || z.Hi.Num != 2499 {
		t.Fatalf("zone 2 n = %+v %v", z, ok)
	}

	if _, ok := r.Zone(0, "tag"); ok {
		t.Fatal("a mixed column has no usable zone")
	}
}

func TestBloomPerGroup(t *testing.T) {
	r := openReader(t, scanRows(2500), "status")

	if may, known := r.Bloom(0, "status", segment.Scalar{Kind: segment.ScalarString, Str: "closed"}); !known || !may {
		t.Fatalf("closed in group 0: may %v known %v", may, known)
	}

	if may, known := r.Bloom(0, "status", segment.Scalar{Kind: segment.ScalarString, Str: "never"}); !known || may {
		t.Fatalf("never in group 0: may %v known %v", may, known)
	}

	if _, known := r.Bloom(0, "n", segment.Scalar{Kind: segment.ScalarNumber, Num: 3}); known {
		t.Fatal("a column without a filter is not known")
	}
}

func TestReadGroupCellsAndRest(t *testing.T) {
	r := openReader(t, scanRows(20), "status")

	g, err := r.ReadGroup(0, []string{"n", "tag", "tags", "missing"})
	if err != nil {
		t.Fatal(err)
	}

	if g.IDs[3] != "r3" || g.Seqs[3] != 4 {
		t.Fatalf("id/seq of row 3 = %s/%d", g.IDs[3], g.Seqs[3])
	}

	n := g.Cells[0][3]

	if !n.Known || n.List || n.Vals[0].Num != 3 {
		t.Fatalf("n[3] = %+v", n)
	}

	tag := g.Cells[1][5]

	if !tag.Known || tag.Vals[0].Kind != segment.ScalarString || tag.Vals[0].Str != "five" {
		t.Fatalf("tag[5] from _rest = %+v", tag)
	}

	tags := g.Cells[2][2]

	if !tags.Known || !tags.List || len(tags.Vals) != 2 || tags.Vals[1].Str != "r2" {
		t.Fatalf("tags[2] = %+v", tags)
	}

	if g.Cells[2][1].Known {
		t.Fatal("tags[1] is absent: it must be undecided")
	}

	if g.Cells[3][0].Known {
		t.Fatal("a path that is not a column must be undecided")
	}
}
