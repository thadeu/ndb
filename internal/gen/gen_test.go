package gen_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"testing"

	"github.com/thadeu/ndb/internal/gen"
)

func generate(t *testing.T, opts gen.Options) []byte {
	t.Helper()

	var buf bytes.Buffer

	if err := gen.Write(&buf, opts); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestSameSeedSameBytes(t *testing.T) {
	opts := gen.Options{Seed: 7, Bytes: 200_000}

	a := generate(t, opts)
	b := generate(t, opts)

	if !bytes.Equal(a, b) {
		t.Fatal("two runs with the same seed differ")
	}

	c := generate(t, gen.Options{Seed: 8, Bytes: 200_000})

	if bytes.Equal(a, c) {
		t.Fatal("different seeds gave the same bytes")
	}
}

func TestSizeAndValidLines(t *testing.T) {
	out := generate(t, gen.Options{Seed: 1, Bytes: 100_000, TypeConflicts: 0.1, Updates: 0.1, Deletes: 0.05})

	if len(out) < 100_000 || len(out) > 102_000 {
		t.Fatalf("size %d not near 100000", len(out))
	}

	sc := bufio.NewScanner(bytes.NewReader(out))
	seen := map[string]bool{}
	repeats, deletes, stringAges := 0, 0, 0

	for sc.Scan() {
		var doc map[string]any

		if err := json.Unmarshal(sc.Bytes(), &doc); err != nil {
			t.Fatalf("invalid line %q: %v", sc.Text(), err)
		}

		id, _ := doc["id"].(string)

		if seen[id] {
			repeats++
		}

		seen[id] = true

		if doc["_deleted"] == true {
			deletes++
		}

		if _, ok := doc["age"].(string); ok {
			stringAges++
		}
	}

	if repeats == 0 || deletes == 0 || stringAges == 0 {
		t.Fatalf("want repeats, deletes and string ages; got %d %d %d", repeats, deletes, stringAges)
	}
}

func TestLargeValues(t *testing.T) {
	out := generate(t, gen.Options{Seed: 1, Bytes: 50_000, LargeValues: 1, LargeSize: 8192})

	line, _, _ := bytes.Cut(out, []byte("\n"))

	if len(line) < 8192 {
		t.Fatalf("first line is %d bytes, want ≥ 8192", len(line))
	}
}
