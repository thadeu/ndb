package ndbredis

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	ndb "github.com/thadeu/ndb/sdk/go"
)

// The two backends of an app have one interface. The check is in a test,
// not in the package: a later go-redis can add a method to the interface,
// and that must not break the build of an app.
var (
	_ redis.StreamCmdable = (*Client)(nil)
	_ redis.StreamCmdable = (*redis.Client)(nil)
)

// backend is one implementation of the stream commands that the cases run
// on.
type backend struct {
	name string
	rdb  redis.StreamCmdable

	// forget deletes a key after the test (Redis only).
	forget func(key string)
}

var (
	// testDB is the handle of the ndb daemon that TestMain starts.
	testDB *ndb.DB

	// backends has the adapter, and a real Redis when NDB_REDIS_ADDR is
	// set.
	backends []backend

	keySeq atomic.Int64
	keyRun = strconv.FormatInt(time.Now().UnixNano(), 36)
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "ndbredis")

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
	defer testDB.Close()

	backends = []backend{{name: "ndb", rdb: New(testDB), forget: func(string) {}}}

	if addr := os.Getenv("NDB_REDIS_ADDR"); addr != "" {
		rc := redis.NewClient(&redis.Options{Addr: addr})

		defer rc.Close()

		if err := rc.Ping(context.Background()).Err(); err != nil {
			fmt.Fprintln(os.Stderr, "redis:", err)

			return 1
		}

		// Only the keys of the tests are deleted: never the database.
		forget := func(key string) { rc.Del(context.Background(), key) }

		backends = append(backends, backend{name: "redis", rdb: rc, forget: forget})
	}

	return m.Run()
}

// startDaemon builds cmd/ndb from the repo root and starts it on a unix
// socket, with no TCP port. The socket path stays short: macOS allows 104
// bytes.
func startDaemon(dir string) (func(), error) {
	bin := filepath.Join(dir, "ndb")

	build := exec.Command("go", "build", "-o", bin, "./cmd/ndb")
	build.Dir = "../../.."
	build.Stderr = os.Stderr

	if err := build.Run(); err != nil {
		return nil, fmt.Errorf("build: %w", err)
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
		"-tcp", "",
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

	if testDB, err = ndb.Connect("unix://" + sock); err == nil {
		err = waitReady(testDB)
	}

	if err != nil {
		stop()

		logs, _ := os.ReadFile(logPath)

		return nil, fmt.Errorf("%w\n%s", err, logs)
	}

	return stop, nil
}

// waitReady polls the health of the daemon until it answers.
func waitReady(db *ndb.DB) error {
	var last error

	for range 200 {
		if last = db.Health(context.Background()); last == nil {
			return nil
		}

		time.Sleep(50 * time.Millisecond)
	}

	return last
}

// newKey returns a stream name that no other test uses, in this run and in
// the runs before it. It is a valid ndb collection name.
func newKey() string {
	return fmt.Sprintf("ndbredis_%s_%d", keyRun, keySeq.Add(1))
}

// each runs one case on every backend, with the same assertions. The case
// gets a context and a new key.
func each(t *testing.T, test func(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string)) {
	t.Helper()

	for _, b := range backends {
		t.Run(b.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			key := newKey()

			t.Cleanup(cancel)
			t.Cleanup(func() { b.forget(key) })

			test(t, ctx, b.rdb, key)
		})
	}
}

// adapter gives the ndb backend, a context and a new key to a case that is
// only about the adapter.
func adapter(t *testing.T) (*Client, context.Context, string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)

	t.Cleanup(cancel)

	return New(testDB), ctx, newKey()
}

// add appends n entries with the fields n and name, and returns their IDs.
func add(t *testing.T, ctx context.Context, rdb redis.StreamCmdable, key string, n int) []string {
	t.Helper()

	ids := []string{}

	for i := 1; i <= n; i++ {
		id, err := rdb.XAdd(ctx, &redis.XAddArgs{Stream: key, Values: map[string]any{"n": i, "name": "e" + strconv.Itoa(i)}}).Result()

		if err != nil {
			t.Fatal(err)
		}

		ids = append(ids, id)
	}

	return ids
}

// ids gives the IDs of messages, in order.
func ids(msgs []redis.XMessage) []string {
	out := []string{}

	for _, m := range msgs {
		out = append(out, m.ID)
	}

	return out
}
