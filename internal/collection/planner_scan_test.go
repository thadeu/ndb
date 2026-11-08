package collection_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
)

// explainOf runs body with explain on and returns the plan counters.
func explainOf(t *testing.T, c *collection.Collection, body string) *collection.Explain {
	t.Helper()

	var m map[string]any

	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}

	m["explain"] = true

	b, _ := json.Marshal(m)
	p := rq(t, c, string(b))

	if p.Explain == nil {
		t.Fatalf("%s: no explain", body)
	}

	return p.Explain
}

// bulk inserts docs in one transaction.
func bulk(t *testing.T, c *collection.Collection, docs []string) {
	t.Helper()

	if len(docs) == 0 {
		return
	}

	raw := make([][]byte, len(docs))

	for i, d := range docs {
		raw[i] = []byte(d)
	}

	if _, err := c.Insert(ctx, raw...); err != nil {
		t.Fatal(err)
	}
}

// sealBatch inserts docs and seals them into their own segment.
func sealBatch(t *testing.T, c *collection.Collection, docs []string) {
	t.Helper()

	bulk(t, c, docs)

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}
}

func rangeDocs(from, to int, format func(i int) string) []string {
	out := make([]string, 0, to-from)

	for i := from; i < to; i++ {
		out = append(out, format(i))
	}

	return out
}

// TestPlannerChoosesIDIndexOrScan covers the rule-based plan (plan §11.2).
func TestPlannerChoosesIDIndexOrScan(t *testing.T) {
	c := openMem(t)

	seed(t, c,
		`{"_id":"d1","status":"open","n":1}`,
		`{"_id":"d2","status":"closed","n":2}`,
		`{"_id":"d3","status":"open","n":3}`,
	)
	waitReady(t, c)

	cases := []struct {
		body string
		plan string
	}{
		{`{"query":{"_id":"d1"}}`, "id"},
		{`{"query":{"_id":{"$in":["d1","d2"]}}}`, "id"},
		{`{"query":{"status":"open"}}`, "index:status"},
		{`{"query":{"status":{"$gt":"a"}}}`, "index:status"},
		{`{"query":{"$or":[{"status":"open"},{"status":"closed"}]}}`, "scan"},
		{`{"query":{"n":{"$gt":1}}}`, "scan"},
		{`{"query":{"status":{"$regex":"^op"}}}`, "index:status"},
		{`{"query":{"status":{"$regex":"^OP","$options":"i"}}}`, "scan"},
		{`{"query":{"status":{"$regex":"pen"}}}`, "scan"},
		{`{"query":{"status":{"$not":{"$regex":"^op"}}}}`, "scan"},
	}

	for _, tc := range cases {
		if p := rq(t, c, tc.body); p.Plan != tc.plan {
			t.Fatalf("%s: plan %s, want %s", tc.body, p.Plan, tc.plan)
		}

		if e := explainOf(t, c, tc.body); e.Plan != tc.plan || e.RowsMatched != len(rq(t, c, tc.body).Rows) {
			t.Fatalf("%s: explain %+v", tc.body, e)
		}
	}

	if p := rq(t, c, `{"query":{"_id":"d1"}}`); p.Explain != nil {
		t.Fatal("explain must be nil unless requested")
	}
}

