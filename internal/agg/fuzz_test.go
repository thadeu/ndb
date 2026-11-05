package agg_test

import (
	"testing"

	"github.com/thadeu/ndb/internal/agg"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte(`{"pipeline":[{"$group":{"_id":"$a","n":{"$count":{}}}}]}`))
	f.Add([]byte(`{"pipeline":[{"$match":{"a":1}},{"$group":{"_id":{"h":{"$dateTrunc":{"date":"$ts","unit":"hour","timezone":"UTC"}}},"s":{"$sum":"$n"}}},{"$sort":{"s":-1}},{"$limit":3}],"limit":2}`))
	f.Add([]byte(`{"pipeline":[{"$sort":{"a":1}},{"$group":{"_id":null,"f":{"$first":"$a"}}},{"$project":{"f":1}}]}`))
	f.Add([]byte(`{"pipeline":[`))

	f.Fuzz(func(t *testing.T, body []byte) {
		p, err := agg.Parse(body, agg.Options{TimeField: "ts"})
		if err != nil {
			return
		}

		_ = p.Paths()
		_ = p.NewTable(agg.NewBudget(1 << 20)).Rows()
	})
}
