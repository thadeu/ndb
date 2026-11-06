package query_test

import (
	"testing"

	"github.com/thadeu/ndb/internal/query"
)

func TestParseInvalid(t *testing.T) {
	cases := map[string]string{
		"not json":            `{`,
		"unknown key":         `{"wher": {}}`,
		"where not object":    `{"where": 5}`,
		"unknown operator":    `{"where": {"a": {"$foo": "x"}}}`,
		"mixed operator":      `{"where": {"a": {"$gt": 1, "b": 2}}}`,
		"in not array":        `{"where": {"a": {"$in": 1}}}`,
		"exists not bool":     `{"where": {"a": {"$exists": 1}}}`,
		"and empty":           `{"where": {"$and": []}}`,
		"and not objects":     `{"where": {"$or": [1]}}`,
		"nor empty":           `{"where": {"$nor": []}}`,
		"unknown top op":      `{"where": {"$elemMatch": {}}}`,
		"not not object":      `{"where": {"a": {"$not": 5}}}`,
		"sort bad direction":  `{"sort": {"a": 2}}`,
		"sort not object":     `{"sort": ["a"]}`,
		"negative limit":      `{"limit": -1}`,
		"negative skip":       `{"skip": -1}`,
		"limit not number":    `{"limit": "5"}`,
		"fields not strings":  `{"fields": [1]}`,
		"count not bool":      `{"count": 1}`,
		"empty path":          `{"where": {"a..b": 1}}`,
		"gt with array":       `{"where": {"a": {"$gt": [1]}}}`,
		"after not string":    `{"after": 5}`,
		"snapshot not bool":   `{"snapshot": "yes"}`,
		"eq operand missing":  `{"where": {"a": {"$eq": }}}`,
		"sort empty path":     `{"sort": {"": 1}}`,
		"fields empty string": `{"fields": [""]}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := query.Parse([]byte(body), query.Options{})
			if err == nil {
				t.Fatal("want error")
			}

			if err.(*query.Error).Code() != "invalid_query" {
				t.Fatalf("code = %s", err.(*query.Error).Code())
			}
		})
	}
}

func TestParseFields(t *testing.T) {
	q, err := query.Parse([]byte(`{"where":{"a":1},"fields":["a","b.c"],"sort":{"z":-1,"a":1},"limit":5,"skip":2,"after":"x","count":true,"snapshot":true}`), query.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if q.Limit != 5 || q.Skip != 2 || q.After != "x" || !q.Count || !q.Snapshot {
		t.Fatalf("%+v", q)
	}

	if len(q.Fields) != 2 || len(q.Sort) != 2 || q.Sort[0].Path != "z" || !q.Sort[0].Desc || q.Sort[1].Path != "a" || q.Sort[1].Desc {
		t.Fatalf("%+v", q)
	}
}

func TestParseEmptyBody(t *testing.T) {
	if _, err := query.Parse(nil, query.Options{}); err != nil {
		t.Fatal(err)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`{}`, `{"where":{"a":{"$in":[1,2]}}}`, `{"where":{"$or":[{"a":1},{"b":{"$not":{"$gt":2}}}]},"sort":{"a":1}}`,
		`{"limit":3,"after":"abc"}`, `{"where":{"a.b":{"$exists":false}}}`,
		`{"where":{"a":{"$regex":"^ab+","$options":"i"}}}`, `{"where":{"a":{"$not":{"$regex":"[a-z]+","$options":"msx"}}}}`,
		`{"where":{"a":{"$regex":"(?P<n>x)|\\d{2,}"}}}`, `{"where":{"a":{"$options":"i"}}}`,
	} {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		q, err := query.Parse(b, query.Options{TimeField: "t"})
		if err != nil {
			return
		}

		q.Match(map[string]any{"a": []any{1.0, "x"}, "t": "2026-01-01T00:00:00Z"})
		q.Hash()
		q.Indexable()
	})
}
