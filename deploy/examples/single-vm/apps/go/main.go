// Command visits is a tiny sample app: every 5 seconds it records one visit
// in the shared "visits" collection and prints how many visits each app has.
// It reads NDB_URL (default unix:///run/ndb/ndb.sock). With NDB_DIRECT_DIR set
// it appends the visit to a file instead (direct mode).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	ndb "github.com/thadeu/ndb/sdk/go"
)

const app = "go"

// The json tags matter in direct mode, where the app marshals the line itself.
type Visit struct {
	ID  string    `ndb:"id,id" json:"id"`
	App string    `ndb:"app,index" json:"app"`
	Pod string    `ndb:"pod" json:"pod"`
	TS  time.Time `ndb:"ts,time" json:"ts"`
}

func main() {
	addr := env("NDB_URL", "unix:///run/ndb/ndb.sock")

	db, err := ndb.Open(addr)
	if err != nil {
		log.Fatal(err)
	}

	defer db.Close()

	ctx := context.Background()
	pod, _ := os.Hostname()

	visits := waitFor(ctx, db)

	for {
		v := Visit{ID: fmt.Sprintf("%s-%s-%d", app, pod, time.Now().UnixNano()), App: app, Pod: pod, TS: time.Now().UTC()}

		if err := record(ctx, visits, v); err != nil {
			log.Printf("record: %v", err)
		}

		report(ctx, visits)

		time.Sleep(5 * time.Second)
	}
}

// waitFor retries until the daemon is up: the apps may start before ndb.
func waitFor(ctx context.Context, db *ndb.DB) *ndb.Coll[Visit] {
	for {
		visits, err := ndb.Collection[Visit](ctx, db, "visits")
		if err == nil {
			return visits
		}

		log.Printf("waiting for ndb: %v", err)

		time.Sleep(time.Second)
	}
}

func record(ctx context.Context, visits *ndb.Coll[Visit], v Visit) error {
	dir := os.Getenv("NDB_DIRECT_DIR")

	if dir == "" {
		_, err := visits.Insert(ctx, v)

		return err
	}

	line, err := json.Marshal(v)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(filepath.Join(dir, app+"-"+v.Pod+".ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	defer f.Close()

	_, err = f.Write(append(line, '\n'))

	return err
}

func report(ctx context.Context, visits *ndb.Coll[Visit]) {
	mine, err := visits.Where(ndb.Eq("app", app)).Count(ctx)
	if err != nil {
		log.Printf("count: %v", err)

		return
	}

	all, err := visits.Where().Count(ctx)
	if err != nil {
		log.Printf("count: %v", err)

		return
	}

	log.Printf("%s visits: %d, all apps: %d", app, mine, all)
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}
