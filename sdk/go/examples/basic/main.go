// Command basic writes and reads calls through an ndb daemon. Start the
// daemon first, for example: ndb -unix /tmp/ndb.sock -data-dir /tmp/ndb-data
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	ndb "github.com/thadeu/ndb/sdk/go"
)

type Call struct {
	ID       string    `ndb:"_id"`
	Status   string    `ndb:"status,index"`
	DNIS     string    `ndb:"dnis,index"`
	Duration int       `ndb:"duration"`
	TS       time.Time `ndb:"ts,time"`
}

func main() {
	addr := flag.String("addr", "unix:///tmp/ndb.sock", "daemon address: unix:///path or tcp://host:port")

	flag.Parse()

	if err := run(context.Background(), *addr); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, addr string) error {
	db, err := ndb.Connect(addr)
	if err != nil {
		return err
	}

	defer db.Close()

	calls, err := ndb.Collection[Call](db, "calls")
	if err != nil {
		return err
	}

	id := ndb.NewID()

	_, err = calls.Insert(ctx, Call{ID: id, Status: "answered", DNIS: "1133", Duration: 42, TS: time.Now().UTC()})
	if err != nil {
		return err
	}

	answered, err := calls.Query(ndb.Eq("status", "answered")).Sort(ndb.Desc("ts")).Limit(10).All(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("answered calls: %d\n", len(answered))

	byID := ndb.Eq("_id", id)

	res, err := calls.UpdateBy(ctx, byID, ndb.Ops{"$set": {"status": "done"}})
	if err != nil {
		return err
	}

	fmt.Printf("updated %s: matched %d, modified %d\n", id, res.Matched, res.Modified)

	stale := ndb.And(byID, ndb.Eq("_v", 1))

	res, err = calls.UpdateBy(ctx, stale, ndb.Ops{"$set": {"status": "stale"}})
	if err != nil {
		return err
	}

	if res.Matched == 0 {
		fmt.Println("stale update matched nothing, as expected")
	}

	_, err = calls.DeleteBy(ctx, byID)

	return err
}
