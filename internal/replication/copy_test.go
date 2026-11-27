package replication_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/replication"
)

// pair is an owner and a copy of its default database, both ready.
func pair(t *testing.T) (*node, *node, *replication.Copy) {
	t.Helper()

	owner := startNode(t, t.TempDir(), testKey).attach()
	cp := startNode(t, t.TempDir(), testKey)
	c := cp.copyOf(owner, "default", "default", replication.Options{})

	if err := c.First(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	cp.attach()

	return owner, cp, c
}

func TestCopyHasTheDataOfTheOwnerAfterEveryKindOfWrite(t *testing.T) {
	owner, cp, c := pair(t)

	owner.must("PUT", "/users", "", `{"indexes":["email"],"unique":["email"]}`)
	owner.must("POST", "/users/_doc", "", `{"_id":"u1","email":"a@x","n":1}`)
	owner.must("POST", "/users/_bulk", "", `{"docs":[{"_id":"u2","email":"b@x","n":2},{"_id":"u3","email":"c@x","n":3}]}`)
	owner.must("POST", "/users/_update/u1", "", `{"update":{"$inc":{"n":10}}}`)
	owner.must("DELETE", "/users/_doc/u2", "", "")
	owner.must("POST", "/users/_tx", "", `{"ops":[{"op":"insert","doc":{"_id":"u4","email":"d@x"}},{"op":"update","id":"u3","update":{"$set":{"n":30}}}]}`)
	owner.must("POST", "/users/_update_by_query", "", `{"query":{"email":"c@x"},"update":{"$set":{"seen":true}}}`)

	owner.must("POST", "/logs/_doc", "", `{"_id":"l1","msg":"x"}`)
	owner.must("POST", "/tmp/_doc", "", `{"_id":"t1"}`)
	owner.must("POST", "/gone/_doc", "", `{"_id":"g1"}`)

	sameData(t, owner.db, cp.db)

	owner.must("POST", "/logs/_truncate?confirm=logs", "", "")
	owner.must("POST", "/logs/_doc", "", `{"_id":"l2","msg":"after the truncate"}`)
	owner.must("DELETE", "/gone?confirm=gone", "", "")
	owner.must("DELETE", "/users/_indexes/email", "", "")

	sameData(t, owner.db, cp.db)

	eventually(t, "the index drop is on the copy", func() bool {
		want, _ := owner.db.Definition("users")
		got, _ := cp.db.Definition("users")

		return len(got.Indexes) == len(want.Indexes) && len(got.Unique) == 1
	})

	eventually(t, "the copy is synced", c.Synced)
	eventually(t, "no lag", func() bool { return c.Status().LagRecords == 0 })

	st := c.Status()

	if st.State != replication.StateStreaming || !strings.Contains(st.Source, ":***@") {
		t.Fatalf("status = %+v", st)
	}
}

func TestBlobsGoToTheCopy(t *testing.T) {
	owner, cp, _ := pair(t)

	big := strings.Repeat("z", 1<<10)

	owner.must("PUT", "/files", "", `{"blob_threshold":256}`)
	owner.must("POST", "/files/_doc", "", `{"_id":"f1","body":"`+big+`"}`)

	sameData(t, owner.db, cp.db)

	status, data, _ := cp.do("GET", "/files/_doc/f1", "", "")

	if status != http.StatusOK || !strings.Contains(string(data), "$blob") {
		t.Fatalf("get on the copy: %d %s", status, data)
	}

	count, _ := cp.db.Blobs().Stats()

	if count != 1 {
		t.Fatalf("blobs on the copy: %d", count)
	}
}

func TestAWriteOnTheCopyGoesToTheOwnerAndIsReadAfter(t *testing.T) {
	owner, cp, _ := pair(t)

	for i := range 20 {
		id := "w" + string(rune('a'+i))

		status, body, h := cp.do("POST", "/orders/_doc", "", `{"_id":"`+id+`","n":1}`)

		if status != http.StatusCreated && status != http.StatusOK {
			t.Fatalf("insert through the copy: %d %s", status, body)
		}

		if h.Get(replication.StaleHeader) != "" {
			t.Fatal("the answer is stale")
		}

		status, body, _ = cp.do("GET", "/orders/_doc/"+id, "", "")

		if status != http.StatusOK {
			t.Fatalf("read after the write: %d %s", status, body)
		}
	}

	cp.must("PUT", "/orders/_indexes/n", "", "")

	def, _ := cp.db.Definition("orders")

	if len(def.Indexes) != 1 {
		t.Fatalf("the index is not on the copy after the answer: %+v", def)
	}

	if n := len(docs(t, owner.db, "orders")); n != 20 {
		t.Fatalf("owner has %d orders", n)
	}
}

func TestWatchAndStreamsRunOnTheOwner(t *testing.T) {
	owner, cp, _ := pair(t)

	cp.must("POST", "/jobs/_doc", "", `{"_id":"j1"}`)

	status, body, _ := cp.do("POST", "/jobs/_watch", "", `{"consumer":"w","limit":1}`)

	if status != http.StatusOK || !strings.Contains(string(body), "j1") {
		t.Fatalf("watch through the copy: %d %s", status, body)
	}

	cp.must("POST", "/jobs/_ack", "", `{"consumer":"w","pos":"e1.1"}`)

	if _, err := owner.db.Get("jobs"); err != nil {
		t.Fatal(err)
	}

	cp.must("POST", "/events/_streams/append", "", `{"doc":{"a":1}}`)

	if status, body, _ := owner.do("GET", "/events/_streams", "", ""); status != http.StatusOK {
		t.Fatalf("the stream is not on the owner: %d %s", status, body)
	}
}

func TestOwnerDownWritesAre503AndReadsWork(t *testing.T) {
	owner, cp, c := pair(t)

	owner.must("POST", "/users/_doc", "", `{"_id":"u1"}`)

	sameData(t, owner.db, cp.db)
	eventually(t, "synced", c.Synced)

	owner.stop()

	status, body, _ := cp.do("POST", "/users/_doc", "", `{"_id":"u2"}`)

	if status != http.StatusServiceUnavailable || !strings.Contains(string(body), "owner_unavailable") {
		t.Fatalf("write with the owner down: %d %s", status, body)
	}

	status, body, _ = cp.do("GET", "/users/_doc/u1", "", "")

	if status != http.StatusOK {
		t.Fatalf("read with the owner down: %d %s", status, body)
	}

	eventually(t, "the copy is down", func() bool { return c.Status().State == replication.StateDown })
}

func TestACopyThatRestartsGoesOnFromItsPosition(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()
	dir := t.TempDir()
	cp := startNode(t, dir, testKey)
	c := cp.copyOf(owner, "default", "default", replication.Options{})

	cp.attach()

	owner.must("POST", "/a/_doc", "", `{"_id":"1"}`)

	sameData(t, owner.db, cp.db)

	cp.stop()

	owner.must("POST", "/a/_doc", "", `{"_id":"2"}`)
	owner.must("POST", "/b/_doc", "", `{"_id":"3"}`)

	st, ok, err := replication.LoadState(dir)

	if err != nil || !ok || st.Cursors["a"] == 0 || st.Role != replication.RoleCopy {
		t.Fatalf("state = %+v %v %v", st, ok, err)
	}

	again := startNode(t, dir, testKey)

	again.copyOf(owner, "default", "default", replication.Options{})
	again.attach()

	sameData(t, owner.db, again.db)

	_ = c
}

func TestAnOtherDatabaseWithAs(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()

	owner.must("PUT", "/_databases/app", "", "")
	owner.must("POST", "/users/_doc", "app", `{"_id":"u1"}`)

	cp := startNode(t, t.TempDir(), testKey)
	c := cp.copyOf(owner, "app", "mirror", replication.Options{})

	cp.attach()

	cp.must("POST", "/users/_doc", "mirror", `{"_id":"u2"}`)
	cp.must("POST", "/own/_doc", "", `{"_id":"local"}`)

	sameData(t, dbOf(t, owner, "app"), dbOf(t, cp, "mirror"))

	if len(docs(t, owner.db, "own")) != 0 || len(docs(t, cp.db, "own")) != 1 {
		t.Fatal("a collection of the own database of the copy was replicated")
	}

	status, body, _ := cp.do("GET", "/_replication", "", "")

	var out struct {
		Databases []struct {
			Database string `json:"database"`
			Role     string `json:"role"`
			State    string `json:"state"`
		} `json:"databases"`
	}

	if err := json.Unmarshal(body, &out); err != nil || status != http.StatusOK {
		t.Fatalf("status: %d %s", status, body)
	}

	roles := map[string]string{}

	for _, d := range out.Databases {
		roles[d.Database] = d.Role
	}

	if roles["default"] != "owner" || roles["mirror"] != "copy" {
		t.Fatalf("roles = %v (%s)", roles, body)
	}

	_, body, _ = owner.do("GET", "/_replication", "", "")

	if !strings.Contains(string(body), `"copies"`) {
		t.Fatalf("the owner does not show its copy: %s", body)
	}

	_ = c
}

func TestAnotherKeyIsAStartError(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()
	cp := startNode(t, t.TempDir(), otherKey)
	c := cp.copyOf(owner, "default", "default", replication.Options{})

	err := c.First(5 * time.Second)

	if err == nil || !strings.Contains(err.Error(), "encryption key") {
		t.Fatalf("err = %v", err)
	}

	eventually(t, "down", func() bool { return c.Status().State == replication.StateDown })
}

func TestReplicateTokenReadsTheStreamOnly(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()

	owner.must("POST", "/users/_doc", "", `{"_id":"u1"}`)

	for _, path := range []string{"/users/_doc/u1", "/_stats", "/_replication"} {
		req, _ := http.NewRequest("GET", "http://"+owner.addr+path, nil)
		req.Header.Set("Authorization", "Bearer "+replToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()

		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s with the replicate token: %d", path, resp.StatusCode)
		}
	}
}

func TestAPositionThatIsGoneTakesASnapshot(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()

	for i := range 1200 {
		owner.must("POST", "/big/_doc", "", `{"_id":"d`+strconv.Itoa(i)+`"}`)

		if i == 600 {
			break
		}
	}

	dir := t.TempDir()

	if err := replication.SaveState(dir, replication.State{Role: replication.RoleCopy, Cursors: map[string]uint64{"big": 1 << 40}}); err != nil {
		t.Fatal(err)
	}

	cp := startNode(t, dir, testKey)
	c := cp.copyOf(owner, "default", "default", replication.Options{})

	cp.attach()

	sameData(t, owner.db, cp.db)

	owner.must("POST", "/big/_doc", "", `{"_id":"after"}`)

	sameData(t, owner.db, cp.db)

	_ = c
}

func TestPromoteThenTheOldOwnerComesBackAsACopy(t *testing.T) {
	oldDir := t.TempDir()
	owner := startNode(t, oldDir, testKey).attach()
	cp, _, c := func() (*node, *node, *replication.Copy) {
		n := startNode(t, t.TempDir(), testKey)
		c := n.copyOf(owner, "default", "default", replication.Options{})

		n.attach()

		return n, nil, c
	}()

	owner.must("POST", "/users/_doc", "", `{"_id":"u1"}`)
	owner.must("POST", "/users/_doc", "", `{"_id":"u2"}`)

	sameData(t, owner.db, cp.db)

	owner.stop()

	term, err := c.Promote()
	if err != nil || term != 1 {
		t.Fatalf("promote: %d %v", term, err)
	}

	cp.must("POST", "/users/_doc", "", `{"_id":"u3"}`)

	back := startNode(t, oldDir, testKey)
	bc := back.copyOf(cp, "default", "default", replication.Options{})

	if err := bc.First(5 * time.Second); err != nil {
		t.Fatal(err)
	}

	back.attach()

	sameData(t, cp.db, back.db)

	if st := bc.Status(); st.Term != 1 {
		t.Fatalf("term of the old owner = %d", st.Term)
	}

	status, body, _ := cp.do("GET", "/_replication", "", "")

	if status != http.StatusOK || !strings.Contains(string(body), `"term":1`) {
		t.Fatalf("status of the new owner: %s", body)
	}
}

func TestAnOldOwnerWithItsOwnWritesMustResync(t *testing.T) {
	oldDir := t.TempDir()
	owner := startNode(t, oldDir, testKey).attach()
	cp := startNode(t, t.TempDir(), testKey)
	c := cp.copyOf(owner, "default", "default", replication.Options{})

	cp.attach()

	owner.must("POST", "/users/_doc", "", `{"_id":"u1"}`)

	sameData(t, owner.db, cp.db)

	if _, err := c.Promote(); err != nil {
		t.Fatal(err)
	}

	owner.must("POST", "/users/_doc", "", `{"_id":"lost-on-the-old-owner"}`)
	owner.stop()

	back := startNode(t, oldDir, testKey)
	bc := back.copyOf(cp, "default", "default", replication.Options{})

	err := bc.First(5 * time.Second)

	if !errors.Is(err, replication.ErrResync) {
		t.Fatalf("err = %v", err)
	}

	back.attach()

	if err := bc.Resync(); err != nil {
		t.Fatal(err)
	}

	sameData(t, cp.db, back.db)
}

func TestALowerTermIsRefused(t *testing.T) {
	owner := startNode(t, t.TempDir(), testKey).attach()
	dir := t.TempDir()

	if err := replication.SaveState(dir, replication.State{Role: replication.RoleCopy, Term: 3}); err != nil {
		t.Fatal(err)
	}

	cp := startNode(t, dir, testKey)
	c := cp.copyOf(owner, "default", "default", replication.Options{})

	if err := c.First(5 * time.Second); err == nil || !strings.Contains(err.Error(), "lower than term 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestPromoteAndResyncRoutes(t *testing.T) {
	owner, cp, _ := pair(t)

	status, body, _ := cp.do("POST", "/_replication/promote", "", "")

	if status != http.StatusForbidden {
		t.Fatalf("promote over TCP: %d %s", status, body)
	}

	status, _, _ = owner.do("POST", "/_replication/resync", "", "")

	if status != http.StatusForbidden {
		t.Fatalf("resync over TCP: %d", status)
	}
}
