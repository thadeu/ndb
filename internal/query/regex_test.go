package query_test

import (
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/query"
)

func TestRegexMatch(t *testing.T) {
	cases := []struct {
		name, where, doc string
		want             bool
	}{
		{"shorthand hit", `{"a":{"$regex":"ab+c"}}`, `{"a":"xabbcx"}`, true},
		{"shorthand miss", `{"a":{"$regex":"^ab"}}`, `{"a":"xab"}`, false},
		{"case sensitive", `{"a":{"$regex":"abc"}}`, `{"a":"ABC"}`, false},
		{"option i", `{"a":{"$regex":"abc","$options":"i"}}`, `{"a":"xABCx"}`, true},
		{"option m", `{"a":{"$regex":"^b$","$options":"m"}}`, `{"a":"a\nb\nc"}`, true},
		{"no option m", `{"a":{"$regex":"^b$"}}`, `{"a":"a\nb\nc"}`, false},
		{"option s", `{"a":{"$regex":"a.b","$options":"s"}}`, `{"a":"a\nb"}`, true},
		{"no option s", `{"a":{"$regex":"a.b"}}`, `{"a":"a\nb"}`, false},
		{"option x", `{"a":{"$regex":"a b # note\n c","$options":"x"}}`, `{"a":"abc"}`, true},
		{"option x keeps class space", `{"a":{"$regex":"a[ ]b","$options":"x"}}`, `{"a":"a b"}`, true},
		{"option x keeps escaped space", `{"a":{"$regex":"a\\ b","$options":"x"}}`, `{"a":"a b"}`, true},
		{"option x keeps escaped hash", `{"a":{"$regex":"a\\#b","$options":"x"}}`, `{"a":"a#b"}`, true},
		{"options combined", `{"a":{"$regex":"^A.B$","$options":"is"}}`, `{"a":"a\nb"}`, true},
		{"empty options", `{"a":{"$regex":"a","$options":""}}`, `{"a":"a"}`, true},
		{"array any element", `{"t":{"$regex":"^b"}}`, `{"t":["a","bb"]}`, true},
		{"array no element", `{"t":{"$regex":"^c"}}`, `{"t":["a","bb"]}`, false},
		{"dot path", `{"m.city":{"$regex":"^R"}}`, `{"m":{"city":"Rio"}}`, true},
		{"array of objects", `{"m.city":{"$regex":"^S"}}`, `{"m":[{"city":"Rio"},{"city":"SP"}]}`, true},
		{"number ignored", `{"a":{"$regex":"1"}}`, `{"a":123}`, false},
		{"null ignored", `{"a":{"$regex":".*"}}`, `{"a":null}`, false},
		{"missing ignored", `{"a":{"$regex":".*"}}`, `{}`, false},
		{"object ignored", `{"a":{"$regex":".*"}}`, `{"a":{"x":"y"}}`, false},
		{"bool ignored", `{"a":{"$regex":"true"}}`, `{"a":true}`, false},
		{"not regex hit", `{"a":{"$not":{"$regex":"^x"}}}`, `{"a":"yes"}`, true},
		{"not regex miss", `{"a":{"$not":{"$regex":"^x"}}}`, `{"a":"xes"}`, false},
		{"not regex missing field", `{"a":{"$not":{"$regex":"^x"}}}`, `{}`, true},
		{"not regex with options", `{"a":{"$not":{"$regex":"^x","$options":"i"}}}`, `{"a":"XES"}`, false},
		{"top not", `{"$not":{"a":{"$regex":"^x"}}}`, `{"a":"yes"}`, true},
		{"or", `{"$or":[{"a":{"$regex":"^x"}},{"b":{"$regex":"^y"}}]}`, `{"b":"yo"}`, true},
		{"and", `{"$and":[{"a":{"$regex":"^x"}},{"b":{"$regex":"^y"}}]}`, `{"a":"xo","b":"no"}`, false},
		{"with other op", `{"a":{"$regex":"^x","$ne":"xx"}}`, `{"a":"xy"}`, true},
		{"time field string", `{"at":{"$regex":"^2026-01"}}`, `{"at":"2026-01-02T03:04:05Z"}`, true},
		{"unicode", `{"a":{"$regex":"^é.o$"}}`, `{"a":"éxo"}`, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := match(t, c.where, c.doc); got != c.want {
				t.Fatalf("%s on %s = %v, want %v", c.where, c.doc, got, c.want)
			}
		})
	}
}

