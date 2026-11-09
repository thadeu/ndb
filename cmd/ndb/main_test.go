package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/crashtest"
	"github.com/thadeu/ndb/internal/engine"
)

var iterations = flag.Int("crash.iterations", 50, "kills per crash test")

const childEnv = "NDB_CRASHTEST_CHILD"

func TestMain(m *testing.M) {
	crashtest.Children(map[string]func(){
		"daemon": runDaemonChild,
		"writer": runWriterChild,
	})

	os.Exit(m.Run())
}

// runDaemonChild is the daemon process of the SIGTERM test.
func runDaemonChild() {
	if err := run(childArgs()); err != nil {
		panic(err)
	}
}

// runWriterChild runs the daemon and writes docs forever, acking each one
// after the HTTP answer. The parent kills it with SIGKILL.
func runWriterChild() {
	go func() {
		if err := run(childArgs()); err != nil {
			panic(err)
		}
	}()

	client := unixClient(os.Getenv("NDB_SOCK"))

	waitHealthy(client)

	put(client, "/log", `{"id":"k"}`)

	// Numbers continue from the rows on disk, so n is the global write count:
	// after a restart, rows >= the highest acked n.
	for n := int64(rowCount(client)) + 1; ; n++ {
		body := fmt.Sprintf(`{"docs":[{"k":"w%d","n":%d}]}`, n, n)

		status := post(client, "/log/_bulk", body)

		if status != http.StatusOK {
			panic(fmt.Sprintf("insert %d: status %d", n, status))
		}

		crashtest.Ack(n)
	}
}

func childArgs() []string {
	args := []string{
		"-config", filepath.Join(os.Getenv("NDB_DATA"), "absent.yml"),
		"-data-dir", os.Getenv("NDB_DATA"),
		"-unix", os.Getenv("NDB_SOCK"),
	}

	// -tcp is always set: the default of a user install listens on
	// 127.0.0.1:7070, which a dev daemon may hold.
	args = append(args, "-tcp", os.Getenv("NDB_TCP"))

	if file := os.Getenv("NDB_TOKEN_FILE"); file != "" {
		args = append(args, "-token-file", file)
	}

	return args
}

// sockPath is a short unix socket path: macOS caps the length at 104 bytes.
func sockPath(tb testing.TB) string {
	tb.Helper()

	dir, err := os.MkdirTemp("", "ndbs")
	if err != nil {
		tb.Fatal(err)
	}

	tb.Cleanup(func() { os.RemoveAll(dir) })

	return filepath.Join(dir, "ndb.sock")
}

func unixClient(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer

			return d.DialContext(ctx, "unix", sock)
		},
	}}
}

func waitHealthy(client *http.Client) {
	deadline := time.Now().Add(10 * time.Second)

	for time.Now().Before(deadline) {
		resp, err := client.Get("http://ndb/_health")
		if err == nil {
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				return
			}
		}

		time.Sleep(10 * time.Millisecond)
	}

	panic("daemon never became healthy")
}

func put(client *http.Client, path, body string) {
	req, _ := http.NewRequest("PUT", "http://ndb"+path, bytes.NewBufferString(body))

	resp, err := client.Do(req)
	if err != nil {
		panic(err)
	}

	resp.Body.Close()
}

