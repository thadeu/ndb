// Command basic writes and reads calls through an ndb daemon. Start the
// daemon first, for example: ndb -unix /tmp/ndb.sock -data-dir /tmp/ndb-data
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"time"

	ndb "github.com/thadeu/ndb/sdk/go"
)

type Call struct {
	CallID   string    `ndb:"call_id,id"`
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
	db, err := ndb.Open(addr)
	if err != nil {
		return err
	}

	defer db.Close()

	calls, err := ndb.Collection[Call](ctx, db, "calls")
	if err != nil {
		return err
	}

	id := fmt.Sprintf("c%d", time.Now().UnixNano())

	_, err = calls.Insert(ctx, Call{CallID: id, Status: "answered", DNIS: "1133", Duration: 42, TS: time.Now().UTC()})
	if err != nil {
		return err
	}

	answered, err := calls.Where(ndb.Eq("status", "answered")).Sort(ndb.Desc("ts")).Limit(10).All(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("answered calls: %d\n", len(answered))

	version, err := calls.Update(ctx, id, map[string]any{"status": "done"}, ndb.IfVersion(1))
	if err != nil {
		return err
	}

	fmt.Printf("updated %s to version %d\n", id, version)

	_, err = calls.Update(ctx, id, map[string]any{"status": "stale"}, ndb.IfVersion(1))

	if errors.Is(err, ndb.ErrVersionConflict) {
		fmt.Println("stale update refused, as expected")
	}

	return calls.Delete(ctx, id)
}
