// Direct mode: other processes append NDJSON lines to files, ndb only reads them.
// Run it from this folder (go run .): it starts ../append.sh as the "other process".
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// #region model
type Line struct {
	ID    string    `ndb:"_id"`
	TS    time.Time `ndb:"ts,time"`
	App   string    `ndb:"app,index"`
	Level string    `ndb:"level,index"`
	Msg   string    `ndb:"msg"`
	Path  string    `ndb:"path"`
	MS    int       `ndb:"ms"`
}

// #endregion model

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

	db, err := ndb.Connect(addr)
	if err != nil {
		return err
	}

	defer db.Close()

	logs, err := ndb.Collection[Line](db, "app_logs")
	if err != nil {
		return err
	}

	history := ndb.Lt("ts", time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC))

	if err := waitFor(func() (bool, error) {
		n, err := logs.Count(ctx, history)

		return n == 12, err
	}); err != nil {
		return err
	}

	fmt.Println("history: 12 lines from 2 files")

	// #region filter
	failures, err := logs.Query(history, ndb.Eq("level", "error")).Sort(ndb.Desc("ts")).All(ctx)
	if err != nil {
		return err
	}

	fmt.Println("errors, newest first:")

	for _, l := range failures {
		fmt.Printf("  %s %-6s %s\n", l.TS.Format("15:04:05"), l.App, l.Msg)
	}

	slow, err := logs.Count(ctx, history, ndb.Gte("ms", 800))
	if err != nil {
		return err
	}

	fmt.Printf("slow (ms >= 800): %d\n", slow)
	// #endregion filter

	// #region regex
	payments, err := logs.Count(ctx, history, ndb.Regex("path", "^/api/v[0-9]+/payments"))
	if err != nil {
		return err
	}

	fmt.Printf("payment paths ($regex): %d\n", payments)
	// #endregion regex

	// #region cursor
	var pages []string

	query := logs.Query(history).Sort(ndb.Asc("ts")).Limit(5)

	for {
		rows, err := query.All(ctx)
		if err != nil {
			return err
		}

		pages = append(pages, strconv.Itoa(len(rows)))

		cursor := query.NextCursor()

		if cursor == "" {
			break
		}

		query = logs.Query(history).Sort(ndb.Asc("ts")).Limit(5).After(cursor)
	}

	fmt.Printf("pages (limit 5): %s\n", strings.Join(pages, ", "))
	// #endregion cursor

	// #region append
	msg := fmt.Sprintf("hello from go %d", time.Now().UnixMilli())
	line := fmt.Sprintf(`{"ts":%q,"app":"cli","level":"info","msg":%q}`, time.Now().UTC().Format(time.RFC3339), msg)

	if err := exec.Command("../append.sh", "cli", "3003", line).Run(); err != nil {
		return err
	}

	fmt.Println("append: cli wrote one line")

	if err := waitFor(func() (bool, error) {
		n, err := logs.Count(ctx, ndb.Eq("msg", msg))

		return n == 1, err
	}); err != nil {
		return err
	}

	fmt.Println("the new line is in the collection")

	added, err := logs.Query(ndb.Eq("msg", msg)).First(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("it has no _id, so it has no _doc/{id} routes: _id is %q\n", added.ID)
	// #endregion append

	return nil
}

// waitFor polls until ready returns true. The daemon sees a new line within
// a moment (file events, with a 5 s poll as the fallback).
func waitFor(ready func() (bool, error)) error {
	deadline := time.Now().Add(15 * time.Second)

	for time.Now().Before(deadline) {
		ok, err := ready()
		if err != nil {
			return err
		}

		if ok {
			return nil
		}

		time.Sleep(200 * time.Millisecond)
	}

	return fmt.Errorf("timeout waiting for the daemon to read the files")
}
