package query_test

import (
	"slices"
	"testing"

	"github.com/thadeu/ndb/internal/query"
)

func TestParseExplainFlag(t *testing.T) {
	q, err := query.Parse([]byte(`{"where":{"a":1},"explain":true}`), query.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if !q.Explain {
		t.Fatal("explain: true must set Explain")
	}

	q, err = query.Parse([]byte(`{}`), query.Options{})
	if err != nil || q.Explain {
		t.Fatalf("no explain key: Explain %v err %v", q.Explain, err)
	}

	if _, err := query.Parse([]byte(`{"explain":"yes"}`), query.Options{}); err == nil {
		t.Fatal("explain must be a boolean")
	}
}

func TestPathsAreTheWhereFields(t *testing.T) {
	q, err := query.Parse([]byte(`{"where":{"status":"x","n":{"$gt":1},"deep.a":2,"$or":[{"status":"y"}]}}`), query.Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := q.Paths()
	slices.Sort(got)

	if !slices.Equal(got, []string{"deep.a", "n", "status"}) {
		t.Fatalf("Paths() = %v", got)
	}
}

// TestMatchColsAgreesWithMatch checks the column matcher on the same values
// as the document matcher, for each operator kind.
func TestMatchColsAgreesWithMatch(t *testing.T) {
	docs := []string{
		`{"status":"closed","n":3,"tags":["a","b"]}`,
		`{"status":"open","n":10,"tags":["c"]}`,
		`{"status":"closed","n":7}`,
	}

	bodies := []string{
		`{"where":{"status":"closed"}}`,
		`{"where":{"n":{"$gte":5,"$lt":10}}}`,
		`{"where":{"status":{"$in":["open","x"]},"n":{"$ne":10}}}`,
		`{"where":{"$or":[{"status":"open"},{"n":3}]}}`,
		`{"where":{"$not":{"status":"closed"}}}`,
		`{"where":{"tags":"b"}}`,
	}

	for _, body := range bodies {
		q, err := query.Parse([]byte(body), query.Options{})
		if err != nil {
			t.Fatal(err)
		}

		for _, d := range docs {
			m, err := query.Decode([]byte(d))
			if err != nil {
				t.Fatal(err)
			}

			cands := make([][]query.Value, len(q.Paths()))

			for i, p := range q.Paths() {
				cands[i] = candidatesOf(m, p)
			}

			if got, want := q.MatchCols(cands), q.Match(m); got != want {
				t.Fatalf("%s on %s: MatchCols %v, Match %v", body, d, got, want)
			}
		}
	}
}

// candidatesOf is what a column holds for path: the elements of an array
// plus the whole array, as the document matcher sees them.
func candidatesOf(m map[string]any, path string) []query.Value {
	out := query.Elements(m, path, false)

	if arr, ok := m[path].([]any); ok {
		out = append(out, query.FromJSON(arr, false))
	}

	return out
}