// TestPruningSkipsSegmentsAndRowGroups uses three segments with disjoint
// ranges of n. Zone maps skip whole segments and row groups before any row
// is read.
func TestPruningSkipsSegmentsAndRowGroups(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	sealBatch(t, c, rangeDocs(0, 2000, func(i int) string {
		return fmt.Sprintf(`{"_id":"a%d","status":"a","n":%d}`, i, i)
	}))
	sealBatch(t, c, rangeDocs(0, 2000, func(i int) string {
		return fmt.Sprintf(`{"_id":"b%d","status":"a","n":%d}`, i, 2000+i)
	}))
	sealBatch(t, c, rangeDocs(0, 2000, func(i int) string {
		return fmt.Sprintf(`{"_id":"c%d","status":"a","n":%d}`, i, 4000+i)
	}))

	e := explainOf(t, c, `{"query":{"n":{"$gte":5500}}}`)

	if e.SegmentsRead != 1 || e.SegmentsSkipped != 2 {
		t.Fatalf("segments read %d skipped %d", e.SegmentsRead, e.SegmentsSkipped)
	}

	// Segment 2 has two groups: n 4000..5023 (zone max below 5500, skipped)
	// and n 5024..5999 (read). Segments 0 and 1 have two groups each.
	if e.RowGroupsSkipped != 5 || e.RowGroupsRead != 1 {
		t.Fatalf("row groups read %d skipped %d", e.RowGroupsRead, e.RowGroupsSkipped)
	}

	if e.RowsRead != 976 || e.RowsMatched != 500 {
		t.Fatalf("rows read %d matched %d", e.RowsRead, e.RowsMatched)
	}
}

// TestBloomSkipsOnEquality drops the index so the scan path runs, while the
// segments keep their bloom filters for status.
func TestBloomSkipsOnEquality(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	sealBatch(t, c, rangeDocs(0, 1500, func(i int) string {
		return fmt.Sprintf(`{"_id":"a%d","status":%q}`, i, []string{"a", "z"}[i%2])
	}))
	sealBatch(t, c, rangeDocs(0, 1500, func(i int) string {
		return fmt.Sprintf(`{"_id":"b%d","status":"a"}`, i)
	}))
	sealBatch(t, c, rangeDocs(0, 1500, func(i int) string {
		return fmt.Sprintf(`{"_id":"c%d","status":%q}`, i, []string{"a", "m"}[i%2])
	}))

	c.DropIndex("status")

	e := explainOf(t, c, `{"query":{"status":"m"}}`)

	if e.Plan != "scan" || e.RowsMatched != 750 {
		t.Fatalf("plan %s matched %d", e.Plan, e.RowsMatched)
	}

	if e.SegmentsSkipped != 2 || e.SegmentsRead != 1 {
		t.Fatalf("segments read %d skipped %d", e.SegmentsRead, e.SegmentsSkipped)
	}
}

// refAndSealed opens two collections with the same def: ref never seals,
// and sealed writes its memtable to segments. The tests run the same
// operations on both and compare their answers.
func refAndSealed(t *testing.T, cfg collection.Config) (*collection.Collection, *collection.Collection) {
	t.Helper()

	sealed := openSealed(t, filepath.Join(t.TempDir(), "s"), cfg)

	// Without the index, every query of sealed runs the column scan.
	sealed.DropIndex("status")

	return openMem(t), sealed
}

func mixedDoc(i int) string {
	parts := []string{fmt.Sprintf(`"_id":"d%04d"`, i), fmt.Sprintf(`"status":"s%d"`, i%4)}

	switch i % 5 {
	case 0:
		parts = append(parts, fmt.Sprintf(`"n":%d`, i))
	case 1:
		parts = append(parts, fmt.Sprintf(`"n":"str%d"`, i))
	case 3:
		parts = append(parts, `"n":null`)
	case 4:
		parts = append(parts, fmt.Sprintf(`"n":%d.5`, i))
	}

	if i%3 == 0 {
		parts = append(parts, `"tags":["t1","t2"]`)
	}

	if i%7 == 0 {
		parts = append(parts, `"empty":[]`)
	}

	if i%4 == 0 {
		parts = append(parts, fmt.Sprintf(`"meta":{"k":%d}`, i%3))
	}

	if i%6 == 0 {
		parts = append(parts, `"items":[{"sku":"a"},{"sku":"b"}]`)
	}

	if i%8 == 0 {
		parts = append(parts, `"meta2":{}`)
	}

	parts = append(parts, fmt.Sprintf(`"at":"2026-0%d-1%dT10:00:00Z"`, 1+i%3, i%9))

	return "{" + strings.Join(parts, ",") + "}"
}

