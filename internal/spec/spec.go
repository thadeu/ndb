// Package spec loads the shared cases in spec/cases and runs them against
// any Engine: the library, the HTTP API, or (through their own runners) the SDKs.
package spec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type Case struct {
	Name       string `json:"name"`
	Collection string `json:"collection"`
	Setup      Setup  `json:"setup"`
	Request    Step   `json:"request"`
	Expect     Expect `json:"expect"`

	File string `json:"-"`
}

type Setup struct {
	Catalog json.RawMessage `json:"catalog"`
	Writes  []Step          `json:"writes"`
}

// Step is one call: Op is insert, find or aggregate, and Body is the JSON
// body of the HTTP call of that name.
type Step struct {
	Op   string          `json:"op"`
	Body json.RawMessage `json:"body"`
}

type Expect struct {
	Ordered bool              `json:"ordered"`
	Rows    []json.RawMessage `json:"rows"`
	Error   string            `json:"error"`
}

// CodeError is an engine error with a stable code, such as "duplicate_id".
type CodeError struct {
	Code string
}

func (e *CodeError) Error() string {
	return e.Code
}

type Engine interface {
	EnsureCollection(ctx context.Context, name string, def json.RawMessage) error
	Do(ctx context.Context, collection string, step Step) ([]json.RawMessage, error)
}

// Load reads every *.json file under dir.
func Load(dir string) ([]Case, error) {
	var cases []Case

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		var c Case

		if err := json.Unmarshal(data, &c); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}

		c.File = path
		cases = append(cases, c)

		return nil
	})

	return cases, err
}

// Run runs each case as a subtest, with a fresh engine per case.
func Run(t *testing.T, cases []Case, newEngine func(t *testing.T) Engine) {
	t.Helper()

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			ctx := context.Background()
			eng := newEngine(t)

			if err := eng.EnsureCollection(ctx, c.Collection, c.Setup.Catalog); err != nil {
				t.Fatalf("%s: catalog: %v", c.File, err)
			}

			for i, w := range c.Setup.Writes {
				if _, err := eng.Do(ctx, c.Collection, w); err != nil {
					t.Fatalf("%s: write %d: %v", c.File, i, err)
				}
			}

			rows, err := eng.Do(ctx, c.Collection, c.Request)

			check(t, c, rows, err)
		})
	}
}

func check(t *testing.T, c Case, rows []json.RawMessage, err error) {
	t.Helper()

	if c.Expect.Error != "" {
		var ce *CodeError

		if !errors.As(err, &ce) || ce.Code != c.Expect.Error {
			t.Fatalf("%s: want error %q, got %v", c.File, c.Expect.Error, err)
		}

		return
	}

	if err != nil {
		t.Fatalf("%s: %v", c.File, err)
	}

	got := normalize(t, rows, c.Expect.Ordered)
	want := normalize(t, c.Expect.Rows, c.Expect.Ordered)

	if !slices.Equal(got, want) {
		t.Fatalf("%s:\n got  %s\n want %s", c.File, strings.Join(got, "\n      "), strings.Join(want, "\n      "))
	}
}

// normalize re-encodes each row with sorted keys, and sorts rows when the
// order does not matter.
func normalize(t *testing.T, rows []json.RawMessage, ordered bool) []string {
	t.Helper()

	out := make([]string, 0, len(rows))

	for _, r := range rows {
		var v any

		dec := json.NewDecoder(bytes.NewReader(r))
		dec.UseNumber()

		if err := dec.Decode(&v); err != nil {
			t.Fatalf("bad row %s: %v", r, err)
		}

		b, _ := json.Marshal(v)
		out = append(out, string(b))
	}

	if !ordered {
		slices.Sort(out)
	}

	return out
}
