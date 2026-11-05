package crashtest_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/crashtest"
)

var iterations = flag.Int("crash.iterations", 200, "kills per crash test")

func TestMain(m *testing.M) {
	crashtest.Children(map[string]func(){"writer": demoWriter})
	os.Exit(m.Run())
}

// demoWriter appends "<seq>\n" lines with fsync and acks each one. On start it
// truncates a cut last line in place (never a rewrite: a kill between
// truncate-to-zero and write would lose acked lines) and continues.
func demoWriter() {
	path := os.Getenv("DEMO_PATH")
	lines := readLines(path)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		panic(err)
	}

	if err := f.Truncate(int64(len(joinLines(lines)))); err != nil {
		panic(err)
	}

	for seq := int64(len(lines)) + 1; ; seq++ {
		if _, err := f.WriteString(strconv.FormatInt(seq, 10) + "\n"); err != nil {
			panic(err)
		}

		if err := f.Sync(); err != nil {
			panic(err)
		}

		crashtest.Ack(seq)
	}
}

func readLines(path string) [][]byte {
	data, _ := os.ReadFile(path)
	parts := bytes.Split(data, []byte("\n"))

	return parts[:len(parts)-1]
}

func joinLines(lines [][]byte) []byte {
	var buf bytes.Buffer

	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}

	return buf.Bytes()
}

func TestDemoWriterSurvivesKills(t *testing.T) {
	path := filepath.Join(t.TempDir(), "demo.ndjson")

	crashtest.Run(t, crashtest.Config{
		Child:      "writer",
		Env:        []string{"DEMO_PATH=" + path},
		Iterations: *iterations,
		MaxDelay:   20 * time.Millisecond,
		Check: func(t *testing.T, acked int64) {
			lines := readLines(path)

			if int64(len(lines)) < acked {
				t.Errorf("acked %d but only %d full lines", acked, len(lines))
			}

			for i, l := range lines {
				if string(l) != strconv.Itoa(i+1) {
					t.Errorf("line %d is %q", i+1, l)

					return
				}
			}
		},
	})
}
