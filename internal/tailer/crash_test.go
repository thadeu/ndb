package tailer_test

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/crashtest"
	"github.com/thadeu/ndb/internal/tailer"
)

var iterations = flag.Int("crash.iterations", 200, "kills per crash test")

const (
	crashLines = 30000
	crashLine  = len(`{"_id":"000000"}`) + 1
)

func TestMain(m *testing.M) {
	crashtest.Children(map[string]func(){"tail": tailChild})
	os.Exit(m.Run())
}

func crashDef(data string) catalog.Def {
	return catalog.Def{Name: "calls", Write: catalog.Direct, Path: filepath.Join(data, "*.ndjson")}
}

// outSink stands in for a durable store: one write() per applied line.
type outSink struct{ f *os.File }

func (o outSink) Apply(_ string, _ int64, l []byte) error {
	_, err := o.f.Write(append(append([]byte(nil), l...), '\n'))

	return err
}

func tailChild() {
	out, err := os.OpenFile(os.Getenv("OUT"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		panic(err)
	}

	tl, err := tailer.Open(tailer.Config{
		Dir:        os.Getenv("COL_DIR"),
		Def:        crashDef(os.Getenv("DATA_DIR")),
		Sink:       outSink{out},
		BatchLines: 50,
	})
	if err != nil {
		panic(err)
	}

	for {
		if err := tl.Sync(context.Background()); err != nil {
			panic(err)
		}

		time.Sleep(time.Millisecond)
	}
}

func readIDs(t *testing.T, path string) map[string]bool {
	t.Helper()

	seen := map[string]bool{}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return seen
	}

	if err != nil {
		t.Fatal(err)
	}

	defer f.Close()

	sc := bufio.NewScanner(f)

	for sc.Scan() {
		seen[sc.Text()] = true
	}

	return seen
}

func TestKillDuringIngestionLosesNothing(t *testing.T) {
	data, col := t.TempDir(), t.TempDir()
	out := filepath.Join(t.TempDir(), "out.ndjson")

	f, err := os.Create(filepath.Join(data, "a.ndjson"))
	if err != nil {
		t.Fatal(err)
	}

	w := bufio.NewWriter(f)

	for i := range crashLines {
		fmt.Fprintf(w, "{\"id\":\"%06d\"}\n", i)
	}

	w.Flush()
	f.Close()

	crashtest.Run(t, crashtest.Config{
		Child:      "tail",
		Env:        []string{"DATA_DIR=" + data, "COL_DIR=" + col, "OUT=" + out},
		Iterations: *iterations,
		MaxDelay:   40 * time.Millisecond,
		Check: func(t *testing.T, _ int64) {
			cur, err := tailer.LoadCursors(col)
			if err != nil {
				t.Errorf("meta.bolt after kill: %v", err)

				return
			}

			seen := readIDs(t, out)

			for _, c := range cur {
				for i := range int(c.Offset) / crashLine {
					if !seen[fmt.Sprintf("{\"id\":\"%06d\"}", i)] {
						t.Errorf("cursor at %d but line %d never reached the sink", c.Offset, i)

						return
					}
				}
			}
		},
	})

	cur, _ := tailer.LoadCursors(col)

	for _, c := range cur {
		t.Logf("cursor after the kills: %d of %d lines", int(c.Offset)/crashLine, crashLines)
	}

	tl, err := tailer.Open(tailer.Config{Dir: col, Def: crashDef(data), Sink: outSink{mustAppend(t, out)}})
	if err != nil {
		t.Fatal(err)
	}

	defer tl.Close()

	if err := tl.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}

	if got := len(readIDs(t, out)); got != crashLines {
		t.Fatalf("rows = %d, lines = %d", got, crashLines)
	}
}

func mustAppend(t *testing.T, p string) *os.File {
	t.Helper()

	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { f.Close() })

	return f
}
