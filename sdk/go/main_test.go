package ndb

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// daemon holds the addresses of the ndb daemon that TestMain starts. Every
// test shares it; each test uses its own collections.
var daemon struct {
	unix string
	tcp  string
}

var collSeq atomic.Int64

func TestMain(m *testing.M) {
	os.Exit(runWithDaemon(m))
}

func runWithDaemon(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ndbsdk")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)

		return 1
	}

	defer os.RemoveAll(dir)

	stop, err := startDaemon(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon:", err)

		return 1
	}

	defer stop()

	return m.Run()
}

// startDaemon builds cmd/ndb from the repo root and starts it on a unix
// socket and a TCP port. The socket path stays short: macOS allows 104 bytes.
func startDaemon(dir string) (func(), error) {
	bin := filepath.Join(dir, "ndb")

	build := exec.Command("go", "build", "-o", bin, "./cmd/ndb")
	build.Dir = "../.."
	build.Stderr = os.Stderr

	if err := build.Run(); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	tcpAddr, err := freeAddr()
	if err != nil {
		return nil, err
	}

	logPath := filepath.Join(dir, "daemon.log")

	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}

	sock := filepath.Join(dir, "ndb.sock")
	cmd := exec.Command(bin,
		"-config", filepath.Join(dir, "absent.yml"),
		"-data-dir", filepath.Join(dir, "data"),
		"-unix", sock,
		"-tcp", tcpAddr,
	)

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		_ = logFile.Close()
	}

	daemon.unix = "unix://" + sock
	daemon.tcp = "tcp://" + tcpAddr

	if err := waitReady(daemon.unix); err != nil {
		stop()

		logs, _ := os.ReadFile(logPath)

		return nil, fmt.Errorf("%w\n%s", err, logs)
	}

	return stop, nil
}

// freeAddr returns a loopback TCP address that is free right now.
func freeAddr() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}

	addr := ln.Addr().String()

	return addr, ln.Close()
}

// waitReady polls /health until the daemon has recovered.
func waitReady(addr string) error {
	db, err := Open(addr)
	if err != nil {
		return err
	}

	defer db.Close()

	var last error

	for range 200 {
		resp, err := db.do(context.Background(), http.MethodGet, "/health", nil)
		if err == nil {
			discard(resp)

			return nil
		}

		last = err

		time.Sleep(50 * time.Millisecond)
	}

	return last
}

func openTest(t *testing.T, addr string) *DB {
	t.Helper()

	db, err := Open(addr)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	return db
}

// newColl returns a collection name that no other test in this process uses.
func newColl() string {
	return fmt.Sprintf("c%d", collSeq.Add(1))
}

// rawPut sends ensure_collection with a catalog JSON body.
func rawPut(t *testing.T, db *DB, name, catalog string) {
	t.Helper()

	resp, err := db.do(context.Background(), http.MethodPut, collPath(name), []byte(catalog))
	if err != nil {
		t.Fatal(err)
	}

	discard(resp)
}

// mustInsert inserts a JSON array of documents.
func mustInsert(t *testing.T, db *DB, name, docs string) {
	t.Helper()

	body := []byte(`{"insert":` + docs + `}`)

	resp, err := db.do(context.Background(), http.MethodPost, collPath(name)+"/insert", body)
	if err != nil {
		t.Fatal(err)
	}

	discard(resp)
}
