// Outbox: a producer inserts events, a consumer watches them with a filter and acks after the work.
// Delivery is at least once, so the work must be safe to repeat: a receipt per event id does that.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	ndb "github.com/thadeu/ndb/sdk/go"
)

type Event struct {
	EventID   string    `ndb:"event_id,id"`
	Topic     string    `ndb:"topic,index"`
	CreatedAt time.Time `ndb:"created_at,time"`
}

type Receipt struct {
	EventID string    `ndb:"event_id,id"`
	DoneAt  time.Time `ndb:"done_at"`
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

	outbox, err := ndb.Collection[Event](ctx, db, "outbox")
	if err != nil {
		return err
	}

	receipts, err := ndb.Collection[Receipt](ctx, db, "receipts")
	if err != nil {
		return err
	}

	if err := reset(ctx, outbox, receipts); err != nil {
		return err
	}

	// #region produce
	topics := []string{"invoice.paid", "user.signup", "invoice.paid", "user.signup", "invoice.paid"}

	for i, topic := range topics {
		id := fmt.Sprintf("evt-%d", i+1)

		if _, err := outbox.Insert(ctx, Event{EventID: id, Topic: topic, CreatedAt: time.Now().UTC()}); err != nil {
			return err
		}

		fmt.Printf("produce %s %s\n", id, topic)
	}
	// #endregion produce

	// #region consume
	paid := ndb.WatchWhere(ndb.Eq("topic", "invoice.paid"))

	fmt.Println("consumer billing, run 1 (where topic = invoice.paid):")

	for ev, err := range outbox.Watch(ctx, "billing", paid, ndb.Limit(2)) {
		if err != nil {
			return err
		}

		if err := charge(ctx, receipts, ev.ID); err != nil {
			return err
		}

		if ev.ID == "evt-3" {
			fmt.Println("  evt-3 charged, crash before ack")

			break
		}

		if err := outbox.Ack(ctx, ev); err != nil {
			return err
		}

		fmt.Printf("  %s charged, acked\n", ev.ID)
	}
	// #endregion consume

	// #region restart
	fmt.Println("consumer billing, run 2 (restart, resumes after the last ack):")

	for ev, err := range outbox.Watch(ctx, "billing", paid, ndb.Limit(2)) {
		if err != nil {
			return err
		}

		err := charge(ctx, receipts, ev.ID)

		switch {
		case errors.Is(err, ndb.ErrDuplicateID):
			fmt.Printf("  %s delivered again, receipt exists, skipped, acked\n", ev.ID)
		case err != nil:
			return err
		default:
			fmt.Printf("  %s charged, acked\n", ev.ID)
		}

		if err := outbox.Ack(ctx, ev); err != nil {
			return err
		}
	}
	// #endregion restart

	total, err := receipts.Where().Count(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("receipts: %d (one per invoice, none twice)\n", total)

	return nil
}

// #region work
// charge is the work of the consumer. The receipt id is the event id, so a second
// delivery of the same event fails with duplicate_id and the work is not done twice.
func charge(ctx context.Context, receipts *ndb.Coll[Receipt], eventID string) error {
	_, err := receipts.Insert(ctx, Receipt{EventID: eventID, DoneAt: time.Now().UTC()})

	return err
}

// #endregion work

// reset makes the example repeatable: it removes what an earlier run left.
func reset(ctx context.Context, outbox *ndb.Coll[Event], receipts *ndb.Coll[Receipt]) error {
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("evt-%d", i)

		if err := outbox.Delete(ctx, id); err != nil && !errors.Is(err, ndb.ErrNotFound) {
			return err
		}

		if err := receipts.Delete(ctx, id); err != nil && !errors.Is(err, ndb.ErrNotFound) {
			return err
		}
	}

	return nil
}
