package streams

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

// clock is a fake time for the service: it moves only when a test moves it.
type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.at
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

// testService opens an engine on dir and a service on it; stop closes both.
func testService(t *testing.T, dir string, now func() time.Time) (*Service, func()) {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err != nil {
		t.Fatal(err)
	}

	s := New(func() *engine.DB { return db })

	if now != nil {
		s.now = now
	}

	return s, func() {
		s.Close()
		_ = db.Close()
	}
}

// fill makes stream jobs with n entries and group g at 0, and reads them
// all as consumer c1.
func fill(t *testing.T, s *Service, n int) []string {
	t.Helper()

	ctx := context.Background()
	docs := make([]map[string]any, n)

	for i := range docs {
		docs[i] = map[string]any{"n": i}
	}

	ids, err := s.Append(ctx, "jobs", docs, AppendOptions{})

	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateGroup(ctx, "jobs", "g", "0", nil, false); err != nil {
		t.Fatal(err)
	}

	rows, err := s.Read(ctx, "jobs", "g", ReadOptions{Consumer: "c1", Count: n})

	if err != nil || len(rows) != n {
		t.Fatalf("read %d rows, %v", len(rows), err)
	}

	out := make([]string, n)

	for i, id := range ids {
		out[i] = id.String()
	}

	return out
}

func pendingRows(t *testing.T, s *Service, o PendingOptions) []PendingEntry {
	t.Helper()

	if o.Count == 0 {
		o.Count = 100
	}

	v, err := s.Pending(context.Background(), "jobs", "g", o)

	if err != nil {
		t.Fatal(err)
	}

	return v.([]PendingEntry)
}

// claimedIDs runs a claim and gives the IDs it took (JustIDs).
func claimedIDs(t *testing.T, s *Service, o ClaimOptions) []string {
	t.Helper()

	o.JustIDs = true

	res, err := s.Claim(context.Background(), "jobs", "g", o)

	if err != nil {
		t.Fatal(err)
	}

	out := []string{}

	for _, raw := range res.Entries {
		var id string

		_ = json.Unmarshal(raw, &id)
		out = append(out, id)
	}

	return out
}

// No claim and no pending read gives a nacked entry before its retry time;
// at the retry time it is due at once, whatever min_idle_ms, and the
// delivery count goes on.
func TestNackNeverDeliversBeforeRetryAt(t *testing.T) {
	clk := &clock{at: time.UnixMilli(1_800_000_000_000)}
	s, stop := testService(t, t.TempDir(), clk.now)

	defer stop()

	ctx := context.Background()
	ids := fill(t, s, 2)

	n, err := s.Nack(ctx, "jobs", "g", NackOptions{Consumer: "c1", IDs: ids[:1], DelayMs: 10_000})

	if err != nil || n != 1 {
		t.Fatalf("nack: %d, %v", n, err)
	}

	clk.add(9_999 * time.Millisecond)

	tries := []ClaimOptions{
		{Consumer: "c2", IDs: ids[:1]},
		{Consumer: "c2", IDs: ids[:1], Force: true},
		{Consumer: "c2", Cursor: "0-0", Count: 100},
		{Consumer: "c2", Due: true},
	}

	for i, o := range tries {
		if got := claimedIDs(t, s, o); len(got) != 0 && got[0] == ids[0] {
			t.Fatalf("claim %d took the nacked entry 1 ms before its retry time: %v", i, got)
		}
	}

	rows, err := s.Read(ctx, "jobs", "g", ReadOptions{Consumer: "c1", Pending: true})

	if err != nil || len(rows) != 0 {
		t.Fatalf("a pending read of c1 gave %d rows (%v); the nacked entry is no one's, and ids[1] went to c2", len(rows), err)
	}

	sum, _ := s.Pending(ctx, "jobs", "g", PendingOptions{})

	if w := sum.(PendingSummary).Waiting; w != 1 {
		t.Fatalf("waiting %d, want 1", w)
	}

	clk.add(time.Millisecond)

	if got := claimedIDs(t, s, ClaimOptions{Consumer: "c3", Due: true, MinIdleMs: 3_600_000}); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("due claim at the retry time took %v, want %s", got, ids[0])
	}

	row := pendingRows(t, s, PendingOptions{Start: ids[0], End: ids[0]})

	if len(row) != 1 || row[0].Consumer != "c3" || row[0].RetryAt != nil {
		t.Fatalf("after the claim: %+v, want c3 and no retry_at", row)
	}

	res, _ := s.Claim(ctx, "jobs", "g", ClaimOptions{Consumer: "c4", IDs: ids[:1], MinIdleMs: 1000})

	if len(res.Entries) != 0 {
		t.Fatal("a claim cleared RetryAt, so min_idle_ms counts again: the entry is not idle")
	}
}

