// Reports with aggregate: $match, $group by hour with $dateTrunc, $sum, $avg, $sort, and a filtered count.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	ndb "github.com/thadeu/ndb/sdk/go"
)

type Order struct {
	ID        string    `ndb:"_id"`
	CreatedAt time.Time `ndb:"created_at,time"`
	Status    string    `ndb:"status,index"`
	Amount    int       `ndb:"amount"`
}

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	addr := os.Getenv("NDB_URL")

	if addr == "" {
		addr = "unix:///tmp/ndb-examples.sock"
	}

	db, err := ndb.Open(addr)
	if err != nil {
		return err
	}

	defer db.Close()

	orders, err := ndb.Collection[Order](ctx, db, "orders")
	if err != nil {
		return err
	}

	if err := seed(ctx, orders); err != nil {
		return err
	}

	total, err := orders.Where().Count(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("orders: %d\n", total)

	// #region hourly
	perHour, err := orders.Aggregate(ctx, ndb.Pipeline{
		{"$match": map[string]any{"status": "paid"}},
		{"$group": map[string]any{
			"_id":        map[string]any{"$dateTrunc": map[string]any{"date": "$created_at", "unit": "hour"}},
			"orders":     map[string]any{"$count": map[string]any{}},
			"revenue":    map[string]any{"$sum": "$amount"},
			"avg_ticket": map[string]any{"$avg": "$amount"},
		}},
		ndb.SortStage(ndb.Desc("revenue")),
	})
	if err != nil {
		return err
	}

	fmt.Println("paid orders per hour, best revenue first:")

	for _, row := range perHour.Rows {
		fmt.Printf("  %s orders=%v revenue=%v avg=%v\n", row["_id"], row["orders"], row["revenue"], row["avg_ticket"])
	}
	// #endregion hourly

	// #region by-status
	byStatus, err := orders.Aggregate(ctx, ndb.Pipeline{
		{"$group": map[string]any{
			"_id":     "$status",
			"orders":  map[string]any{"$count": map[string]any{}},
			"revenue": map[string]any{"$sum": "$amount"},
		}},
		ndb.SortStage(ndb.Asc("_id")),
	})
	if err != nil {
		return err
	}

	fmt.Println("by status:")

	for _, row := range byStatus.Rows {
		fmt.Printf("  %-8s orders=%v revenue=%v\n", row["_id"], row["orders"], row["revenue"])
	}
	// #endregion by-status

	// #region count
	big, err := orders.Where(ndb.Eq("status", "paid"), ndb.Gte("amount", 100)).Count(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("paid orders of 100 or more: %d\n", big)
	// #endregion count

	return nil
}

// seed inserts the twelve orders once. A second run finds them and does nothing.
func seed(ctx context.Context, orders *ndb.Coll[Order]) error {
	existing, err := orders.Where().Count(ctx)
	if err != nil {
		return err
	}

	if existing > 0 {
		return nil
	}

	at := func(clock string) time.Time {
		t, _ := time.Parse(time.RFC3339, "2026-11-01T"+clock+"Z")

		return t
	}

	_, err = orders.Insert(ctx,
		Order{ID: "o01", CreatedAt: at("09:05:00"), Status: "paid", Amount: 50},
		Order{ID: "o02", CreatedAt: at("09:20:00"), Status: "paid", Amount: 70},
		Order{ID: "o03", CreatedAt: at("09:45:00"), Status: "refunded", Amount: 30},
		Order{ID: "o04", CreatedAt: at("10:02:00"), Status: "paid", Amount: 120},
		Order{ID: "o05", CreatedAt: at("10:15:00"), Status: "paid", Amount: 80},
		Order{ID: "o06", CreatedAt: at("10:30:00"), Status: "pending", Amount: 60},
		Order{ID: "o07", CreatedAt: at("10:55:00"), Status: "paid", Amount: 160},
		Order{ID: "o08", CreatedAt: at("11:05:00"), Status: "paid", Amount: 40},
		Order{ID: "o09", CreatedAt: at("11:25:00"), Status: "refunded", Amount: 90},
		Order{ID: "o10", CreatedAt: at("11:40:00"), Status: "paid", Amount: 60},
		Order{ID: "o11", CreatedAt: at("11:50:00"), Status: "pending", Amount: 20},
		Order{ID: "o12", CreatedAt: at("11:59:00"), Status: "paid", Amount: 110},
	)

	return err
}