var diffQueries = []string{
	`{}`,
	`{"query":{"status":"s1"}}`,
	`{"query":{"n":{"$gte":100,"$lt":300}}}`,
	`{"query":{"n":"str6"}}`,
	`{"query":{"n":{"$gt":"str"}}}`,
	`{"query":{"n":{"$exists":false}}}`,
	`{"query":{"n":{"$exists":true},"status":{"$ne":"s0"}}}`,
	`{"query":{"n":7.5}}`,
	`{"query":{"n":{"$in":[3,"str6",7.5]}}}`,
	`{"query":{"n":null}}`,
	`{"query":{"tags":"t2"}}`,
	`{"query":{"tags":["t1","t2"]}}`,
	`{"query":{"empty":[]}}`,
	`{"query":{"meta.k":1}}`,
	`{"query":{"meta":{"k":2}}}`,
	`{"query":{"items.sku":"a"}}`,
	`{"query":{"meta2":{}}}`,
	`{"query":{"$or":[{"status":"s2"},{"n":3}]}}`,
	`{"query":{"$not":{"status":"s1"}}}`,
	`{"query":{"n":{"$gt":50}},"sort":{"n":-1,"status":1},"limit":7}`,
	`{"sort":{"status":1,"n":-1},"limit":10,"skip":3}`,
	`{"query":{"status":"s3"},"fields":["status","meta.k"],"count":true}`,
	`{"query":{"at":{"$gte":"2026-02-01T00:00:00Z"}},"sort":{"at":-1},"limit":9}`,
	`{"query":{"status":"s9"}}`,
}

// sameAnswers checks every query on both collections, including the pages
// that a cursor walks.
func sameAnswers(t *testing.T, ref, sealed *collection.Collection, bodies []string) {
	t.Helper()

	for _, body := range bodies {
		want := answersOf(t, ref, body)
		got := answersOf(t, sealed, body)

		if !slices.Equal(got, want) {
			t.Fatalf("%s:\n got %q\nwant %q", body, got, want)
		}
	}
}

func answersOf(t *testing.T, c *collection.Collection, body string) []string {
	t.Helper()

	p := rq(t, c, body, "at")

	out := make([]string, 0, len(p.Rows))

	for _, r := range p.Rows {
		out = append(out, string(r))
	}

	if p.Total != nil {
		out = append(out, fmt.Sprintf("total=%d", *p.Total))
	}

	return out
}

// TestColumnScanEqualsRowEvaluator runs the same documents, in memtable and
// in segments, through queries that use conflicts, nulls, lists, empty
// objects, dot paths and time strings.
func TestColumnScanEqualsRowEvaluator(t *testing.T) {
	ref, sealed := refAndSealed(t, collection.Config{ScanWorkers: 2})
	docs := rangeDocs(0, 400, mixedDoc)

	bulk(t, ref, docs)
	bulk(t, sealed, docs[:250])

	if err := sealed.Seal(); err != nil {
		t.Fatal(err)
	}

	bulk(t, sealed, docs[250:])

	if err := sealed.Seal(); err != nil {
		t.Fatal(err)
	}

	sameAnswers(t, ref, sealed, diffQueries)

	e := explainOf(t, sealed, `{"query":{"status":"s1"}}`)

	if e.Plan != "scan" || e.RawFetched != e.RowsMatched || e.RowsRead <= e.RowsMatched {
		t.Fatalf("column scan must fetch raw only for matches: %+v", e)
	}
}