// A nack keeps the delivery count; the claim that takes the entry again
// adds 1.
func TestNackKeepsTheDeliveryCount(t *testing.T) {
	clk := &clock{at: time.UnixMilli(1_800_000_000_000)}
	s, stop := testService(t, t.TempDir(), clk.now)

	defer stop()

	ctx := context.Background()
	ids := fill(t, s, 1)

	for want := int64(1); want <= 3; want++ {
		if _, err := s.Nack(ctx, "jobs", "g", NackOptions{Consumer: "c1", IDs: ids, DelayMs: 1000}); err != nil {
			t.Fatal(err)
		}

		row := pendingRows(t, s, PendingOptions{})

		if row[0].Deliveries != want || row[0].Consumer != "" || row[0].RetryAt == nil || *row[0].RetryAt != clk.now().UnixMilli()+1000 {
			t.Fatalf("after nack %d: %+v", want, row[0])
		}

		clk.add(time.Second)

		res, err := s.Claim(ctx, "jobs", "g", ClaimOptions{Consumer: "c1", Due: true})

		if err != nil || len(res.Entries) != 1 {
			t.Fatalf("due claim %d: %d entries, %v", want, len(res.Entries), err)
		}
	}
}

// A nack survives a restart: the reopened service still has the entry with
// no owner and its retry time; once the time passes (as after a restore
// later on), it is due at once.
func TestNackSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{at: time.UnixMilli(1_800_000_000_000)}
	s, stop := testService(t, dir, clk.now)

	ids := fill(t, s, 3)

	if n, err := s.Nack(context.Background(), "jobs", "g", NackOptions{Consumer: "c1", IDs: ids[1:], DelayMs: 60_000}); err != nil || n != 2 {
		t.Fatalf("nack: %d, %v", n, err)
	}

	stop()

	s, stop = testService(t, dir, clk.now)

	defer stop()

	rows := pendingRows(t, s, PendingOptions{Due: new(false)})

	if len(rows) != 2 || rows[0].Consumer != "" || rows[0].RetryAt == nil {
		t.Fatalf("waiting after restart: %+v", rows)
	}

	if got := claimedIDs(t, s, ClaimOptions{Consumer: "c2", Cursor: "0-0", Count: 100}); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("the scan after restart took %v, want only %s", got, ids[0])
	}

	clk.add(time.Hour)

	if got := claimedIDs(t, s, ClaimOptions{Consumer: "c2", Due: true}); len(got) != 2 {
		t.Fatalf("due claim an hour later took %v, want both nacked entries", got)
	}
}

// A PEL written before RetryAt (no "r" key, no retry bucket) opens and
// works: its entries are not nacked, a nack makes the bucket.
func TestOldPELRecordsOpen(t *testing.T) {
	dir := t.TempDir()
	clk := &clock{at: time.UnixMilli(1_800_000_000_000)}
	s, stop := testService(t, dir, clk.now)

	ids := fill(t, s, 2)

	db, _ := s.db().Get("jobs")
	path := filepath.Join(db.Dir(), StoreFile)

	stop()

	store, err := bolt.Open(path, 0o644, nil)

	if err != nil {
		t.Fatal(err)
	}

	err = store.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bGroups).Bucket([]byte("g"))

		if err := b.DeleteBucket(bRetry); err != nil {
			return err
		}

		pel := b.Bucket(bPEL)
		old := `{"c":"c1","t":` + strconv.FormatInt(clk.now().UnixMilli(), 10) + `,"n":4}`

		return pel.ForEach(func(k, _ []byte) error {
			return pel.Put(k, []byte(old))
		})
	})

	store.Close()

	if err != nil {
		t.Fatal(err)
	}

	s, stop = testService(t, dir, clk.now)

	defer stop()

	rows := pendingRows(t, s, PendingOptions{})

	if len(rows) != 2 || rows[0].Deliveries != 4 || rows[0].RetryAt != nil || rows[0].Consumer != "c1" {
		t.Fatalf("old records: %+v", rows)
	}

	if got := claimedIDs(t, s, ClaimOptions{Consumer: "c2", Due: true}); len(got) != 0 {
		t.Fatalf("due claim with no retry bucket took %v", got)
	}

	if n, err := s.Nack(context.Background(), "jobs", "g", NackOptions{Consumer: "c1", IDs: ids[:1]}); err != nil || n != 1 {
		t.Fatalf("nack on an old store: %d, %v", n, err)
	}

	if got := claimedIDs(t, s, ClaimOptions{Consumer: "c2", Due: true}); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("due claim took %v, want %s", got, ids[0])
	}
}

