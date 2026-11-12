package ndb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The shared suite of plan §12.3: the JSON cases in spec/cases, which every
// client runs. This runner is a copy of the shape in internal/spec; it does
// not import the engine.

const sharedCasesDir = "../../spec/cases"

type sharedCase struct {
	name       string
	file       string
	collection string
	catalog    json.RawMessage
	writes     []sharedStep
	request    sharedStep
	ordered    bool
	rows       []json.RawMessage
	wantError  string
}

type sharedStep struct {
	Op   string          `json:"op"`
	Body json.RawMessage `json:"body"`
}

type sharedFile struct {
	Name       string `json:"name"`
	Collection string `json:"collection"`
	Setup      struct {
		Catalog json.RawMessage `json:"catalog"`
		Writes  []sharedStep    `json:"writes"`
	} `json:"setup"`
	Request sharedStep `json:"request"`
	Expect  struct {
		Ordered bool              `json:"ordered"`
		Rows    []json.RawMessage `json:"rows"`
		Error   string            `json:"error"`
	} `json:"expect"`
}

// loadSharedCases reads every *.json file under spec/cases.
func loadSharedCases(t *testing.T) []sharedCase {
	t.Helper()

	var out []sharedCase

	err := filepath.WalkDir(sharedCasesDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var f sharedFile

		if err := json.Unmarshal(raw, &f); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		out = append(out, sharedCase{
			name:       f.Name,
			file:       path,
			collection: f.Collection,
			catalog:    f.Setup.Catalog,
			writes:     f.Setup.Writes,
			request:    f.Request,
			ordered:    f.Expect.Ordered,
			rows:       f.Expect.Rows,
			wantError:  f.Expect.Error,
		})

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	return out
}

func TestSharedCases(t *testing.T) {
	cases := loadSharedCases(t)

	if len(cases) == 0 {
		t.Fatalf("no cases under %s", sharedCasesDir)
	}

	for _, transport := range []struct {
		name string
		addr string
	}{
		{name: "unix", addr: daemon.unix},
		{name: "tcp", addr: daemon.tcp},
	} {
		t.Run(transport.name, func(t *testing.T) {
			db := openTest(t, transport.addr)

			for _, c := range cases {
				t.Run(c.name, func(t *testing.T) {
					runSharedCase(t, db, c)
				})
			}
		})
	}
}

// runSharedCase declares a fresh collection, runs the setup writes and the
// request, and compares the rows or the error code with the expectation.
func runSharedCase(t *testing.T, db *DB, c sharedCase) {
	t.Helper()

	ctx := context.Background()
	name := newColl()

	if err := db.putCollection(ctx, name, c.catalog); err != nil {
		t.Fatalf("%s: catalog: %v", c.file, err)
	}

	for i, w := range c.writes {
		if w.Op != "bulk" {
			t.Fatalf("%s: setup op %q is not supported", c.file, w.Op)
		}

		resp, err := db.do(ctx, http.MethodPost, collPath(name)+"/_"+w.Op, w.Body)
		if err != nil {
			t.Fatalf("%s: write %d: %v", c.file, i, err)
		}

		discard(resp)
	}

	var (
		resp *http.Response
		err  error
	)

	switch c.request.Op {
	case "search", "aggregate":
		resp, err = db.do(ctx, http.MethodPost, collPath(name)+"/_"+c.request.Op+"?decrypt=1", c.request.Body)
	default:
		t.Fatalf("%s: request op %q is not supported", c.file, c.request.Op)
	}

	if c.wantError != "" {
		var apiErr *Error

		if !errors.As(err, &apiErr) || apiErr.Code != c.wantError {
			t.Fatalf("%s: want error %q, got %v", c.file, c.wantError, err)
		}

		return
	}

	if err != nil {
		t.Fatalf("%s: %v", c.file, err)
	}

	got := readSharedRows(t, c.file, resp, c.ordered)
	want := normalizeRows(t, c.file, c.rows, c.ordered)

	if !slices.Equal(got, want) {
		t.Fatalf("%s:\n got  %s\n want %s", c.file, strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
}

func readSharedRows(t *testing.T, file string, resp *http.Response, ordered bool) []string {
	t.Helper()

	defer resp.Body.Close()

	var rows []json.RawMessage

	for row, err := range ndjsonRows(resp.Body) {
		if err != nil {
			t.Fatalf("%s: bad row: %v", file, err)
		}

		rows = append(rows, row)
	}

	return normalizeRows(t, file, rows, ordered)
}

// normalizeRows re-encodes each row with sorted keys and numbers kept as
// written. It sorts the rows when the order does not matter.
func normalizeRows(t *testing.T, file string, rows []json.RawMessage, ordered bool) []string {
	t.Helper()

	out := make([]string, 0, len(rows))

	for _, r := range rows {
		var v any

		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()

		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s: bad row %s: %v", file, r, err)
		}

		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}

		out = append(out, string(b))
	}

	if !ordered {
		slices.Sort(out)
	}

	return out
}
