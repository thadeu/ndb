package ndb

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const notReadyBody = `{"error":{"code":"not_ready","message":"ndb is recovering"}}`

func TestRetryWaitsForDaemonToStart(t *testing.T) {
	dir, err := os.MkdirTemp("", "ndbr")
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sock := filepath.Join(dir, "ndb.sock")
	db := openTest(t, "unix://"+sock)

	started := make(chan *exec.Cmd, 1)

	go func() {
		time.Sleep(300 * time.Millisecond)

		cmd := exec.Command(daemon.bin,
			"-config", filepath.Join(dir, "absent.yml"),
			"-data-dir", filepath.Join(dir, "data"),
			"-unix", sock,
			"-tcp", "127.0.0.1:0",
		)

		_ = cmd.Start()

		started <- cmd
	}()

	t.Cleanup(func() {
		cmd := <-started

		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	resp, err := db.do(context.Background(), http.MethodGet, "/_health", nil)
	if err != nil {
		t.Fatalf("want success after the daemon starts: %v", err)
	}

	discard(resp)
}

func TestRetryNotReadyThenOK(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(notReadyBody))

			return
		}

		_, _ = w.Write([]byte(`{}`))
	}))

	t.Cleanup(srv.Close)

	db := openTest(t, "tcp://"+srv.Listener.Addr().String())

	resp, err := db.do(context.Background(), http.MethodPost, "/v1/c/x/insert", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}

	discard(resp)

	if got := calls.Load(); got != 3 {
		t.Fatalf("calls: got %d, want 3", got)
	}
}

func TestRetryDeadlineKeepsErrorClass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(notReadyBody))
	}))

	t.Cleanup(srv.Close)

	db := openTest(t, "tcp://"+srv.Listener.Addr().String(), WithReadyTimeout(200*time.Millisecond))

	_, err := db.do(context.Background(), http.MethodGet, "/_health", nil)
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("want ErrNotReady, got %v", err)
	}
}

func TestRetryDisabledAndCancelled(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(notReadyBody))
	}))

	t.Cleanup(srv.Close)

	off := openTest(t, "tcp://"+srv.Listener.Addr().String(), WithReadyTimeout(0))

	if _, err := off.do(context.Background(), http.MethodGet, "/_health", nil); !errors.Is(err, ErrNotReady) {
		t.Fatalf("want ErrNotReady, got %v", err)
	}

	if calls.Load() != 1 {
		t.Fatalf("disabled retry: got %d calls, want 1", calls.Load())
	}

	on := openTest(t, "tcp://"+srv.Listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)

	defer cancel()

	start := time.Now()
	_, err := on.do(ctx, http.MethodGet, "/_health", nil)

	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("want a prompt error, got %v after %v", err, time.Since(start))
	}
}

// dropServer accepts connections, reads the request and closes without
// an answer. It counts the connections.
func dropServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	var conns atomic.Int32

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}

			conns.Add(1)

			buf := make([]byte, 4096)

			_, _ = c.Read(buf)
			_ = c.Close()
		}
	}()

	t.Cleanup(func() { _ = ln.Close() })

	return "tcp://" + ln.Addr().String(), &conns
}

func TestRetryNeverRepeatsWriteDroppedAfterSend(t *testing.T) {
	addr, conns := dropServer(t)
	db := openTest(t, addr, WithReadyTimeout(time.Second))

	_, err := db.do(context.Background(), http.MethodPost, "/v1/c/x/insert", []byte(`{}`))
	if err == nil {
		t.Fatal("want an error")
	}

	if got := conns.Load(); got != 1 {
		t.Fatalf("write sent %d times, want 1", got)
	}
}

func TestRetryRepeatsReadDroppedAfterSend(t *testing.T) {
	addr, conns := dropServer(t)
	db := openTest(t, addr, WithReadyTimeout(400*time.Millisecond))

	_, err := db.do(context.Background(), http.MethodPost, "/x/_search", []byte(`{}`))
	if err == nil {
		t.Fatal("want an error")
	}

	if got := conns.Load(); got < 2 {
		t.Fatalf("read sent %d times, want a retry", got)
	}
}
