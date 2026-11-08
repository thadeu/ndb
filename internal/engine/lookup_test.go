package engine_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/lookup"
)

func seedShop(t *testing.T) *engine.DB {
	t.Helper()

	db := open(t, cfg(t.TempDir(), nil))

	put := func(def catalog.Def, docs ...string) {
		col := ensure(t, db, def)

		for _, d := range docs {
			if _, err := col.Insert(ctx, []byte(d)); err != nil {
				t.Fatal(err)
			}
		}
	}

	put(catalog.Def{Name: "customers"}, `{"_id":"c1","name":"Ana"}`, `{"_id":"c2","name":"Bruno"}`)
	put(catalog.Def{Name: "products", Indexes: []string{"code"}}, `{"_id":"p1","code":"A","name":"Mouse"}`, `{"_id":"p2","code":"B","name":"Teclado"}`)
	put(catalog.Def{Name: "orders"},
		`{"_id":"o1","customer_id":"c1","total":120,"tags":["A","B"]}`,
		`{"_id":"o2","customer_id":"c2","total":40}`,
		`{"_id":"o3","customer_id":"nobody","total":1}`)
	put(catalog.Def{Name: "order_items", Indexes: []string{"order_id"}},
		`{"_id":"i1","order_id":"o1","sku":"A","pos":2,"qty":1}`,
		`{"_id":"i2","order_id":"o1","sku":"B","pos":1,"qty":3}`,
		`{"_id":"i3","order_id":"o1","sku":"A","pos":3,"qty":0}`,
		`{"_id":"i4","order_id":"o2","sku":"A","pos":1,"qty":1}`)
	put(catalog.Def{Name: "invoices", Indexes: []string{"order_id"}}, `{"_id":"f1","order_id":"o1","number":"NF-1","total":120}`)

	return db
}

// searchWith runs _search on orders with a lookup, like the server does.
func searchWith(t *testing.T, db *engine.DB, body string) []map[string]any {
	t.Helper()

	rows, err := lookupSearch(db, "orders", body)
	if err != nil {
		t.Fatal(err)
	}

	out := make([]map[string]any, len(rows))

	for i, r := range rows {
		if err := json.Unmarshal(r, &out[i]); err != nil {
			t.Fatalf("row %s: %v", r, err)
		}
	}

	return out
}

func lookupSearch(db *engine.DB, coll, body string) ([]json.RawMessage, error) {
	rest, specs, added, err := lookup.Split([]byte(body))
	if err != nil {
		return nil, err
	}

	col, err := db.Get(coll)
	if err != nil {
		return nil, err
	}

	page, err := col.Find(ctx, rest)
	if err != nil {
		return nil, err
	}

	rows, err := db.Lookup(ctx, page.Rows, specs)
	if err != nil {
		return nil, err
	}

	for i := range rows {
		rows[i] = lookup.Without(rows[i], added)
	}

	return rows, nil
}

func TestLookupBelongsTo(t *testing.T) {
	db := seedShop(t)

	rows := searchWith(t, db, `{"sort":{"_id":1},"fields":["_id","total"],"lookup":{"customer":{"belongs_to":"customers","fk":"customer_id","fields":["name"]}}}`)

	if len(rows) != 3 {
		t.Fatalf("rows = %d", len(rows))
	}

	if c, _ := rows[0]["customer"].(map[string]any); c["name"] != "Ana" || c["_id"] != nil {
		t.Fatalf("o1 customer = %v", rows[0]["customer"])
	}

	if _, kept := rows[0]["customer_id"]; kept {
		t.Fatalf("customer_id was added for the lookup and must go: %v", rows[0])
	}

	if rows[2]["customer"] != nil {
		t.Fatalf("o3 has no customer: %v", rows[2]["customer"])
	}
}

func TestLookupHasManyHasOneAndNested(t *testing.T) {
	db := seedShop(t)

	rows := searchWith(t, db, `{"sort":{"_id":1},"lookup":{
		"items":{"has_many":"order_items","fk":"order_id","query":{"qty":{"$gt":0}},"sort":{"pos":1},"limit":5,
		         "lookup":{"product":{"belongs_to":"products","fk":"sku","pk":"code","fields":["name"]}}},
		"invoice":{"has_one":"invoices","fk":"order_id","fields":["number"]}}}`)

	items, _ := rows[0]["items"].([]any)

	if len(items) != 2 {
		t.Fatalf("o1 items = %v (qty 0 filtered out)", rows[0]["items"])
	}

	first, _ := items[0].(map[string]any)
	product, _ := first["product"].(map[string]any)

	if first["_id"] != "i2" || product["name"] != "Teclado" {
		t.Fatalf("first item by pos = %v", first)
	}

	if inv, _ := rows[0]["invoice"].(map[string]any); inv["number"] != "NF-1" || inv["order_id"] != nil {
		t.Fatalf("o1 invoice = %v", rows[0]["invoice"])
	}

	if rows[1]["invoice"] != nil {
		t.Fatalf("o2 invoice = %v", rows[1]["invoice"])
	}

	if got, _ := rows[2]["items"].([]any); got == nil || len(got) != 0 {
		t.Fatalf("o3 items = %#v, want []", rows[2]["items"])
	}
}

func TestLookupArrayFKGivesAnArray(t *testing.T) {
	db := seedShop(t)

	rows := searchWith(t, db, `{"query":{"_id":"o1"},"lookup":{"products":{"belongs_to":"products","fk":"tags","pk":"code","fields":["name"]}}}`)

	products, _ := rows[0]["products"].([]any)

	if len(products) != 2 {
		t.Fatalf("products = %v", rows[0]["products"])
	}
}

func TestLookupChecks(t *testing.T) {
	db := seedShop(t)

	_, err := lookupSearch(db, "orders", `{"lookup":{"items":{"has_many":"order_items","fk":"sku"}}}`)

	var coded interface{ Code() string }

	if !errors.As(err, &coded) || coded.Code() != "lookup_unindexed" || !strings.Contains(err.Error(), "_indexes/sku") {
		t.Fatalf("unindexed: %v", err)
	}

	if _, err := lookupSearch(db, "orders", `{"lookup":{"x":{"belongs_to":"nope","fk":"customer_id"}}}`); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("unknown collection: %v", err)
	}

	defer engine.SetLookupBudget(3)()

	if _, err := lookupSearch(db, "orders", `{"lookup":{"items":{"has_many":"order_items","fk":"order_id"}}}`); !errors.As(err, &coded) || coded.Code() != "lookup_too_large" {
		t.Fatalf("too large: %v", err)
	}
}

func TestLookupIntoAPartitionedCollection(t *testing.T) {
	db := seedShop(t)

	def, err := db.EnsurePartitioned(catalog.Def{Name: "events", Time: "ts", PartitionBy: "day", Indexes: []string{"order_id"}})
	if err != nil {
		t.Fatal(err)
	}

	for i, at := range []time.Time{time.Now().AddDate(0, 0, -1), time.Now()} {
		col, err := db.PartitionAt(def, at)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := col.Insert(ctx, []byte(`{"order_id":"o1","n":`+string(rune('0'+i))+`,"ts":"`+at.UTC().Format(time.RFC3339)+`"}`)); err != nil {
			t.Fatal(err)
		}
	}

	rows := searchWith(t, db, `{"query":{"_id":"o1"},"lookup":{"events":{"has_many":"events","fk":"order_id","sort":{"n":1}}}}`)

	if events, _ := rows[0]["events"].([]any); len(events) != 2 {
		t.Fatalf("events = %v", rows[0]["events"])
	}
}
