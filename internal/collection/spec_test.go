package collection_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/spec"
)

// specEngine runs the shared cases against a real engine-mode collection.
type specEngine struct {
	t    *testing.T
	cols map[string]*collection.Collection

	// seal seals every collection before each find, so the shared cases
	// read from segments and memtable. The indexes are dropped, so the finds
	// run the column scan.
	seal bool

	// compact puts every insert in its own segment, and merges them all
	// before each read, so the cases prove that compaction keeps the results.
	compact bool
}

func (e *specEngine) EnsureCollection(_ context.Context, name string, raw json.RawMessage) error {
	var def catalog.Def

	if err := json.Unmarshal(raw, &def); err != nil {
		return err
	}

	def.Name = name

	root := e.t.TempDir()

	cfg := collection.Config{Dir: filepath.Join(root, "wal"), FS: fs.OS{}, Def: def}

	if e.seal || e.compact {
		cfg.Data = root
		cfg.CompactMin = 1 << 20
	}

	c, err := collection.Open(cfg)
	if err != nil {
		return err
	}

	e.t.Cleanup(func() { c.Close() })

	if e.seal || e.compact {
		for _, f := range def.Indexes {
			c.DropIndex(f)
		}
	}

	e.cols[name] = c

	return nil
}

func (e *specEngine) Do(ctx context.Context, name string, step spec.Step) ([]json.RawMessage, error) {
	c := e.cols[name]

	switch step.Op {
	case "insert":
		var body struct {
			Insert []json.RawMessage `json:"insert"`
		}

		if err := json.Unmarshal(step.Body, &body); err != nil {
			return nil, err
		}

		docs := make([][]byte, len(body.Insert))

		for i, d := range body.Insert {
			docs[i] = d
		}

		_, err := c.Insert(ctx, docs...)
		if err == nil && e.compact {
			err = c.Seal()
		}

		return nil, specErr(err)
	case "aggregate":
		if err := e.settle(c); err != nil {
			return nil, err
		}

		p, err := c.ParseAggregate(step.Body)
		if err != nil {
			return nil, specErr(err)
		}

		page, err := c.Aggregate(ctx, p)

		return page.Rows, specErr(err)
	case "find":
		if err := e.settle(c); err != nil {
			return nil, err
		}

		q, err := c.ParseQuery(step.Body)
		if err != nil {
			return nil, specErr(err)
		}

		page, err := c.Query(ctx, q)

		return page.Rows, specErr(err)
	}

	return nil, &spec.CodeError{Code: "unknown_op"}
}

// settle seals the memtable, and compacts in compact mode.
func (e *specEngine) settle(c *collection.Collection) error {
	if err := c.Seal(); err != nil {
		return err
	}

	if e.compact {
		return c.Compact()
	}

	return nil
}

func specErr(err error) error {
	var ce interface{ Code() string }

	if errors.As(err, &ce) {
		return &spec.CodeError{Code: ce.Code()}
	}

	return err
}

func TestSharedCases(t *testing.T) {
	cases, err := spec.Load("../../spec/cases")
	if err != nil {
		t.Fatal(err)
	}

	spec.Run(t, cases, func(t *testing.T) spec.Engine {
		return &specEngine{t: t, cols: map[string]*collection.Collection{}}
	})
}

func TestSharedCasesAfterSealing(t *testing.T) {
	cases, err := spec.Load("../../spec/cases")
	if err != nil {
		t.Fatal(err)
	}

	spec.Run(t, cases, func(t *testing.T) spec.Engine {
		return &specEngine{t: t, cols: map[string]*collection.Collection{}, seal: true}
	})
}

func TestSharedCasesAfterCompaction(t *testing.T) {
	cases, err := spec.Load("../../spec/cases")
	if err != nil {
		t.Fatal(err)
	}

	spec.Run(t, cases, func(t *testing.T) spec.Engine {
		return &specEngine{t: t, cols: map[string]*collection.Collection{}, compact: true}
	})
}