func TestRegexInvalid(t *testing.T) {
	long := strings.Repeat("a", 1025)

	bodies := []string{
		`{"query":{"a":{"$regex":"("}}}`,
		`{"query":{"a":{"$regex":"(?=x)"}}}`,
		`{"query":{"a":{"$regex":"(a)\\1"}}}`,
		`{"query":{"a":{"$regex":5}}}`,
		`{"query":{"a":{"$regex":null}}}`,
		`{"query":{"a":{"$regex":"a","$options":"q"}}}`,
		`{"query":{"a":{"$regex":"a","$options":"g"}}}`,
		`{"query":{"a":{"$regex":"a","$options":5}}}`,
		`{"query":{"a":{"$options":"i"}}}`,
		`{"query":{"a":{"$not":{"$options":"i"}}}}`,
		`{"query":{"a":{"$regex":"` + long + `"}}}`,
		`{"query":{"a":{"$regex":"a","$options":"x"},"b":{"$regex":"[a"}}}`,
	}

	for _, b := range bodies {
		_, err := query.Parse([]byte(b), query.Options{})
		if err == nil {
			t.Fatalf("%.80s must be invalid_query", b)
		}

		if qe, ok := err.(*query.Error); !ok || qe.Code() != "invalid_query" {
			t.Fatalf("%.80s: error %v", b, err)
		}
	}
}

func TestRegexInvalidCarriesRE2Message(t *testing.T) {
	_, err := query.Parse([]byte(`{"query":{"a":{"$regex":"(ab"}}}`), query.Options{})
	if err == nil || !strings.Contains(err.Error(), "missing closing )") {
		t.Fatalf("error %v", err)
	}
}

func TestRegexLimitBoundary(t *testing.T) {
	ok := strings.Repeat("a", 1024)

	if _, err := query.Parse([]byte(`{"query":{"a":{"$regex":"`+ok+`"}}}`), query.Options{}); err != nil {
		t.Fatal(err)
	}
}

func TestRegexIndexableConds(t *testing.T) {
	cases := []struct {
		where string
		want  string
	}{
		{`{"a":{"$regex":"^abc"}}`, "gte:abc lt:abd"},
		{`{"a":{"$regex":"^abc.*x"}}`, "gte:abc lt:abd"},
		{`{"a":{"$regex":"^abc","$options":"s"}}`, "gte:abc lt:abd"},
		{`{"a":{"$regex":"^abc","$options":"i"}}`, ""},
		{`{"a":{"$regex":"^abc","$options":"m"}}`, ""},
		{`{"a":{"$regex":"abc"}}`, ""},
		{`{"a":{"$regex":"^(abc|abd)"}}`, ""},
		{`{"a":{"$regex":"^a|^b"}}`, ""},
		{`{"a":{"$regex":"^"}}`, ""},
		{`{"a":{"$regex":"^ab*"}}`, "gte:a lt:b"},
		{`{"a":{"$regex":"^ab?c"}}`, "gte:a lt:b"},
		{`{"a":{"$not":{"$regex":"^abc"}}}`, ""},
		{`{"$or":[{"a":{"$regex":"^abc"}}]}`, ""},
		{`{"at":{"$regex":"^2026"}}`, ""},
		{`{"a":{"$regex":"^x","$options":"x"}}`, "gte:x lt:y"},
	}

	for _, c := range cases {
		q, err := query.Parse([]byte(`{"query":`+c.where+`}`), query.Options{TimeField: "at"})
		if err != nil {
			t.Fatalf("%s: %v", c.where, err)
		}

		var got []string

		for _, cd := range q.Indexable() {
			got = append(got, cd.Op+":"+cd.Vals[0].Str)
		}

		if strings.Join(got, " ") != c.want {
			t.Fatalf("%s: conds %q, want %q", c.where, got, c.want)
		}
	}
}

func TestRegexPrefixHighByte(t *testing.T) {
	q, err := query.Parse([]byte("{\"query\":{\"a\":{\"$regex\":\"^\\u00ff\"}}}"), query.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, cd := range q.Indexable() {
		if cd.Op == "lt" && cd.Vals[0].Str <= "ÿ" {
			t.Fatalf("bad upper bound %q", cd.Vals[0].Str)
		}
	}
}

func TestRegexColsAgreeWithMatch(t *testing.T) {
	q, err := query.Parse([]byte(`{"query":{"a":{"$regex":"^b","$options":"i"},"n":{"$regex":"1"}}}`), query.Options{})
	if err != nil {
		t.Fatal(err)
	}

	str := func(s string) query.Value { return query.FromJSON(s, false) }
	num := query.FromJSON(float64(1), false)

	cands := map[string][]query.Value{"a": {str("B1")}, "n": {num}}

	got := make([][]query.Value, len(q.Paths()))

	for i, p := range q.Paths() {
		got[i] = cands[p]
	}

	if q.MatchCols(got) {
		t.Fatal("a number never matches a regex")
	}

	cands["n"] = []query.Value{str("a1")}

	for i, p := range q.Paths() {
		got[i] = cands[p]
	}

	if !q.MatchCols(got) {
		t.Fatal("string columns must match")
	}
}
