package agg_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/thadeu/ndb/internal/agg"
	"github.com/thadeu/ndb/internal/query"
)

func TestParseFindLeavesAGroupToParse(t *testing.T) {
	for _, body := range []string{
		`{"pipeline":[{"$match":{}},{"$group":{"_id":null}}]}`,
		`not json`,
		`{"pipeline":[]}`,
	} {
		if _, ok, err := agg.ParseFind([]byte(body)); ok || err != nil {
			t.Fatalf("%s: ok = %v, err = %v; want not a find", body, ok, err)
		}
	}
}

func TestParseFindErrors(t *testing.T) {
	cases := map[string]string{
		"match after limit":   `{"pipeline":[{"$limit":2},{"$match":{}}]}`,
		"sort after skip":     `{"pipeline":[{"$skip":2},{"$sort":{"a":1}}]}`,
		"match after project": `{"pipeline":[{"$project":{"a":1}},{"$match":{}}]}`,
		"two sorts":           `{"pipeline":[{"$sort":{"a":1}},{"$sort":{"b":1}}]}`,
		"two projects":        `{"pipeline":[{"$project":{"a":1}},{"$project":{"b":1}}]}`,
		"limit zero":          `{"pipeline":[{"$limit":0}]}`,
		"skip negative":       `{"pipeline":[{"$skip":-1}]}`,
		"having":              `{"pipeline":[{"$having":{"n":1}}]}`,
		"bad match":           `{"pipeline":[{"$match":{"a":{"$nope":1}}}]}`,
		"bad sort":            `{"pipeline":[{"$sort":{"a":2}}]}`,
		"unknown stage":       `{"pipeline":[{"$unwind":"$a"}]}`,
		"unknown key":         `{"pipeline":[{"$match":{}}],"nope":1}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, ok, err := agg.ParseFind([]byte(body))

			var qe *query.Error

			if !ok || !errors.As(err, &qe) || qe.Code() != "invalid_pipeline" {
				t.Fatalf("ok = %v, err = %v; want invalid_pipeline", ok, err)
			}
		})
	}
}

// The $skip and $limit stages fold into one window, in their order.
func TestParseFindWindow(t *testing.T) {
	cases := map[string]struct {
		pipeline    string
		skip, limit int
	}{
		"skip then limit": {`[{"$skip":2},{"$limit":5}]`, 2, 5},
		"limit then skip": {`[{"$limit":5},{"$skip":2}]`, 2, 3},
		"two limits":      {`[{"$limit":5},{"$limit":3}]`, 0, 3},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, ok, err := agg.ParseFind([]byte(`{"pipeline":` + c.pipeline + `}`))
			if !ok || err != nil {
				t.Fatalf("ok = %v, err = %v", ok, err)
			}

			pg, err := f.Page()
			if err != nil {
				t.Fatal(err)
			}

			var body struct {
				Skip  int `json:"skip"`
				Limit int `json:"limit"`
			}

			if err := json.Unmarshal(pg.Body, &body); err != nil {
				t.Fatal(err)
			}

			if body.Skip != c.skip || body.Limit != c.limit {
				t.Fatalf("skip, limit = %d, %d; want %d, %d", body.Skip, body.Limit, c.skip, c.limit)
			}
		})
	}
}

// A window with no row left still runs the find, for count and explain,
// but gives no row and no cursor.
func TestParseFindSkipPastTheLimit(t *testing.T) {
	f, _, err := agg.ParseFind([]byte(`{"pipeline":[{"$limit":2},{"$skip":4}],"count":true}`))
	if err != nil {
		t.Fatal(err)
	}

	pg, _ := f.Page()

	total := 20

	rows, next, got, err := pg.Finish([]json.RawMessage{[]byte(`{"_id":"x"}`)}, "more", &total)
	if err != nil {
		t.Fatal(err)
	}

	if len(rows) != 0 || next != "" || *got != 0 {
		t.Fatalf("rows = %d, next = %q, total = %d; want none", len(rows), next, *got)
	}
}

func TestParseFindProject(t *testing.T) {
	f, _, err := agg.ParseFind([]byte(`{"pipeline":[{"$project":{"a":1,"_id":0}}]}`))
	if err != nil {
		t.Fatal(err)
	}

	pg, _ := f.Page()

	if got := string(pg.Body); got != `{"count":false,"explain":false,"fields":["a"]}` {
		t.Fatalf("body = %s", got)
	}

	f, _, _ = agg.ParseFind([]byte(`{"pipeline":[{"$project":{"b":0}}]}`))
	pg, _ = f.Page()

	rows, _, _, err := pg.Finish([]json.RawMessage{[]byte(`{"_id":"x","a":1,"b":2}`)}, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := string(rows[0]); got != `{"_id":"x","a":1}` {
		t.Fatalf("row = %s", got)
	}
}
