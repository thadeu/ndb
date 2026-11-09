package agg_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/query"
)

// run feeds docs (in order, Seq = position + 1) to one table per split part
// and merges them, then finishes the pipeline. split 0 means one table.
func run(t *testing.T, body string, split int, docs ...string) []string {
	t.Helper()

	p, err := parse(t, body)
	if err != nil {
		t.Fatal(err)
	}

	budget := agg.NewBudget(1 << 30)
	tables := []*agg.Table{p.NewTable(budget)}

	for i, d := range docs {
		if split > 0 && i > 0 && i%split == 0 {
			tables = append(tables, p.NewTable(budget))
		}

		m, err := query.Decode([]byte(d))
		if err != nil {
			t.Fatal(err)
		}

		if !p.Query.Match(m) {
			continue
		}

		pos := query.Pos{Keys: p.Query.SortKeys(m), ID: string(rune('a' + i)), Seq: uint64(i + 1)}

		if err := tables[len(tables)-1].Add(p.Values(m), pos); err != nil {
			t.Fatal(err)
		}
	}

	for _, o := range tables[1:] {
		if err := tables[0].Merge(o); err != nil {
			t.Fatal(err)
		}
	}

	res, err := p.Finish(tables[0].Rows())
	if err != nil {
		t.Fatal(err)
	}

	out := make([]string, len(res.Rows))

	for i, r := range res.Rows {
		out[i] = string(r)
	}

	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("\n got  %s\n want %s", strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
}

var abc = []string{
	`{"s":"a","n":1,"ts":"2026-11-01T10:15:00Z"}`,
	`{"s":"b","n":2,"ts":"2026-11-01T10:45:00Z"}`,
	`{"s":"a","n":3,"ts":"2026-11-01T11:05:00Z"}`,
	`{"s":"a","n":null,"ts":"2026-11-02T00:00:00Z"}`,
	`{"s":"b","n":"x"}`,
	`{"n":5}`,
}

func TestGroupAccumulators(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":"$s","c":{"$count":{}},"sum":{"$sum":"$n"},"avg":{"$avg":"$n"},"lo":{"$min":"$n"},"hi":{"$max":"$n"}}}]}`

	for _, split := range []int{0, 1, 2, 4} {
		eq(t, run(t, body, split, abc...),
			`{"_id":null,"avg":5,"c":1,"hi":5,"lo":5,"sum":5}`,
			`{"_id":"a","avg":2,"c":3,"hi":3,"lo":1,"sum":4}`,
			`{"_id":"b","avg":2,"c":2,"hi":"x","lo":2,"sum":2}`,
		)
	}
}

func TestSumOfNothingAvgNull(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":null,"sum":{"$sum":"$zz"},"avg":{"$avg":"$zz"},"lo":{"$min":"$zz"},"f":{"$first":"$zz"}}}]}`

	eq(t, run(t, body, 0, `{"a":1}`), `{"_id":null,"avg":null,"f":null,"lo":null,"sum":0}`)
}

func TestCompositeIDAndDateTrunc(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":{"s":"$s","h":{"$dateTrunc":{"date":"$ts","unit":"hour"}}},"c":{"$count":{}}}}]}`

	eq(t, run(t, body, 0, abc...),
		`{"_id":{"h":null,"s":null},"c":1}`,
		`{"_id":{"h":"2026-11-01T10:00:00Z","s":"a"},"c":1}`,
		`{"_id":{"h":"2026-11-01T11:00:00Z","s":"a"},"c":1}`,
		`{"_id":{"h":"2026-11-02T00:00:00Z","s":"a"},"c":1}`,
		`{"_id":{"h":null,"s":"b"},"c":1}`,
		`{"_id":{"h":"2026-11-01T10:00:00Z","s":"b"},"c":1}`,
	)
}

func TestFirstLastInsertionOrderAndMerge(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":"$s","f":{"$first":"$n"},"l":{"$last":"$n"}}}]}`

	for _, split := range []int{0, 1, 3} {
		eq(t, run(t, body, split, abc...),
			`{"_id":null,"f":5,"l":5}`,
			`{"_id":"a","f":1,"l":null}`,
			`{"_id":"b","f":2,"l":"x"}`,
		)
	}
}

func TestFirstLastFollowSortBeforeGroup(t *testing.T) {
	body := `{"pipeline":[{"$sort":{"n":-1}},{"$group":{"_id":"$s","f":{"$first":"$n"},"l":{"$last":"$n"}}}]}`

	for _, split := range []int{0, 1, 3} {
		eq(t, run(t, body, split, abc...),
			`{"_id":null,"f":5,"l":5}`,
			`{"_id":"a","f":3,"l":null}`,
			`{"_id":"b","f":"x","l":2}`,
		)
	}
}

func TestPostStages(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":"$s","c":{"$count":{}},"sum":{"$sum":"$n"}}},{"$match":{"c":{"$gte":2}}},{"$sort":{"sum":-1}},{"$project":{"sum":1,"_id":0}}]}`

	eq(t, run(t, body, 0, abc...), `{"sum":4}`, `{"sum":2}`)

	body = `{"pipeline":[{"$group":{"_id":"$s","c":{"$count":{}}}},{"$sort":{"c":-1}},{"$skip":1},{"$limit":1},{"$project":{"c":0}}]}`

	eq(t, run(t, body, 0, abc...), `{"_id":"b"}`)
}

func TestMissingAndNullShareAGroup(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":"$x","c":{"$count":{}}}}]}`

	eq(t, run(t, body, 0, `{"x":null}`, `{}`, `{"x":1}`), `{"_id":null,"c":2}`, `{"_id":1,"c":1}`)
}

func TestMinMaxCrossTypeOrder(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":null,"lo":{"$min":"$v"},"hi":{"$max":"$v"}}}]}`

	eq(t, run(t, body, 0, `{"v":"s"}`, `{"v":7}`, `{"v":true}`, `{"v":null}`, `{}`, `{"v":[1]}`),
		`{"_id":null,"hi":true,"lo":7}`)
}

func TestSumIgnoresNonNumbers(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":null,"s":{"$sum":"$v"}}}]}`

	eq(t, run(t, body, 0, `{"v":1}`, `{"v":"2"}`, `{"v":[3]}`, `{"v":null}`, `{"v":4.5}`), `{"_id":null,"s":5.5}`)
}

func TestArrayValueIsOneKey(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":"$tags","c":{"$count":{}}}}]}`

	eq(t, run(t, body, 0, `{"tags":["a","b"]}`, `{"tags":["a","b"]}`, `{"tags":["a"]}`),
		`{"_id":["a","b"],"c":2}`, `{"_id":["a"],"c":1}`)
}

func TestDateTruncUnitsAndZone(t *testing.T) {
	cases := []struct{ unit, zone, in, want string }{
		{"minute", "", "2026-11-04T12:34:56.789Z", "2026-11-04T12:34:00Z"},
		{"hour", "", "2026-11-04T12:34:56Z", "2026-11-04T12:00:00Z"},
		{"day", "", "2026-11-04T23:59:59Z", "2026-11-04T00:00:00Z"},
		{"week", "", "2026-11-04T12:00:00Z", "2026-11-02T00:00:00Z"},
		{"week", "", "2026-11-08T12:00:00Z", "2026-11-02T00:00:00Z"},
		{"month", "", "2026-11-30T23:00:00Z", "2026-11-01T00:00:00Z"},
		{"day", "America/Sao_Paulo", "2026-11-04T01:00:00Z", "2026-11-03T03:00:00Z"},
		{"hour", "Asia/Kolkata", "2026-11-04T12:10:00Z", "2026-11-04T11:30:00Z"},
		{"day", "", "2026-11-04T12:00:00+03:00", "2026-11-04T00:00:00Z"},
		{"day", "", "2026-11-04", "2026-11-04T00:00:00Z"},
	}

	for _, c := range cases {
		spec := `{"date":"$d","unit":"` + c.unit + `"`

		if c.zone != "" {
			spec += `,"timezone":"` + c.zone + `"`
		}

		spec += `}`

		body := `{"pipeline":[{"$group":{"_id":{"$dateTrunc":` + spec + `}}}]}`
		in, _ := json.Marshal(c.in)

		eq(t, run(t, body, 0, `{"d":`+string(in)+`}`), `{"_id":"`+c.want+`"}`)
	}
}

func TestDateTruncNotATimeIsNull(t *testing.T) {
	body := `{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"$d","unit":"day"}}}}]}`

	eq(t, run(t, body, 0, `{"d":"nope"}`, `{"d":5}`, `{}`), `{"_id":null}`)
}

func TestGroupLimit(t *testing.T) {
	p, err := parse(t, `{"pipeline":[{"$group":{"_id":"$k","c":{"$count":{}}}}]}`)
	if err != nil {
		t.Fatal(err)
	}

	tbl := p.NewTable(agg.NewBudget(2000))
	var last error

	for i := 0; i < 1000 && last == nil; i++ {
		m := map[string]any{"k": float64(i)}
		last = tbl.Add(p.Values(m), query.Pos{Seq: uint64(i)})
	}

	wantCode(t, last, "group_limit_exceeded")
}

func TestPagination(t *testing.T) {
	p, err := parse(t, `{"pipeline":[{"$group":{"_id":"$n","c":{"$count":{}}}}],"limit":2,"count":true}`)
	if err != nil {
		t.Fatal(err)
	}

	tbl := p.NewTable(agg.NewBudget(1 << 20))

	for i := 1; i <= 5; i++ {
		_ = tbl.Add([]query.Value{query.FromJSON(float64(i), false)}, query.Pos{Seq: uint64(i)})
	}

	res, err := p.Finish(tbl.Rows())
	if err != nil || len(res.Rows) != 2 || res.Next == "" || res.Total != 5 {
		t.Fatalf("page 1: %+v %v", res, err)
	}

	var ids []string

	for _, r := range res.Rows {
		ids = append(ids, string(r))
	}

	next := `{"pipeline":[{"$group":{"_id":"$n","c":{"$count":{}}}}],"limit":2,"after":"` + res.Next + `"}`

	for next != "" {
		p2, err := parse(t, next)
		if err != nil {
			t.Fatal(err)
		}

		res, err = p2.Finish(tbl.Rows())
		if err != nil {
			t.Fatal(err)
		}

		for _, r := range res.Rows {
			ids = append(ids, string(r))
		}

		next = ""

		if res.Next != "" {
			next = `{"pipeline":[{"$group":{"_id":"$n","c":{"$count":{}}}}],"limit":2,"after":"` + res.Next + `"}`
		}
	}

	eq(t, ids, `{"_id":1,"c":1}`, `{"_id":2,"c":1}`, `{"_id":3,"c":1}`, `{"_id":4,"c":1}`, `{"_id":5,"c":1}`)
}

func TestCursorErrors(t *testing.T) {
	p, _ := parse(t, `{"pipeline":[{"$group":{"_id":null}}],"limit":1}`)
	tbl := p.NewTable(agg.NewBudget(1 << 20))
	res, _ := p.Finish(tbl.Rows())

	if res.Next != "" {
		t.Fatalf("a single row has no next: %q", res.Next)
	}

	bad, _ := parse(t, `{"pipeline":[{"$group":{"_id":null}}],"after":"!!!"}`)

	wantCode(t, bad.CheckCursor(), "cursor_invalid")

	two, _ := parse(t, `{"pipeline":[{"$group":{"_id":"$a"}}],"limit":1}`)
	tb := two.NewTable(agg.NewBudget(1 << 20))

	_ = tb.Add([]query.Value{query.FromJSON("x", false)}, query.Pos{})
	_ = tb.Add([]query.Value{query.FromJSON("y", false)}, query.Pos{})

	res, _ = two.Finish(tb.Rows())

	other, _ := parse(t, `{"pipeline":[{"$group":{"_id":"$b"}}],"limit":1,"after":"`+res.Next+`"}`)

	wantCode(t, other.CheckCursor(), "cursor_mismatch")
}

// The top n of a group stage followed by $sort and $limit gives the rows of
// the whole path (Rows, every stage), ties broken by the group key.
func TestTopOfSortAndLimitEqualsTheWholePath(t *testing.T) {
	var docs []string

	for i := range 600 {
		docs = append(docs, fmt.Sprintf(`{"u":"u%03d","n":%d}`, i%97, (i*7)%5))
	}

	for _, body := range []string{
		`{"pipeline":[{"$group":{"_id":"$u","t":{"$sum":"$n"}}},{"$sort":{"t":-1}},{"$limit":10}]}`,
		`{"pipeline":[{"$group":{"_id":"$u","t":{"$sum":"$n"}}},{"$sort":{"t":1}},{"$limit":5},{"$project":{"t":1}}]}`,
		`{"pipeline":[{"$group":{"_id":"$u","t":{"$max":"$n"}}},{"$sort":{"_id":-1}},{"$limit":3}],"limit":2}`,
		`{"pipeline":[{"$group":{"_id":"$u","t":{"$sum":"$n"}}},{"$sort":{"t":-1}},{"$limit":500}]}`,
	} {
		p, err := parse(t, body)
		if err != nil {
			t.Fatal(err)
		}

		table := p.NewTable(agg.NewBudget(1 << 30))

		for i, d := range docs {
			m, _ := query.Decode([]byte(d))

			if err := table.Add(p.Values(m), query.Pos{Seq: uint64(i + 1)}); err != nil {
				t.Fatal(err)
			}
		}

		whole, err := p.Finish(table.Rows())
		if err != nil {
			t.Fatal(err)
		}

		top, err := p.FinishTable(table)
		if err != nil {
			t.Fatal(err)
		}

		if fmt.Sprint(rowsText(whole), whole.Total, whole.Next) != fmt.Sprint(rowsText(top), top.Total, top.Next) {
			t.Fatalf("%s\n whole %v (total %d)\n top   %v (total %d)", body, rowsText(whole), whole.Total, rowsText(top), top.Total)
		}
	}
}

func rowsText(r agg.Result) []string {
	out := make([]string, len(r.Rows))

	for i, row := range r.Rows {
		out[i] = string(row)
	}

	return out
}
