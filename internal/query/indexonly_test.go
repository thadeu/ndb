package query

import (
	"encoding/json"
	"testing"
)

func TestIndexOnly(t *testing.T) {
	cases := []struct {
		where string
		path  string
		ok    bool
	}{
		{`{"status":"x"}`, "status", true},
		{`{"status":{"$in":["a","b"]}}`, "status", true},
		{`{"day":{"$lt":10}}`, "day", true},
		{`{"day":{"$gte":10,"$lt":12}}`, "day", true},
		{`{"$and":[{"day":{"$gte":10}},{"day":{"$lt":12}}]}`, "day", true},
		{`{"flag":true}`, "flag", true},

		{`{"status":"x","city":"y"}`, "", false},
		{`{"status":null}`, "", false},
		{`{"status":{"$in":["a",null]}}`, "", false},
		{`{"status":"x","$or":[{"a":1}]}`, "", false},
		{`{"status":{"$ne":"x"}}`, "", false},
		{`{"status":{"$regex":"^x"}}`, "", false},
		{`{"day":{"$gte":10,"$lt":"z"}}`, "", false},
		{`{"day":{"$gt":1,"$gte":2}}`, "", false},
		{`{"day":5,"$and":[{"day":{"$lt":9}}]}`, "", false},
		{`{"tags":["a","b"]}`, "", false},
	}

	for _, tc := range cases {
		var m map[string]any

		if err := json.Unmarshal([]byte(tc.where), &m); err != nil {
			t.Fatal(err)
		}

		q, err := ParseWhere(m, Options{})
		if err != nil {
			t.Fatalf("%s: %v", tc.where, err)
		}

		path, ok := q.IndexOnly()

		if ok != tc.ok || path != tc.path {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.where, path, ok, tc.path, tc.ok)
		}
	}
}
