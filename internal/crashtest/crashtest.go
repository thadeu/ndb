// Package crashtest kills a child process with SIGKILL at random times and
// checks invariants after each kill.
//
// The child is the test binary itself. Register children in TestMain:
//
//	func TestMain(m *testing.M) {
//		crashtest.Children(map[string]func(){"writer": runWriter})
//		os.Exit(m.Run())
//	}
//
// A child reports progress with Ack(n) after the work for n is durable.
package crashtest

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

const envChild = "NDB_CRASHTEST_CHILD"

// Children runs the child named in the environment and exits. It returns
// when the process is the normal test process.
func Children(children map[string]func()) {
	name := os.Getenv(envChild)
	if name == "" {
		return
	}

	fn, ok := children[name]
	if !ok {
		fmt.Fprintln(os.Stderr, "crashtest: unknown child", name)
		os.Exit(2)
	}

	fn()
	os.Exit(0)
}

// Ack tells the parent that the work for n is durable.
func Ack(n int64) {
	fmt.Fprintf(os.Stdout, "ack %d\n", n)
}

type Config struct {
	Child      string
	Env        []string
	Iterations int
	MaxDelay   time.Duration

	// Check runs after each kill with the highest acked value so far.
	Check func(t *testing.T, acked int64)
}

func Run(t *testing.T, cfg Config) {
	t.Helper()

	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 1))

	var acked atomic.Int64

	for i := range cfg.Iterations {
		delay := time.Duration(rng.Int64N(int64(cfg.MaxDelay)))

		if err := runOnce(cfg, delay, &acked); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}

		cfg.Check(t, acked.Load())

		if t.Failed() {
			t.Fatalf("invariant broken after iteration %d (delay %s)", i, delay)
		}
	}
}

func runOnce(cfg Config, delay time.Duration, acked *atomic.Int64) error {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(append(os.Environ(), envChild+"="+cfg.Child), cfg.Env...)
	cmd.Stderr = os.Stderr

	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		sc := bufio.NewScanner(out)

		for sc.Scan() {
			n, ok := parseAck(sc.Text())

			if ok && n > acked.Load() {
				acked.Store(n)
			}
		}
	}()

	time.Sleep(delay)

	_ = cmd.Process.Signal(syscall.SIGKILL)

	<-done

	_ = cmd.Wait()

	return nil
}

func parseAck(line string) (int64, bool) {
	num, ok := strings.CutPrefix(line, "ack ")
	if !ok {
		return 0, false
	}

	n, err := strconv.ParseInt(num, 10, 64)

	return n, err == nil
}