func rowCount(client *http.Client) int {
	resp, err := client.Get("http://ndb/log/_stats")
	if err != nil {
		panic(err)
	}

	defer resp.Body.Close()

	var st struct {
		Rows int `json:"rows"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		panic(err)
	}

	return st.Rows
}

func post(client *http.Client, path, body string) int {
	resp, err := client.Post("http://ndb"+path, "application/json", bytes.NewBufferString(body))
	if err != nil {
		panic(err)
	}

	defer resp.Body.Close()

	_, _ = io.Copy(io.Discard, resp.Body)

	return resp.StatusCode
}

// TestDaemonStopsGracefullyOnSIGTERM starts the daemon as a child process,
// writes through the socket, sends SIGTERM and checks that every acked write
// is on disk and the socket file is gone.
func TestDaemonStopsGracefullyOnSIGTERM(t *testing.T) {
	dir := t.TempDir()
	sock := sockPath(t)

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"=daemon", "NDB_DATA="+dir, "NDB_SOCK="+sock)
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	client := unixClient(sock)

	waitHealthy(client)

	put(client, "/log", `{"id":"k"}`)

	const n = 200

	for i := range n {
		body := fmt.Sprintf(`{"docs":[{"k":"d%d"}]}`, i)

		if status := post(client, "/log/_bulk", body); status != http.StatusOK {
			t.Fatalf("insert %d: status %d", i, status)
		}
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("daemon did not exit cleanly: %v", err)
	}

	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket file left behind: %v", err)
	}

	if got := rowsOn(t, dir, "log"); got != n {
		t.Fatalf("rows after graceful stop = %d, want %d", got, n)
	}
}

// TestDaemonRecoversAfterSIGKILL kills the writer at random times. After each
// kill, a restart must hold every write that was acked.
func TestDaemonRecoversAfterSIGKILL(t *testing.T) {
	dir := t.TempDir()
	sock := sockPath(t)

	crashtest.Run(t, crashtest.Config{
		Child:      "writer",
		Env:        []string{"NDB_DATA=" + dir, "NDB_SOCK=" + sock},
		Iterations: *iterations,
		MaxDelay:   300 * time.Millisecond,
		Check: func(t *testing.T, acked int64) {
			if got := int64(rowsOn(t, dir, "log")); got < acked {
				t.Errorf("acked %d writes, found %d after restart", acked, got)
			}
		},
	})
}

// rowsOn opens the data dir (recovery runs) and counts the rows of one
// collection. It closes the engine again so the next start gets the lock.
func rowsOn(t *testing.T, dir, name string) int {
	t.Helper()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.Default())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	defer db.Close()

	col, err := db.Get(name)

	// The kill can land before the child created the collection.
	if errors.Is(err, catalog.ErrUnknown) {
		return 0
	}

	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}

	return col.Stats().Rows
}

// TestBackupOnlineAndOffline backs up a running daemon through its socket,
// stops it, backs up again from the files, and opens both copies.
func TestBackupOnlineAndOffline(t *testing.T) {
	dir := t.TempDir()
	sock := sockPath(t)
	absent := filepath.Join(dir, "absent.yml")

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"=daemon", "NDB_DATA="+dir, "NDB_SOCK="+sock)
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	client := unixClient(sock)

	waitHealthy(client)

	put(client, "/log", `{"id":"k"}`)

	const n = 50

	for i := range n {
		if status := post(client, "/log/_bulk", fmt.Sprintf(`{"docs":[{"k":"d%d"}]}`, i)); status != http.StatusOK {
			t.Fatalf("insert %d: status %d", i, status)
		}
	}

	online := filepath.Join(t.TempDir(), "online.ndb")

	if err := run([]string{"backup", "-config", absent, "-url", "unix://" + sock, online}); err != nil {
		t.Fatalf("online backup: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	offline := filepath.Join(t.TempDir(), "offline.ndb")

	if err := run([]string{"backup", "-config", absent, "-data-dir", dir, offline}); err != nil {
		t.Fatalf("offline backup: %v", err)
	}

	for name, file := range map[string]string{"online": online, "offline": offline} {
		target := filepath.Join(t.TempDir(), "data")

		if err := run([]string{"restore", "-config", absent, "-data-dir", target, file}); err != nil {
			t.Fatalf("%s restore: %v", name, err)
		}

		if got := rowsOn(t, target, "log"); got != n {
			t.Errorf("%s copy has %d rows, want %d", name, got, n)
		}
	}

	if err := run([]string{"backup", "-config", absent, "-data-dir", dir}); err == nil {
		t.Error("backup without a dest must fail")
	}
}

func TestReadTokenFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	empty := filepath.Join(dir, "empty")
	blank := filepath.Join(dir, "blank")

	os.WriteFile(good, []byte("  abc123\n"), 0o600)
	os.WriteFile(empty, nil, 0o600)
	os.WriteFile(blank, []byte(" \n"), 0o600)

	tok, err := readTokenFile(good)
	if err != nil || tok != "abc123" {
		t.Fatalf("good: %q %v", tok, err)
	}

	for _, p := range []string{empty, blank, filepath.Join(dir, "absent")} {
		if _, err := readTokenFile(p); err == nil {
			t.Fatalf("%s: want an error", p)
		}
	}
}

// The daemon with -token-file asks for the token on TCP only.
func TestDaemonTokenFileOnTCP(t *testing.T) {
	dir := t.TempDir()
	sock := sockPath(t)
	tokenFile := filepath.Join(dir, "token")

	os.WriteFile(tokenFile, []byte("s3cret\n"), 0o600)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := ln.Addr().String()

	ln.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), childEnv+"=daemon", "NDB_DATA="+dir, "NDB_SOCK="+sock, "NDB_TCP="+addr, "NDB_TOKEN_FILE="+tokenFile)
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		cmd.Wait()
	})

	waitHealthy(unixClient(sock))

	status := func(auth string) int {
		req, _ := http.NewRequest("GET", "http://"+addr+"/_collections", nil)

		if auth != "" {
			req.Header.Set("Authorization", auth)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}

		resp.Body.Close()

		return resp.StatusCode
	}

	if got := status(""); got != 401 {
		t.Fatalf("no token: %d", got)
	}

	if got := status("Bearer s3cret"); got != 200 {
		t.Fatalf("token: %d", got)
	}

	if got := post(unixClient(sock), "/x/_search", `{}`); got == 401 {
		t.Fatal("unix socket asked for a token")
	}
}
