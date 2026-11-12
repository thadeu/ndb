package query_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/query"
)

func decode(t *testing.T, s string) map[string]any {
	t.Helper()

	m, err := query.Decode([]byte(s))
	if err != nil {
		t.Fatal(err)
	}

	return m
}

func match(t *testing.T, where, doc string) bool {
	t.Helper()

	q, err := query.Parse([]byte(`{"query":`+where+`}`), query.Options{TimeField: "at"})
	if err != nil {
		t.Fatalf("%s: %v", where, err)
	}

	return q.Match(decode(t, doc))
}

func TestOperators(t *testing.T) {
	cases := []struct {
		name, where, doc string
		want             bool
	}{
		{"empty where", `{}`, `{}`, true},
		{"implicit eq", `{"a":1}`, `{"a":1}`, true},
		{"implicit eq 1.0", `{"a":1}`, `{"a":1.0}`, true},
		{"eq miss", `{"a":1}`, `{"a":2}`, false},
		{"eq string vs number", `{"a":1}`, `{"a":"1"}`, false},
		{"eq object", `{"a":{"x":1,"y":2}}`, `{"a":{"y":2,"x":1}}`, true},
		{"eq array whole", `{"a":[1,2]}`, `{"a":[1,2]}`, true},
		{"eq array order", `{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{"$eq", `{"a":{"$eq":"x"}}`, `{"a":"x"}`, true},
		{"$ne hit", `{"a":{"$ne":1}}`, `{"a":2}`, true},
		{"$ne same", `{"a":{"$ne":1}}`, `{"a":1}`, false},
		{"$ne missing", `{"a":{"$ne":1}}`, `{}`, true},
		{"$gt", `{"a":{"$gt":1}}`, `{"a":2}`, true},
		{"$gt equal", `{"a":{"$gt":2}}`, `{"a":2}`, false},
		{"$gte equal", `{"a":{"$gte":2}}`, `{"a":2}`, true},
		{"$lt", `{"a":{"$lt":2}}`, `{"a":1}`, true},
		{"$lte equal", `{"a":{"$lte":2}}`, `{"a":2}`, true},
		{"range", `{"a":{"$gt":1,"$lt":5}}`, `{"a":3}`, true},
		{"range out", `{"a":{"$gt":1,"$lt":5}}`, `{"a":7}`, false},
		{"$gt string", `{"a":{"$gt":"b"}}`, `{"a":"c"}`, true},
		{"$gt brackets number vs string", `{"a":{"$gt":1}}`, `{"a":"z"}`, false},
		{"$lt brackets string vs number", `{"a":{"$lt":"z"}}`, `{"a":1}`, false},
		{"$nor hit", `{"$nor":[{"a":1},{"a":2}]}`, `{"a":3}`, true},
		{"$nor miss", `{"$nor":[{"a":1},{"a":2}]}`, `{"a":2}`, false},
		{"$gt bool", `{"a":{"$gt":false}}`, `{"a":true}`, true},
		{"$in", `{"a":{"$in":[1,2,3]}}`, `{"a":2}`, true},
		{"$in miss", `{"a":{"$in":[1,2,3]}}`, `{"a":9}`, false},
		{"$in empty", `{"a":{"$in":[]}}`, `{"a":1}`, false},
		{"$in mixed types", `{"a":{"$in":["x",1]}}`, `{"a":1}`, true},
		{"$nin", `{"a":{"$nin":[1,2]}}`, `{"a":3}`, true},
		{"$nin hit", `{"a":{"$nin":[1,2]}}`, `{"a":2}`, false},
		{"$nin missing", `{"a":{"$nin":[1,2]}}`, `{}`, true},
		{"$exists true", `{"a":{"$exists":true}}`, `{"a":null}`, true},
		{"$exists true missing", `{"a":{"$exists":true}}`, `{}`, false},
		{"$exists false", `{"a":{"$exists":false}}`, `{}`, true},
		{"$exists false null", `{"a":{"$exists":false}}`, `{"a":null}`, false},
		{"$and", `{"$and":[{"a":1},{"b":2}]}`, `{"a":1,"b":2}`, true},
		{"$and miss", `{"$and":[{"a":1},{"b":2}]}`, `{"a":1,"b":3}`, false},
		{"$or", `{"$or":[{"a":1},{"b":2}]}`, `{"a":9,"b":2}`, true},
		{"$or miss", `{"$or":[{"a":1},{"b":2}]}`, `{"a":9,"b":9}`, false},
		{"$not field", `{"a":{"$not":{"$gt":5}}}`, `{"a":3}`, true},
		{"$not field hit", `{"a":{"$not":{"$gt":5}}}`, `{"a":9}`, false},
		{"$not field missing", `{"a":{"$not":{"$gt":5}}}`, `{}`, true},
		{"$not top", `{"$not":{"a":1}}`, `{"a":2}`, true},
		{"nested and implicit", `{"a":1,"$or":[{"b":1},{"c":1}]}`, `{"a":1,"c":1}`, true},
		{"dot path", `{"address.city":"Rio"}`, `{"address":{"city":"Rio"}}`, true},
		{"dot path miss", `{"address.city":"Rio"}`, `{"address":{"city":"SP"}}`, false},
		{"dot path through scalar", `{"a.b":1}`, `{"a":5}`, false},
		{"dot path exists false", `{"a.b":{"$exists":false}}`, `{"a":5}`, true},
		{"dot through array", `{"items.sku":"x"}`, `{"items":[{"sku":"y"},{"sku":"x"}]}`, true},
		{"literal dollar value", `{"a":"$gt"}`, `{"a":"$gt"}`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := match(t, c.where, c.doc); got != c.want {
				t.Fatalf("match(%s, %s) = %v", c.where, c.doc, got)
			}
		})
	}
}

func TestMissingVersusNull(t *testing.T) {
	cases := []struct {
		name, where, doc string
		want             bool
	}{
		{"eq null hits null", `{"a":null}`, `{"a":null}`, true},
		{"eq null skips missing", `{"a":null}`, `{}`, false},
		{"ne null hits missing", `{"a":{"$ne":null}}`, `{}`, true},
		{"ne null skips null", `{"a":{"$ne":null}}`, `{"a":null}`, false},
		{"gt null no match", `{"a":{"$gt":null}}`, `{"a":1}`, false},
		{"in null", `{"a":{"$in":[null]}}`, `{"a":null}`, true},
		{"in null skips missing", `{"a":{"$in":[null]}}`, `{}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := match(t, c.where, c.doc); got != c.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestArraysMatchAnyElement(t *testing.T) {
	cases := []struct {
		name, where, doc string
		want             bool
	}{
		{"eq element", `{"tags":"b"}`, `{"tags":["a","b"]}`, true},
		{"eq no element", `{"tags":"z"}`, `{"tags":["a","b"]}`, false},
		{"gt element", `{"n":{"$gt":5}}`, `{"n":[1,9]}`, true},
		{"gt none", `{"n":{"$gt":10}}`, `{"n":[1,9]}`, false},
		{"range same element", `{"n":{"$gt":2,"$lt":4}}`, `{"n":[1,3]}`, true},
		{"in element", `{"tags":{"$in":["x","b"]}}`, `{"tags":["a","b"]}`, true},
		{"ne element present", `{"tags":{"$ne":"a"}}`, `{"tags":["a","b"]}`, false},
		{"nin element present", `{"tags":{"$nin":["a"]}}`, `{"tags":["a","b"]}`, false},
		{"empty array eq", `{"tags":"a"}`, `{"tags":[]}`, false},
		{"empty array exists", `{"tags":{"$exists":true}}`, `{"tags":[]}`, true},
		{"mixed array", `{"v":1}`, `{"v":["x",1]}`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := match(t, c.where, c.doc); got != c.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestTimeField(t *testing.T) {
	cases := []struct {
		name, where, doc string
		want             bool
	}{
		{"offsets compare as time", `{"at":{"$lt":"2026-11-04T12:00:00Z"}}`, `{"at":"2026-11-04T08:00:00-03:00"}`, true},
		{"offsets compare as time miss", `{"at":{"$lt":"2026-11-04T12:00:00Z"}}`, `{"at":"2026-11-04T10:00:00+03:00"}`, true},
		{"same instant eq", `{"at":"2026-11-04T12:00:00Z"}`, `{"at":"2026-11-04T09:00:00-03:00"}`, true},
		{"string compare would differ", `{"at":{"$gt":"2026-11-04T12:00:00Z"}}`, `{"at":"2026-11-04T10:00:00-03:00"}`, true},
		{"other field compares as time too", `{"x":{"$gt":"2026-11-04T12:00:00Z"}}`, `{"x":"2026-11-04T10:00:00-03:00"}`, true},
		{"unparsable stays string", `{"at":"nope"}`, `{"at":"nope"}`, true},
		{"unparsable not in time class", `{"at":{"$lt":"2026-11-04T12:00:00Z"}}`, `{"at":"nope"}`, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := match(t, c.where, c.doc); got != c.want {
				t.Fatalf("got %v", got)
			}
		})
	}
}

func TestCompareTypeOrder(t *testing.T) {
	ordered := []string{`"MISSING"`, `null`, `-5`, `0`, `3.5`, `""`, `"a"`, `"b"`, `{"a":1}`, `{}`, `[1]`, `[]`, `false`, `true`}
	vals := make([]query.Value, len(ordered))

	for i, s := range ordered {
		if s == `"MISSING"` {
			vals[i] = query.Value{}

			continue
		}

		var v any

		dec := json.NewDecoder(strings.NewReader(s))
		dec.UseNumber()

		if err := dec.Decode(&v); err != nil {
			t.Fatal(err)
		}

		vals[i] = query.FromJSON(v, false)
	}

	for i := range vals {
		for j := range vals {
			want := 0

			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}

			if got := query.Compare(vals[i], vals[j]); got != want {
				t.Fatalf("Compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
}

func TestIndexable(t *testing.T) {
	q, err := query.Parse([]byte(`{"query":{"a":1,"b":{"$in":[1,2]},"c":{"$gt":3,"$ne":4},"d":[1],"$or":[{"e":1}],"$and":[{"f":"x"}]}}`), query.Options{})
	if err != nil {
		t.Fatal(err)
	}

	got := map[string]string{}

	for _, c := range q.Indexable() {
		got[c.Path+c.Op] = ""
	}

	for _, want := range []string{"aeq", "bin", "cgt", "feq"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing %s in %v", want, got)
		}
	}

	if len(got) != 4 {
		t.Fatalf("got %v", got)
	}
}
