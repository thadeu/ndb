package segment_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/thadeu/ndb/internal/gen"
	"github.com/thadeu/ndb/internal/segment"
)

// FuzzSchemaRoundTrip encodes mixed-type NDJSON from internal/gen and checks
// that every row comes back with its id, versions, tombstone flag and the
// exact raw document.
func FuzzSchemaRoundTrip(f *testing.F) {
	f.Add(uint64(1), uint8(40), uint8(30))
	f.Add(uint64(7), uint8(200), uint8(90))

	f.Fuzz(func(t *testing.T, seed uint64, lines uint8, conflicts uint8) {
		var buf bytes.Buffer

		err := gen.Write(&buf, gen.Options{
			Seed:          seed,
			Bytes:         int64(lines) * 120,
			TypeConflicts: float64(conflicts) / 100,
			Updates:       0.2,
			Deletes:       0.05,
			LargeValues:   0.02,
			LargeSize:     5000,
		})
		if err != nil {
			t.Fatal(err)
		}

		var rows []segment.Row

		sc := bufio.NewScanner(&buf)
		sc.Buffer(make([]byte, 1<<20), 1<<20)

		for i := 0; sc.Scan(); i++ {
			line := bytes.Clone(sc.Bytes())

			var top struct {
				ID      string `json:"id"`
				Deleted bool   `json:"_deleted"`
			}

			if err := json.Unmarshal(line, &top); err != nil {
				t.Fatal(err)
			}

			r := segment.Row{ID: top.ID, V: uint64(i + 1), Seq: uint64(i + 1), Tx: uint64(i + 1), Del: top.Deleted}

			if !top.Deleted {
				r.Raw = line
			}

			rows = append(rows, r)
		}

		if len(rows) == 0 {
			return
		}

		data, info, err := segment.NewWriter().Encode(rows, []string{"status"})
		if err != nil {
			t.Fatal(err)
		}

		if info.Rows != len(rows) {
			t.Fatalf("Info.Rows = %d, want %d", info.Rows, len(rows))
		}

		reader, err := segment.Decode(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}

		i := 0

		err = reader.Entries(func(n int, got segment.Row) error {
			want := rows[n]

			if got.ID != want.ID || got.V != want.V || got.Seq != want.Seq || got.Tx != want.Tx || got.Del != want.Del {
				t.Fatalf("row %d = %+v, want %+v", n, got, want)
			}

			if !want.Del {
				raw, err := reader.Raw(n)
				if err != nil {
					t.Fatal(err)
				}

				if !bytes.Equal(raw, want.Raw) {
					t.Fatalf("raw %d differs", n)
				}
			}

			i++

			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		if i != len(rows) {
			t.Fatalf("read %d rows, want %d", i, len(rows))
		}
	})
}
