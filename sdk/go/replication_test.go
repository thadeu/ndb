package ndb

import (
	"context"
	"strings"
	"testing"
)

// TestReadAfterAWriteThroughACopy: a copy forwards the write to its owner
// and answers after it has applied it, so the next read sees it.
func TestReadAfterAWriteThroughACopy(t *testing.T) {
	key := "NDB_ENCRYPTION_KEY=" + strings.Repeat("ab", 32)

	stopOwner, err := launchDaemonEnv(daemon.dir, "", false, []string{key})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stopOwner)

	owner := lastLaunch.tcp

	stopCopy, err := launchDaemonEnv(daemon.dir, "", false, []string{key, "NDB_REPLICA_OF=" + owner})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(stopCopy)

	ctx := context.Background()
	db := openTest(t, lastLaunch.unix)

	calls, err := Collection[Call](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	for i := range 20 {
		id := "c" + string(rune('a'+i))

		if _, err := calls.Insert(ctx, Call{CallID: id, Status: "new"}); err != nil {
			t.Fatal(err)
		}

		got, err := calls.Get(ctx, id)
		if err != nil || got.Status != "new" {
			t.Fatalf("read after the write %s: %+v %v", id, got, err)
		}
	}

	ownerDB := openTest(t, owner)

	n, err := Collection[Call](ownerDB, calls.Name())
	if err != nil {
		t.Fatal(err)
	}

	if count, err := n.Count(ctx); err != nil || count != 20 {
		t.Fatalf("the owner has %d calls (%v)", count, err)
	}
}