// TestLastVersionAndTombstonesAcrossSegments updates and deletes documents
// that live in older segments, and checks the answers again.
func TestLastVersionAndTombstonesAcrossSegments(t *testing.T) {
	ref, sealed := refAndSealed(t, collection.Config{})
	docs := rangeDocs(0, 300, mixedDoc)

	bulk(t, ref, docs)
	bulk(t, sealed, docs)

	if err := sealed.Seal(); err != nil {
		t.Fatal(err)
	}

	for _, c := range []*collection.Collection{ref, sealed} {
		for i := 0; i < 300; i += 3 {
			ch := collection.Change{Set: map[string]any{"status": "s1", "n": i * 10}}

			if _, err := c.UpdateByID(ctx, fmt.Sprintf("d%04d", i), ch, 0); err != nil {
				t.Fatal(err)
			}
		}

		for i := 1; i < 300; i += 11 {
			if err := c.DeleteByID(ctx, fmt.Sprintf("d%04d", i)); err != nil {
				t.Fatal(err)
			}
		}
	}

	sameAnswers(t, ref, sealed, diffQueries)

	if err := sealed.Seal(); err != nil {
		t.Fatal(err)
	}

	sameAnswers(t, ref, sealed, diffQueries)
}

// TestParallelScanWorkersAgree runs the same queries with one and with four
// scan workers, and with the memtable, which is merged last.
func TestParallelScanWorkersAgree(t *testing.T) {
	ref := openMem(t)
	bulk(t, ref, rangeDocs(0, 900, mixedDoc))

	for _, workers := range []int{1, 4} {
		sealed := openSealed(t, filepath.Join(t.TempDir(), fmt.Sprint("w", workers)), collection.Config{ScanWorkers: workers})

		sealed.DropIndex("status")

		bulk(t, sealed, rangeDocs(0, 300, mixedDoc))

		if err := sealed.Seal(); err != nil {
			t.Fatal(err)
		}

		bulk(t, sealed, rangeDocs(300, 600, mixedDoc))

		if err := sealed.Seal(); err != nil {
			t.Fatal(err)
		}

		bulk(t, sealed, rangeDocs(600, 900, mixedDoc))

		sameAnswers(t, ref, sealed, diffQueries)
	}
}

func TestColumnScanFetchesRawOnlyForMatches(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	sealBatch(t, c, rangeDocs(0, 1200, func(i int) string {
		return fmt.Sprintf(`{"_id":"d%d","status":"s%d","n":%d}`, i, i%3, i)
	}))

	c.DropIndex("status")

	e := explainOf(t, c, `{"query":{"status":"s1"}}`)

	if e.RowsRead != 1200 || e.RowsMatched != 400 {
		t.Fatalf("rows read %d matched %d", e.RowsRead, e.RowsMatched)
	}

	if e.RawFetched != 400 {
		t.Fatalf("raw fetched %d, want only the matches", e.RawFetched)
	}
}

func TestIDCacheCountsHits(t *testing.T) {
	c := openMem(t)

	seed(t, c, `{"_id":"d1","n":1}`, `{"_id":"d2","n":2}`)

	first := explainOf(t, c, `{"query":{"_id":"d1"}}`)
	second := explainOf(t, c, `{"query":{"_id":"d1"}}`)

	if first.CacheHits != 0 || second.CacheHits != 1 {
		t.Fatalf("cache hits first %d second %d", first.CacheHits, second.CacheHits)
	}

	if _, err := c.UpdateByID(ctx, "d1", collection.Change{Set: map[string]any{"n": 5}}, 0); err != nil {
		t.Fatal(err)
	}

	after := explainOf(t, c, `{"query":{"_id":"d1"}}`)

	if after.CacheHits != 0 {
		t.Fatalf("an updated document must not hit the cache: %d", after.CacheHits)
	}
}

// walkPages follows the cursor of body and returns every row, in order.
func walkPages(t *testing.T, c *collection.Collection, body string) []string {
	t.Helper()

	var out []string

	next := ""

	for range 200 {
		b := body

		if next != "" {
			b = fmt.Sprintf(`{"after":%q,%s`, next, body[1:])
		}

		p := rq(t, c, b, "at")

		for _, r := range p.Rows {
			out = append(out, string(r))
		}

		if p.Next == "" {
			return out
		}

		next = p.Next
	}

	t.Fatalf("%s: cursor did not end", body)

	return nil
}

