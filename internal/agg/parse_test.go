package agg_test

import (
	"errors"
	"testing"

	"github.com/thadeu/ndb/internal/agg"
)

func parse(t *testing.T, body string) (*agg.Pipeline, error) {
	t.Helper()

	return agg.Parse([]byte(body), agg.Options{TimeField: "ts"})
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()

	var ce interface{ Code() string }

	if !errors.As(err, &ce) || ce.Code() != code {
		t.Fatalf("want code %q, got %v", code, err)
	}
}

func TestParseValid(t *testing.T) {
	bodies := []string{
		`{"pipeline":[{"$group":{"_id":"$status","n":{"$count":{}}}}]}`,
		`{"pipeline":[{"$match":{"n":{"$gt":1}}},{"$group":{"_id":null,"s":{"$sum":"$n"},"a":{"$avg":"$n"}}}]}`,
		`{"pipeline":[{"$group":{"_id":{"a":"$a","h":{"$dateTrunc":{"date":"$ts","unit":"hour"}}},"lo":{"$min":"$n"},"hi":{"$max":"$n"}}},{"$sort":{"_id.h":1}},{"$limit":10},{"$skip":1},{"$project":{"hi":1}}]}`,
		`{"pipeline":[{"$sort":{"ts":1}},{"$group":{"_id":"$a","f":{"$first":"$n"},"l":{"$last":"$n"}}}],"limit":5,"skip":1,"count":true,"explain":true}`,
		`{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"day","timezone":"America/Sao_Paulo"}},"n":{"$sum":1}}}]}`,
	}

	for _, b := range bodies {
		if _, err := parse(t, b); err != nil {
			t.Errorf("%s: %v", b, err)
		}
	}
}

func TestParseInvalid(t *testing.T) {
	bodies := map[string]string{
		"empty body":          ``,
		"not an object":       `[]`,
		"no pipeline":         `{}`,
		"empty pipeline":      `{"pipeline":[]}`,
		"no group":            `{"pipeline":[{"$match":{}}]}`,
		"two groups":          `{"pipeline":[{"$group":{"_id":null}},{"$group":{"_id":null}}]}`,
		"unknown stage":       `{"pipeline":[{"$lookup":{}},{"$group":{"_id":null}}]}`,
		"stage with 2 keys":   `{"pipeline":[{"$group":{"_id":null},"$limit":1}]}`,
		"stage not object":    `{"pipeline":[1]}`,
		"limit before group":  `{"pipeline":[{"$limit":1},{"$group":{"_id":null}}]}`,
		"project before":      `{"pipeline":[{"$project":{"a":1}},{"$group":{"_id":null}}]}`,
		"two sorts before":    `{"pipeline":[{"$sort":{"a":1}},{"$sort":{"b":1}},{"$group":{"_id":null}}]}`,
		"no _id":              `{"pipeline":[{"$group":{"n":{"$count":{}}}}]}`,
		"unknown acc":         `{"pipeline":[{"$group":{"_id":null,"n":{"$median":"$a"}}}]}`,
		"acc not object":      `{"pipeline":[{"$group":{"_id":null,"n":"$a"}}]}`,
		"acc with 2 ops":      `{"pipeline":[{"$group":{"_id":null,"n":{"$sum":"$a","$avg":"$a"}}}]}`,
		"count with arg":      `{"pipeline":[{"$group":{"_id":null,"n":{"$count":"$a"}}}]}`,
		"acc name has dot":    `{"pipeline":[{"$group":{"_id":null,"a.b":{"$sum":1}}}]}`,
		"acc name has dollar": `{"pipeline":[{"$group":{"_id":null,"$a":{"$sum":1}}}]}`,
		"empty path":          `{"pipeline":[{"$group":{"_id":"$"}}]}`,
		"bad path":            `{"pipeline":[{"$group":{"_id":"$a..b"}}]}`,
		"duplicate key field": `{"pipeline":[{"$group":{"_id":{"a":"$a","a":"$b"}}}]}`,
		"empty composite":     `{"pipeline":[{"$group":{"_id":{}}}]}`,
		"unit missing":        `{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"$ts"}}}}]}`,
		"unit unknown":        `{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"year"}}}}]}`,
		"timezone unknown":    `{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"day","timezone":"Mars/Base"}}}}]}`,
		"date not a path":     `{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"x","unit":"day"}}}}]}`,
		"dateTrunc extra key": `{"pipeline":[{"$group":{"_id":{"$dateTrunc":{"date":"$ts","unit":"day","bin":2}}}}]}`,
		"unknown expr op":     `{"pipeline":[{"$group":{"_id":{"$concat":["$a"]}}}]}`,
		"bad match":           `{"pipeline":[{"$match":{"a":{"$bogus":1}}},{"$group":{"_id":null}}]}`,
		"bad sort dir":        `{"pipeline":[{"$sort":{"a":2}},{"$group":{"_id":null}}]}`,
		"bad limit stage":     `{"pipeline":[{"$group":{"_id":null}},{"$limit":0}]}`,
		"bad skip stage":      `{"pipeline":[{"$group":{"_id":null}},{"$skip":-1}]}`,
		"bad project":         `{"pipeline":[{"$group":{"_id":null}},{"$project":{"a":1,"b":0}}]}`,
		"empty project":       `{"pipeline":[{"$group":{"_id":null}},{"$project":{}}]}`,
		"negative limit":      `{"pipeline":[{"$group":{"_id":null}}],"limit":-1}`,
		"unknown top key":     `{"pipeline":[{"$group":{"_id":null}}],"where":{}}`,
		"after not string":    `{"pipeline":[{"$group":{"_id":null}}],"after":1}`,
	}

	for name, b := range bodies {
		t.Run(name, func(t *testing.T) {
			_, err := parse(t, b)

			wantCode(t, err, "invalid_pipeline")
		})
	}
}
