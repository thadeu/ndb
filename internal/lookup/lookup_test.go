package lookup_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/thadeu/ndb/internal/lookup"
)

func parse(t *testing.T, body string) []lookup.Spec {
	t.Helper()

	specs, err := lookup.Parse(json.RawMessage(body))
	if err != nil {
		t.Fatalf("parse %s: %v", body, err)
	}

	return specs
}

func TestParseRailsNames(t *testing.T) {
	specs := parse(t, `{
		"customer": {"belongs_to": "customers", "fk": "customer_id"},
		"items": {"has_many": "order_items", "foreign_key": "order_id", "sort": {"position": 1}, "limit": 5,
		          "lookup": {"product": {"belongs_to": "products", "fk": "sku", "pk": "code"}}},
		"invoice": {"has_one": "invoices", "fk": "order_id", "fields": ["number"]}
	}`)

	if len(specs) != 3 {
		t.Fatalf("specs = %d", len(specs))
	}

	byName := map[string]lookup.Spec{}

	for _, s := range specs {
		byName[s.Name] = s
	}

	c := byName["customer"]

	if c.From != "customers" || c.Local != "customer_id" || c.Foreign != "_id" || c.Many || !c.Batched {
		t.Fatalf("belongs_to = %+v", c)
	}

	i := byName["items"]

	if i.From != "order_items" || i.Local != "_id" || i.Foreign != "order_id" || !i.Many || i.Batched || i.Limit != 5 || len(i.Nested) != 1 {
		t.Fatalf("has_many = %+v", i)
	}

	if p := i.Nested[0]; p.Local != "sku" || p.Foreign != "code" {
		t.Fatalf("nested = %+v", p)
	}

	if v := byName["invoice"]; v.Many || v.Limit != 1 || v.Foreign != "order_id" || len(v.Fields) != 1 {
		t.Fatalf("has_one = %+v", v)
	}
}

func TestParseMongoNames(t *testing.T) {
	specs, err := lookup.ParseStage(json.RawMessage(`{"from": "customers", "localField": "customer_id", "foreignField": "_id", "as": "customer"}`))
	if err != nil {
		t.Fatal(err)
	}

	s := specs

	if s.Name != "customer" || s.From != "customers" || s.Local != "customer_id" || s.Foreign != "_id" || !s.Many {
		t.Fatalf("mongo = %+v", s)
	}

	r, err := lookup.ParseStage(json.RawMessage(`{"customer": {"belongs_to": "customers", "fk": "customer_id"}}`))
	if err != nil || r.Name != "customer" || r.Many {
		t.Fatalf("rails in a stage = %+v, %v", r, err)
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"no kind":         `{"x": {"fk": "a"}}`,
		"two kinds":       `{"x": {"belongs_to": "a", "has_many": "b", "fk": "c"}}`,
		"no fk":           `{"x": {"belongs_to": "a"}}`,
		"unknown key":     `{"x": {"belongs_to": "a", "fk": "b", "nope": 1}}`,
		"bad name":        `{"a.b": {"belongs_to": "a", "fk": "b"}}`,
		"id name":         `{"_id": {"belongs_to": "a", "fk": "b"}}`,
		"limit":           `{"x": {"has_many": "a", "fk": "b", "limit": 5000}}`,
		"pattern":         `{"x": {"belongs_to": "logs-*", "fk": "b"}}`,
		"three levels":    `{"a": {"has_many": "x", "fk": "f", "lookup": {"b": {"has_many": "y", "fk": "f", "lookup": {"c": {"belongs_to": "z", "fk": "f"}}}}}}`,
		"sort on belongs": `{"x": {"belongs_to": "a", "fk": "b", "sort": {"n": 1}}}`,
		"not an object":   `[1]`,
	}

	for name, body := range cases {
		_, err := lookup.Parse(json.RawMessage(body))

		var coded interface{ Code() string }

		if !errors.As(err, &coded) || coded.Code() != "lookup_invalid" {
			t.Errorf("%s: got %v, want lookup_invalid", name, err)
		}
	}
}

func TestSplitTakesLookupOutOfTheBody(t *testing.T) {
	rest, specs, added, err := lookup.Split([]byte(`{"query": {"status": "paid"}, "fields": ["total"], "lookup": {"customer": {"belongs_to": "customers", "fk": "customer_id"}}}`))
	if err != nil || len(specs) != 1 {
		t.Fatalf("split = %v, %v", specs, err)
	}

	var top map[string]json.RawMessage

	_ = json.Unmarshal(rest, &top)

	if _, ok := top["lookup"]; ok {
		t.Fatal("lookup is still in the body")
	}

	if string(top["fields"]) != `["total","customer_id"]` || len(added) != 1 || added[0] != "customer_id" {
		t.Fatalf("fields = %s, added = %v", top["fields"], added)
	}

	same, specs, _, err := lookup.Split([]byte(`{"query": {}}`))
	if err != nil || specs != nil || string(same) != `{"query": {}}` {
		t.Fatalf("no lookup: %s %v %v", same, specs, err)
	}
}

func TestRowHelpers(t *testing.T) {
	row := json.RawMessage(`{"_id":"o1","customer_id":"c1","n":1.0,"tags":["a","b"],"meta":{"k":"v"}}`)

	if keys := lookup.Keys(row, "customer_id"); len(keys) != 1 || keys[0].Key != `s:c1` {
		t.Fatalf("keys = %+v", keys)
	}

	if keys := lookup.Keys(row, "tags"); len(keys) != 2 {
		t.Fatalf("array keys = %+v", keys)
	}

	if keys := lookup.Keys(row, "meta.k"); len(keys) != 1 || keys[0].Key != `s:v` {
		t.Fatalf("dot path = %+v", keys)
	}

	if lookup.Keys(json.RawMessage(`{"n":1}`), "n")[0].Key != lookup.Keys(row, "n")[0].Key {
		t.Fatal("1 and 1.0 must be the same key")
	}

	out := lookup.Set(row, "customer", json.RawMessage(`{"_id":"c1"}`))

	if string(out) != `{"_id":"o1","customer_id":"c1","n":1.0,"tags":["a","b"],"meta":{"k":"v"},"customer":{"_id":"c1"}}` {
		t.Fatalf("set new = %s", out)
	}

	out = lookup.Set(out, "customer", json.RawMessage(`null`))

	var m map[string]any

	if err := json.Unmarshal(out, &m); err != nil || m["customer"] != nil || len(m) != 6 {
		t.Fatalf("set again = %s", out)
	}

	out = lookup.Without(out, []string{"customer_id"})
	m = map[string]any{}

	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}

	if _, ok := m["customer_id"]; ok {
		t.Fatalf("without = %s", out)
	}
}
