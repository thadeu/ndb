package server_test

import (
	"bufio"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/server"
)

// compatRequest is one line of test/compat/requests.json. The writes made
// the fixture; the reads are sent again here.
type compatRequest struct {
	Phase  string          `json:"phase"`
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

type compatAnswer struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// TestCompat opens the data dir that each past release wrote (made by
// test/compat/make-fixture.sh) with the code of today, and checks that every
// read gives the answer that release gave. A failure here means that an app
// in production would see other data after an upgrade.
func TestCompat(t *testing.T) {
	root := filepath.Join("..", "..", "test", "compat")
	reads := compatReads(t, filepath.Join(root, "requests.json"))

	fixtures, err := filepath.Glob(filepath.Join(root, "v*"))
	if err != nil {
		t.Fatal(err)
	}

	if len(fixtures) == 0 {
		t.Fatal("no fixture in test/compat")
	}

	for _, dir := range fixtures {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			answers := compatAnswers(t, filepath.Join(dir, "answers.ndjson"))

			if len(answers) != len(reads) {
				t.Fatalf("%d answers for %d reads: make the fixture again", len(answers), len(reads))
			}

			data := t.TempDir()

			if err := os.CopyFS(data, os.DirFS(filepath.Join(dir, "data"))); err != nil {
				t.Fatal(err)
			}

			db, err := engine.Open(config.Config{DataDir: data, AllowImplicit: true}, slog.Default())
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() { db.Close() })

			srv := server.New()
			srv.Attach(db)

			for i, req := range reads {
				rec := raw(srv.Handler(), req.Method, req.Path, string(req.Body))
				want := answers[i]

				if rec.Code != want.Status {
					t.Errorf("%s %s: status %d, want %d: %s", req.Method, req.Path, rec.Code, want.Status, rec.Body)

					continue
				}

				if !sameJSON(t, rec.Body.Bytes(), want.Body) {
					t.Errorf("%s %s:\n got %s\nwant %s", req.Method, req.Path, rec.Body, want.Body)
				}
			}
		})
	}
}

func compatReads(t *testing.T, path string) []compatRequest {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var all []compatRequest

	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}

	var reads []compatRequest

	for _, r := range all {
		if r.Phase == "read" {
			reads = append(reads, r)
		}
	}

	return reads
}

func compatAnswers(t *testing.T, path string) []compatAnswer {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	defer f.Close()

	var out []compatAnswer

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for sc.Scan() {
		var a compatAnswer

		if err := json.Unmarshal(sc.Bytes(), &a); err != nil {
			t.Fatal(err)
		}

		out = append(out, a)
	}

	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	return out
}

func sameJSON(t *testing.T, got, want []byte) bool {
	t.Helper()

	var g, w any

	if err := json.Unmarshal(got, &g); err != nil {
		return false
	}

	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}

	return reflect.DeepEqual(withoutProgress(g), withoutProgress(w))
}

// withoutProgress drops "done" and "total" from the state of an index
// ({"field","state","done","total"}): they count the work of the process
// that answers, not the data.
func withoutProgress(v any) any {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["state"]; ok {
			delete(x, "done")
			delete(x, "total")
		}

		for k, e := range x {
			x[k] = withoutProgress(e)
		}
	case []any:
		for i, e := range x {
			x[i] = withoutProgress(e)
		}
	}

	return v
}
