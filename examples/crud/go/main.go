// CRUD on the users collection: insert, get by _id, update with a _v guard, delete, count.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// #region model
type User struct {
	ID      string `ndb:"_id"`
	Email   string `ndb:"email,unique"`
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
	db, err := ndb.Connect(addr) // NDB_TOKEN is read by the SDK
	if err != nil {
		return err
	}

	defer db.Close()

	users, err := ndb.Collection[User](db, "users")
	if err != nil {
		return err
	}

	// #endregion connect

	for _, id := range []string{"u1", "u2"} {
		if _, err := users.Delete(ctx, id); err != nil {
			return err
		}
	}

	// #region insert
	results, err := users.InsertMany(ctx, []User{
		{ID: "u1", Email: "ada@example.com", Plan: "free"},
		{ID: "u2", Email: "grace@example.com", Plan: "free"},
	})
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
	saved, err := users.Update(ctx, "u1", ndb.Ops{"$set": {"plan": "pro"}}, ndb.Guard(ndb.Eq("_v", ada.Version)))
	if err != nil {
		return err
	}

	fmt.Printf("update u1 plan=pro (_v %d) -> v%d\n", ada.Version, saved.Doc.Version)
	// #endregion update

	// #region conflict
	stale, err := users.Update(ctx, "u1", ndb.Ops{"$set": {"plan": "team"}}, ndb.Guard(ndb.Eq("_v", ada.Version)))
	if err != nil {
		return err
	}

	if stale.Matched == 0 {
		fmt.Printf("update u1 plan=team (_v %d) -> matched 0\n", ada.Version)

		fresh, err := users.Get(ctx, "u1")
		if err != nil {
			return err
		}

		retried, err := users.Update(ctx, "u1", ndb.Ops{"$set": {"plan": "team"}}, ndb.Guard(ndb.Eq("_v", fresh.Version)))
		if err != nil {
			return err
		}

		fmt.Printf("reload u1: v%d, retry with _v %d -> v%d\n", fresh.Version, fresh.Version, retried.Doc.Version)
	}
	// #endregion conflict

	// #region delete
	if _, err := users.Delete(ctx, "u2"); err != nil {
		return err
	}

	fmt.Println("delete u2")
	// #endregion delete

	// #region count
	total, err := users.Count(ctx)
	if err != nil {
		return err
	}

	onTeam, err := users.Count(ctx, ndb.Eq("plan", "team"))
	if err != nil {
		return err
	}

	fmt.Printf("count: %d user(s), %d on plan team\n", total, onTeam)
	// #endregion count

	return nil
}
