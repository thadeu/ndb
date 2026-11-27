package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

// A backup of a daemon under writes, streamed to stdout and read back from
// stdin (as in ndb backup - | aws s3 cp - s3://... and back), restores every
// write acked before it started, with no gap.
func TestStreamedBackupUnderWritesRestores(t *testing.T) {
	src := t.TempDir()
	sock, _ := startDaemon(t, src)
	client := unixClient(sock)

	const seed = 100

	for i := range seed {
		insertEvent(t, client, i)
	}

	var (
		acked atomic.Int64
		stop  atomic.Bool
		wg    sync.WaitGroup
	)

	acked.Store(seed)
	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := seed; !stop.Load(); i++ {
			if post(client, "/events/_bulk", eventDoc(i)) != http.StatusOK {
				return
			}

			acked.Store(int64(i + 1))
		}
	}()

	before := acked.Load()
	target := filepath.Join(t.TempDir(), "data")
	got := streamBackup(t, []string{"-url", "unix://" + sock, "-"}, target)

	stop.Store(true)
	wg.Wait()

	keys := restoredKeys(t, target)

	if int64(len(keys)) < before {
		t.Fatalf("restored %d events, %d were acked before the backup", len(keys), before)
	}

	for i := range len(keys) {
		if !keys[fmt.Sprintf("e%05d", i)] {
			t.Fatalf("gap: e%05d is missing in a backup of %d events", i, len(keys))
		}
	}

	if got == "" {
		t.Error("restore printed nothing")
	}
}

// streamBackup pipes the backup straight into the restore: no file, no
// buffer of the whole backup.
func streamBackup(t *testing.T, args []string, target string) string {
	t.Helper()

	r, w := io.Pipe()
	done := make(chan error, 1)

	go func() {
		err := backupTo(append([]string{"-config", "none.yml"}, args...), w, io.Discard)

		w.CloseWithError(err)
		done <- err
	}()

	out, err := restoreFrom(t, r, "-", "-data-dir", target)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	if err := <-done; err != nil {
		t.Fatalf("backup: %v", err)
	}

	return out
}

func insertEvent(t *testing.T, client *http.Client, i int) {
	t.Helper()

	if status := post(client, "/events/_bulk", eventDoc(i)); status != http.StatusOK {
		t.Fatalf("insert %d: status %d", i, status)
	}
}

func eventDoc(i int) string {
	return fmt.Sprintf(`{"docs":[{"k":"e%05d","n":%d}]}`, i, i)
}

func restoredKeys(t *testing.T, dir string) map[string]bool {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer db.Close()

	col, err := db.Get("events")
	if err != nil {
		t.Fatal(err)
	}

	page, err := col.Find(context.Background(), []byte(`{"limit":1000000}`))
	if err != nil {
		t.Fatal(err)
	}

	keys := map[string]bool{}

	for _, row := range page.Rows {
		_, rest, _ := strings.Cut(string(row), `"k":"`)
		k, _, _ := strings.Cut(rest, `"`)

		keys[k] = true
	}

	return keys
}