// Workers that read, nack and claim due entries at once (run with -race):
// each entry is acked once at the end, none is lost, and no entry comes
// back before the retry time of its last nack.
func TestNackConcurrentWorkers(t *testing.T) {
	s, stop := testService(t, t.TempDir(), nil)

	defer stop()

	ctx := context.Background()
	total := 200
	docs := make([]map[string]any, total)

	for i := range docs {
		docs[i] = map[string]any{"n": i}
	}

	if _, err := s.Append(ctx, "jobs", docs, AppendOptions{}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateGroup(ctx, "jobs", "g", "0", nil, false); err != nil {
		t.Fatal(err)
	}

	const delay = 20 * time.Millisecond

	var (
		mu      sync.Mutex
		dueAt   = map[string]time.Time{}
		tries   = map[string]int{}
		acked   = map[string]int{}
		early   atomic.Int64
		pending atomic.Int64
	)

	pending.Store(int64(total))

	// handle nacks an entry on its first two tries and acks it on the third.
	handle := func(consumer string, raw []json.RawMessage) {
		for _, r := range raw {
			var row struct {
				ID string `json:"_id"`
			}

			_ = json.Unmarshal(r, &row)
			id, _ := FromKey(row.ID)
			text := id.String()

			mu.Lock()

			if at, ok := dueAt[text]; ok && time.Now().Before(at) {
				early.Add(1)
			}

			tries[text]++
			try := tries[text]

			if try < 3 {
				dueAt[text] = time.Now().Add(delay)
			}

			mu.Unlock()

			if try < 3 {
				if _, err := s.Nack(ctx, "jobs", "g", NackOptions{Consumer: consumer, IDs: []string{text}, DelayMs: delay.Milliseconds()}); err != nil {
					t.Error(err)
				}

				continue
			}

			n, err := s.Ack(ctx, "jobs", "g", []string{text})

			if err != nil {
				t.Error(err)
			}

			mu.Lock()
			acked[text] += n
			mu.Unlock()

			pending.Add(-int64(n))
		}
	}

	var wg sync.WaitGroup

	deadline := time.Now().Add(20 * time.Second)

	for w := range 8 {
		consumer := "w" + strconv.Itoa(w)

		wg.Go(func() {
			for pending.Load() > 0 && time.Now().Before(deadline) {
				rows, err := s.Read(ctx, "jobs", "g", ReadOptions{Consumer: consumer, Count: 10})

				if err != nil {
					t.Error(err)

					return
				}

				handle(consumer, rows)

				res, err := s.Claim(ctx, "jobs", "g", ClaimOptions{Consumer: consumer, Due: true, Count: 10})

				if err != nil {
					t.Error(err)

					return
				}

				handle(consumer, res.Entries)

				if len(rows) == 0 && len(res.Entries) == 0 {
					time.Sleep(2 * time.Millisecond)
				}
			}
		})
	}

	wg.Wait()

	if n := early.Load(); n != 0 {
		t.Fatalf("%d deliveries came before their retry time", n)
	}

	if len(acked) != total {
		t.Fatalf("acked %d entries, want %d", len(acked), total)
	}

	for id, n := range acked {
		if n != 1 || tries[id] != 3 {
			t.Fatalf("%s: acked %d times after %d tries", id, n, tries[id])
		}
	}

	if rows := pendingRows(t, s, PendingOptions{}); len(rows) != 0 {
		t.Fatalf("%d entries left in the PEL", len(rows))
	}
}
