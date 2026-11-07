package segment_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/segment"
)

func writeKeys(t *testing.T, entries []segment.KeyEntry, meta, bloom bool) *segment.Keys {
	t.Helper()

	path := filepath.Join(t.TempDir(), "k")

	if err := segment.WriteKeys(fs.OS{}, path, entries, meta, bloom); err != nil {
		t.Fatal(err)
	}

	k, err := segment.OpenKeys(fs.OS{}, path)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { k.Close() })

	return k
}

func TestKeysFindWithMeta(t *testing.T) {
	var entries []segment.KeyEntry

	for i := range 1000 {
		entries = append(entries, segment.KeyEntry{Key: fmt.Appendf(nil, "id-%05d", (i*7919)%1000), Row: i, V: uint64(i + 1), Seq: uint64(i + 10), Tx: uint64(i + 100), Del: i%3 == 0})
	}

	k := writeKeys(t, entries, true, true)

	if k.Len() != 1000 || k.MaxSeq() != 1009 {
		t.Fatalf("len %d maxSeq %d", k.Len(), k.MaxSeq())
	}

	for i := range 1000 {
		key := fmt.Appendf(nil, "id-%05d", (i*7919)%1000)

		e, ok, err := k.Find(key)
		if err != nil || !ok {
			t.Fatalf("find %s: %v %v", key, ok, err)
		}

		if e.Row != i || e.V != uint64(i+1) || e.Seq != uint64(i+10) || e.Tx != uint64(i+100) || e.Del != (i%3 == 0) {
			t.Fatalf("find %s = %+v", key, e)
		}
	}

	misses := 0

	for i := range 10_000 {
		if k.MayHave(fmt.Appendf(nil, "absent-%d", i)) {
			misses++
		}
	}

	if misses > 300 {
		t.Fatalf("bloom false positives: %d of 10000", misses)
	}

	if _, ok, _ := k.Find([]byte("id-99999")); ok {
		t.Fatal("found an absent key")
	}
}

func TestKeysRangeWithRepeatedKeys(t *testing.T) {
	var entries []segment.KeyEntry

	for i := range 2000 {
		entries = append(entries, segment.KeyEntry{Key: fmt.Appendf(nil, "s%d", i%4), Row: i})
	}

	k := writeKeys(t, entries, false, false)

	count := func(lo, hi string, loIncl, hiIncl bool) int {
		n := 0

		var lb, hb []byte

		if lo != "" {
			lb = []byte(lo)
		}

		if hi != "" {
			hb = []byte(hi)
		}

		err := k.Range(lb, hb, loIncl, hiIncl, func(segment.KeyEntry) bool {
			n++

			return true
		})
		if err != nil {
			t.Fatal(err)
		}

		return n
	}

	cases := []struct {
		lo, hi         string
		loIncl, hiIncl bool
		want           int
	}{
		{"s1", "s1", true, true, 500},
		{"s1", "s2", true, true, 1000},
		{"s1", "s2", false, true, 500},
		{"s1", "s3", true, false, 1000},
		{"", "s1", true, true, 1000},
		{"s2", "", true, true, 1000},
		{"", "", true, true, 2000},
		{"s9", "", true, true, 0},
	}

	for _, c := range cases {
		if got := count(c.lo, c.hi, c.loIncl, c.hiIncl); got != c.want {
			t.Errorf("range %+v = %d", c, got)
		}
	}

	rows := 0

	k.Range([]byte("s3"), []byte("s3"), true, true, func(e segment.KeyEntry) bool {
		if e.Row%4 != 3 {
			t.Fatalf("row %d under s3", e.Row)
		}

		rows++

		return true
	})

	if rows != 500 {
		t.Fatalf("s3 rows = %d", rows)
	}
}

func TestKeysEmpty(t *testing.T) {
	k := writeKeys(t, nil, true, true)

	if _, ok, err := k.Find([]byte("x")); ok || err != nil {
		t.Fatalf("find on empty: %v %v", ok, err)
	}
}