// TestCursorWalkOverSegmentsEqualsMemtable pages through sorted queries on
// both collections. Keyset pages must not skip or repeat rows across
// segments and memtable.
func TestCursorWalkOverSegmentsEqualsMemtable(t *testing.T) {
	ref, sealed := refAndSealed(t, collection.Config{ScanWorkers: 3})

	docs := rangeDocs(0, 500, mixedDoc)

	bulk(t, ref, docs)
	bulk(t, sealed, docs[:200])

	if err := sealed.Seal(); err != nil {
		t.Fatal(err)
	}

	bulk(t, sealed, docs[200:350])

	if err := sealed.Seal(); err != nil {
		t.Fatal(err)
	}

	bulk(t, sealed, docs[350:])

	for _, body := range []string{
		`{"query":{"n":{"$gte":20}},"sort":{"n":-1,"status":1},"limit":6}`,
		`{"sort":{"status":1,"n":1},"limit":11}`,
		`{"query":{"status":"s2"},"limit":5,"snapshot":true}`,
	} {
		want := walkPages(t, ref, body)
		got := walkPages(t, sealed, body)

		if !slices.Equal(got, want) || len(got) == 0 {
			t.Fatalf("%s: got %d rows, want %d", body, len(got), len(want))
		}
	}
}

func TestScanFindsExplicitNullInAllNullGroup(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	sealBatch(t, c, []string{`{"_id":"a","x":null}`, `{"_id":"b","x":null}`})

	if got := rq(t, c, `{"query":{"x":null}}`).Rows; len(got) != 2 {
		t.Fatalf("x:null over a sealed all-null group: %d rows, want 2", len(got))
	}
}

func TestScanFindsExplicitNullInNullRowGroupOfNumberColumn(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	docs := rangeDocs(0, 1024, func(i int) string { return fmt.Sprintf(`{"_id":"n%d","x":%d}`, i, i) })
	docs = append(docs, rangeDocs(0, 1024, func(i int) string { return fmt.Sprintf(`{"_id":"z%d","x":null}`, i) })...)

	sealBatch(t, c, docs)

	if got := rq(t, c, `{"query":{"x":null}}`).Rows; len(got) != 1024 {
		t.Fatalf("x:null: %d rows, want 1024", len(got))
	}
}

func TestQueryWithZeroScanWorkersDoesNotHang(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{ScanWorkers: -1})

	sealBatch(t, c, []string{`{"_id":"a"}`})

	done := make(chan int, 1)

	go func() { done <- len(rq(t, c, `{"query":{}}`).Rows) }()

	select {
	case n := <-done:
		if n != 1 {
			t.Fatalf("%d rows, want 1", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query hung")
	}
}

func TestVersionsFlagReturnsSnapshotVersion(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	sealBatch(t, c, []string{`{"_id":"sealed"}`})
	bulk(t, c, []string{`{"_id":"mem"}`})

	if _, err := c.UpdateByID(ctx, "sealed", collection.Change{Set: map[string]any{"n": 1}}, 0); err != nil {
		t.Fatal(err)
	}

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	bulk(t, c, []string{`{"_id":"tail"}`})

	rows := rq(t, c, `{"query":{},"sort":{"_id":1},"versions":true}`).Rows
	got := map[string]float64{}

	for _, r := range rows {
		var m map[string]any

		if err := json.Unmarshal(r, &m); err != nil {
			t.Fatal(err)
		}

		got[m["_id"].(string)], _ = m["_v"].(float64)
	}

	want := map[string]float64{"mem": 1, "sealed": 2, "tail": 1}

	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}

	plain := rq(t, c, `{"query":{"_id":"tail"}}`).Rows

	if strings.Contains(string(plain[0]), `"_v"`) {
		t.Fatalf("_v without the flag: %s", plain[0])
	}
}

