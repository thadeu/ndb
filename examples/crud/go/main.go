// CRUD on the users collection: insert, get, update with if_version, delete, count.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// #region model
type User struct {
	UserID  string `ndb:"user_id,id"`
	Email   string `ndb:"email"`
	Plan    string `ndb:"plan,index"`
	Version uint64 `ndb:"_v,version"`
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

	// #region connect
	db, err := ndb.Open(addr) // NDB_TOKEN is read by the SDK
	if err != nil {
		return err
	}

	defer db.Close()

	users, err := ndb.Collection[User](ctx, db, "users")
	if err != nil {
		return err
	}

	// #endregion connect

	for _, id := range []string{"u1", "u2"} {
		if err := users.Delete(ctx, id); err != nil && !errors.Is(err, ndb.ErrNotFound) {
			return err
		}
	}

	// #region insert
	results, err := users.Insert(ctx,
		User{UserID: "u1", Email: "ada@example.com", Plan: "free"},
		User{UserID: "u2", Email: "grace@example.com", Plan: "free"},
	)
	if err != nil {
		return err
	}

	fmt.Printf("insert u1, u2 -> v%d, v%d\n", results[0].V, results[1].V)
	// #endregion insert

	// #region get
	ada, err := users.Get(ctx, "u1")
	if err != nil {
		return err
	}

	fmt.Printf("get u1: %s plan=%s v%d\n", ada.Email, ada.Plan, ada.Version)
	// #endregion get

	// #region update
	v, err := users.Update(ctx, "u1", map[string]any{"plan": "pro"}, ndb.IfVersion(ada.Version))
	if err != nil {
		return err
	}

	fmt.Printf("update u1 plan=pro (if_version %d) -> v%d\n", ada.Version, v)
	// #endregion update

	// #region conflict
	_, err = users.Update(ctx, "u1", map[string]any{"plan": "team"}, ndb.IfVersion(ada.Version))

	if errors.Is(err, ndb.ErrVersionConflict) {
		fmt.Println("update u1 plan=team (if_version 1) -> version_conflict")

		fresh, err := users.Get(ctx, "u1")
		if err != nil {
			return err
		}

		v, err = users.Update(ctx, "u1", map[string]any{"plan": "team"}, ndb.IfVersion(fresh.Version))
		if err != nil {
			return err
		}

		fmt.Printf("reload u1: v%d, retry with if_version %d -> v%d\n", fresh.Version, fresh.Version, v)
	} else if err != nil {
		return err
	}
	// #endregion conflict

	// #region delete
	if err := users.Delete(ctx, "u2"); err != nil {
		return err
	}

	fmt.Println("delete u2")
	// #endregion delete

	// #region count
	total, err := users.Where().Count(ctx)
	if err != nil {
		return err
	}

	onTeam, err := users.Where(ndb.Eq("plan", "team")).Count(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("count: %d user(s), %d on plan team\n", total, onTeam)
	// #endregion count

	return nil
}
