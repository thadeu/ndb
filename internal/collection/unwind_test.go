package collection_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
)

// The rows of an $unwind pipeline must be the ones a script gets when it
// expands the arrays by hand: in the memtable and in segments.
func TestUnwindGivesTheRowsOfAHandExpansion(t *testing.T) {
	r := rand.New(rand.NewPCG(26, 2))
	root := t.TempDir()

	c := openData(t, root, catalog.Def{Name: "orders", Indexes: []string{"status"}})

	defer c.Close()

	type item struct {
		Sku string `json:"sku"`
		Qty int    `json:"qty"`
	}

	type order struct {
		ID     string `json:"_id"`
		Status string `json:"status"`
		Items  any    `json:"items,omitempty"`
	}

	var orders []order

	write := func(from, to int) {
		docs := make([][]byte, 0, to-from)

		for i := from; i < to; i++ {
			o := order{ID: fmt.Sprintf("o%04d", i), Status: []string{"paid", "open"}[r.IntN(2)]}

			switch r.IntN(8) {
			case 0:
			case 1:
				o.Items = []item{}
			case 2:
				o.Items = item{Sku: "solo", Qty: 1 + r.IntN(5)}
			default:
				items := make([]item, 1+r.IntN(4))

				for k := range items {
					items[k] = item{Sku: fmt.Sprintf("s%d", r.IntN(6)), Qty: 1 + r.IntN(9)}
				}

				o.Items = items
			}

			raw, _ := json.Marshal(o)

			docs = append(docs, raw)
			orders = append(orders, o)
		}

		if _, err := c.Insert(ctx, docs...); err != nil {
			t.Fatal(err)
		}
	}

	// want is the answer by hand: quantity, lines and orders per sku, for
	// the paid orders and the items with qty >= minQty.
	want := func(minQty int) []string {
		type sum struct {
			qty, lines int
			orders     map[string]bool
		}

		by := map[string]*sum{}

		for _, o := range orders {
			if o.Status != "paid" {
				continue
			}

			var items []item

			switch v := o.Items.(type) {
			case []item:
				items = v
			case item:
				items = []item{v}
			}

			for _, it := range items {
				if it.Qty < minQty {
					continue
				}

				if by[it.Sku] == nil {
					by[it.Sku] = &sum{orders: map[string]bool{}}
				}

				by[it.Sku].qty += it.Qty
				by[it.Sku].lines++
				by[it.Sku].orders[o.ID] = true
			}
		}

		var out []string

		for sku, s := range by {
			out = append(out, fmt.Sprintf("%s qty=%d lines=%d orders=%d", sku, s.qty, s.lines, len(s.orders)))
		}

		slices.Sort(out)

		return out
	}

	got := func(minQty int) []string {
		body := fmt.Sprintf(`{"pipeline":[
			{"$match":{"status":"paid"}},
			{"$unwind":"$items"},
			{"$match":{"items.qty":{"$gte":%d}}},
			{"$group":{"_id":"$items.sku","qty":{"$sum":"$items.qty"},"lines":{"$count":{}},"orders":{"$distinct":"$_id"}}}],"limit":1000}`, minQty)

		p, err := c.ParseAggregate([]byte(body))
		if err != nil {
			t.Fatal(err)
		}

		page, err := c.Aggregate(ctx, p)
		if err != nil {
			t.Fatal(err)
		}

		var out []string

		for _, row := range page.Rows {
			var m map[string]any

			if err := json.Unmarshal(row, &m); err != nil {
				t.Fatal(err)
			}

			out = append(out, fmt.Sprintf("%v qty=%v lines=%v orders=%v", m["_id"], m["qty"], m["lines"], m["orders"]))
		}

		slices.Sort(out)

		return out
	}

	check := func(stage string) {
		for _, minQty := range []int{0, 3, 7, 99} {
			if g, w := got(minQty), want(minQty); !slices.Equal(g, w) {
				t.Fatalf("%s, qty >= %d:\n got %s\nwant %s", stage, minQty, strings.Join(g, "; "), strings.Join(w, "; "))
			}
		}
	}

	write(0, 800)
	waitReady(t, c)
	check("memtable")

	if err := c.Seal(); err != nil {
		t.Fatal(err)
	}

	check("sealed")

	write(800, 1200)
	check("sealed and memtable")
}