// TestRegexPrefixPrunesSegments checks that an anchored literal prefix skips
// the segments whose zone map is outside the prefix range, and that an
// unanchored pattern reads them all with the same answer.
func TestRegexPrefixPrunesSegments(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	for _, p := range []string{"a", "b", "c"} {
		sealBatch(t, c, rangeDocs(0, 2000, func(i int) string {
			return fmt.Sprintf(`{"_id":"%s%d","name":"%s-%04d"}`, p, i, p, i)
		}))
	}

	anchored := explainOf(t, c, `{"query":{"name":{"$regex":"^b-"}}}`)

	if anchored.SegmentsRead != 1 || anchored.SegmentsSkipped != 2 || anchored.RowsMatched != 2000 {
		t.Fatalf("anchored: %+v", anchored)
	}

	open := explainOf(t, c, `{"query":{"name":{"$regex":"b-"}}}`)

	if open.SegmentsSkipped != 0 || open.RowsMatched != 2000 {
		t.Fatalf("unanchored: %+v", open)
	}

	folded := explainOf(t, c, `{"query":{"name":{"$regex":"^B-","$options":"i"}}}`)

	if folded.SegmentsSkipped != 0 || folded.RowsMatched != 2000 {
		t.Fatalf("case folded: %+v", folded)
	}
}

// A count with no conditions adds whole row groups from their live bits when
// the zone map proves that no row is past the retention, and reads the
// groups near the cutoff.
func TestCountAddsVisibleGroupsWithoutReading(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	def := catalog.Def{Name: "events", Time: "ts", Retention: "7d"}

	c, err := collection.Open(collection.Config{Dir: filepath.Join(root, "wal"), Data: root, FS: fs.OS{}, Def: def, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	var docs [][]byte

	for i := range 5000 {
		at := now.Add(-time.Duration(i) * time.Minute)

		if i >= 4000 {
			at = now.Add(-10 * 24 * time.Hour).Add(-time.Duration(i) * time.Second)
		}

		docs = append(docs, fmt.Appendf(nil, `{"_id":"e%05d","ts":%q}`, i, at.Format(time.RFC3339)))
	}

	if _, err := c.Insert(ctx, docs...); err != nil {
		t.Fatal(err)
	}

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	remove(t, c, "e00010")

	q, _ := c.ParseQuery([]byte(`{"count":true,"limit":1,"explain":true}`))

	page, err := c.Query(ctx, q)
	if err != nil {
		t.Fatal(err)
	}

	if *page.Total != 3999 {
		t.Fatalf("count = %d, want 3999", *page.Total)
	}

	if page.Explain.RowGroupsRead >= 5 {
		t.Fatalf("row groups read = %d, want only the first and the ones near the cutoff", page.Explain.RowGroupsRead)
	}
}

// A sort against the insertion order (newest first) makes each row the best
// so far, and a skip passes rows: the documents are read only for the rows
// of the page.
func TestSortedTopKReadsOnlyTheDocumentsItReturns(t *testing.T) {
	c := openSealed(t, t.TempDir(), collection.Config{})

	sealBatch(t, c, rangeDocs(0, 1200, func(i int) string {
		return fmt.Sprintf(`{"_id":"d%04d","status":"s%d","n":%d}`, i, i%3, i)
	}))

	for body, want := range map[string]int{
		`{"query":{"status":"s1"},"sort":{"n":-1},"limit":5}`:            5,
		`{"sort":{"n":-1},"limit":5}`:                                    5,
		`{"query":{"status":"s1"},"sort":{"n":-1},"limit":20,"skip":40}`: 20,
	} {
		e := explainOf(t, c, body)

		if e.RawFetched != want {
			t.Fatalf("%s: raw fetched %d, want only the %d rows of the page (%+v)", body, e.RawFetched, want, e)
		}
	}

	page := rq(t, c, `{"query":{"status":"s1"},"sort":{"n":-1},"limit":3}`)

	if got := ids(t, page); !slices.Equal(got, []string{"d1198", "d1195", "d1192"}) {
		t.Fatalf("top 3 = %v", got)
	}
}
