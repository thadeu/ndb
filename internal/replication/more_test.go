package replication_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/replication"
)

func TestAPromotedDatabaseDoesNotCopyAgain(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()
	dir := t.TempDir()
	cp := startNode(t, dir, testKey)
	c := cp.copyOf(owner, "default", "default", replication.Options{})

	cp.attach()

	owner.must("POST", "/a/_doc", "", `{"_id":"1"}`)

	sameData(t, owner.db, cp.db)

	term, err := c.Promote()
	if err != nil {
		t.Fatal(err)
	}

	if again, err := c.Promote(); err != nil || again != term {
		t.Fatalf("promote twice: %d %v", again, err)
	}

	if err := c.Resync(); err == nil {
		t.Fatal("resync of a promoted database")
	}

	cp.stop()

	next := startNode(t, dir, testKey)
	nc := next.copyOf(owner, "default", "default", replication.Options{})

	if !nc.Promoted() || nc.First(time.Second) != nil {
		t.Fatal("a promoted database started a copy")
	}

	next.attach()

	next.must("POST", "/a/_doc", "", `{"_id":"local"}`)

	if n := len(docs(t, owner.db, "a")); n != 1 {
		t.Fatalf("a write of a promoted database went to the old owner: %d", n)
	}
}

func TestAWrongTokenIsDown(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()
	cp := startNode(t, t.TempDir(), testKey)

	src := replication.Source{Base: "http://" + owner.addr, Token: "wrong", Remote: "default", Local: "default"}

	c, err := replication.New(src, cp.db, cp.db.Key().Fingerprint(), quietLogger().Logger, replication.Options{Backoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	c.Start(bg)

	t.Cleanup(c.Stop)

	eventually(t, "down with the error", func() bool {
		st := c.Status()

		return st.State == replication.StateDown && strings.Contains(st.LastError, "unauthorized")
	})

	if c.ReadsLocal() {
		t.Fatal("a copy that never synced reads local")
	}
}

func TestAWriteWhoseRecordDoesNotComeIsStale(t *testing.T) {
	_, cp, c := pair(t)

	eventually(t, "synced", c.Synced)

	c.Stop()

	stale := replication.Options{Wait: 50 * time.Millisecond}
	sc := cp.copyOf(&node{addr: strings.TrimPrefix(c.Source().Base, "http://")}, "default", "default", stale)

	sc.Stop()

	status, _, h := cp.do("POST", "/a/_doc", "", `{"_id":"x"}`)

	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}

	if h.Get(replication.StaleHeader) != "true" {
		t.Fatalf("headers = %v", h)
	}
}

func TestAnOwnerThatIsNotThere(t *testing.T) {
	cp := startNode(t, t.TempDir(), testKey)
	gone := &node{addr: "127.0.0.1:1"}
	c := cp.copyOf(gone, "default", "default", replication.Options{})

	if err := c.First(100 * time.Millisecond); err != nil {
		t.Fatalf("first with no owner: %v", err)
	}

	cp.attach()

	status, body, _ := cp.do("GET", "/a/_doc/x", "", "")

	if status != http.StatusServiceUnavailable || !strings.Contains(string(body), "owner_unavailable") {
		t.Fatalf("read before the first sync: %d %s", status, body)
	}
}
