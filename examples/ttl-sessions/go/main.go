// Expiry like Redis: expire_in on insert, ttl, expire, persist. An expired document is hidden at once.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// #region model
type Session struct {
	SID       string    `ndb:"sid,id"`
	CreatedAt time.Time `ndb:"created_at,time"`
	ExpiresAt time.Time `ndb:"expires_at,expires"`
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

	db, err := ndb.Open(addr)
	if err != nil {
		return err
	}

	defer db.Close()

	sessions, err := ndb.Collection[Session](ctx, db, "sessions")
	if err != nil {
		return err
	}

	for _, id := range []string{"s-short", "s-long", "s-keep"} {
		if err := sessions.Delete(ctx, id); err != nil && !errors.Is(err, ndb.ErrNotFound) {
			return err
		}
	}

	// #region insert
	now := time.Now().UTC()

	_, err = sessions.InsertWith(ctx, []Session{{SID: "s-short", CreatedAt: now}}, ndb.ExpireIn(2*time.Second))
	if err != nil {
		return err
	}

	_, err = sessions.InsertWith(ctx, []Session{{SID: "s-long", CreatedAt: now}, {SID: "s-keep", CreatedAt: now}}, ndb.ExpireIn(time.Hour))
	if err != nil {
		return err
	}

	fmt.Println("insert s-short (expire_in 2s), s-long and s-keep (expire_in 1h)")
	// #endregion insert

	// #region ttl
	short, _, err := sessions.TTL(ctx, "s-short")
	if err != nil {
		return err
	}

	long, _, err := sessions.TTL(ctx, "s-long")
	if err != nil {
		return err
	}

	fmt.Printf("ttl s-short: %.0fs, s-long: %.0fs\n", short.Seconds(), long.Seconds())
	// #endregion ttl

	// #region extend
	if _, err := sessions.Expire(ctx, "s-long", 30*time.Minute); err != nil {
		return err
	}

	left, _, err := sessions.TTL(ctx, "s-long")
	if err != nil {
		return err
	}

	fmt.Printf("expire s-long 30m -> ttl %.0fs\n", left.Seconds())
	// #endregion extend

	// #region persist
	if _, err := sessions.Persist(ctx, "s-keep"); err != nil {
		return err
	}

	_, expires, err := sessions.TTL(ctx, "s-keep")
	if err != nil {
		return err
	}

	if !expires {
		fmt.Println("persist s-keep -> ttl -1 (never expires)")
	}
	// #endregion persist

	fmt.Println("waiting 3s...")
	time.Sleep(3 * time.Second)

	// #region after
	alive, err := sessions.Where().All(ctx)
	if err != nil {
		return err
	}

	ids := make([]string, len(alive))

	for i, s := range alive {
		ids[i] = s.SID
	}

	fmt.Printf("live sessions: %d (%s)\n", len(alive), strings.Join(ids, ", "))

	_, _, err = sessions.TTL(ctx, "s-short")

	if errors.Is(err, ndb.ErrNotFound) {
		fmt.Println("ttl s-short: gone")
	} else if err != nil {
		return err
	}
	// #endregion after

	return nil
}
