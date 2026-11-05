package catalog_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
)

func open(t *testing.T, opts catalog.Options) (*catalog.Catalog, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "catalog.bolt")

	c, err := catalog.Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { c.Close() })

	return c, path
}

func TestEnsureCreatesWithDefaults(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	def, err := c.Ensure(catalog.Def{Name: "posts"}, catalog.FromCode)
	if err != nil {
		t.Fatal(err)
	}

	if def.ID != "_id" || def.Write != catalog.Engine || def.Durability != catalog.Strict {
		t.Fatalf("defaults not applied: %+v", def)
	}
}

func TestPathWithoutWriteMeansDirect(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	def, err := c.Ensure(catalog.Def{Name: "calls", Path: "/data/*.ndjson"}, catalog.FromCode)
	if err != nil || def.Write != catalog.Direct {
		t.Fatalf("def=%+v err=%v", def, err)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	c, _ := open(t, catalog.Options{})
	d := catalog.Def{Name: "posts", ID: "post_id", Indexes: []string{"status"}}

	a, err := c.Ensure(d, catalog.FromCode)
	if err != nil {
		t.Fatal(err)
	}

	b, err := c.Ensure(d, catalog.FromCode)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(a.Indexes, b.Indexes) || a.ID != b.ID {
		t.Fatalf("%+v != %+v", a, b)
	}
}

func TestIndexesAreMerged(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	c.Ensure(catalog.Def{Name: "calls", Indexes: []string{"status"}}, catalog.FromCode)

	def, err := c.Ensure(catalog.Def{Name: "calls", Indexes: []string{"dnis", "status"}}, catalog.FromYAML)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(def.Indexes, []string{"dnis", "status"}) {
		t.Fatalf("indexes = %v", def.Indexes)
	}
}

func TestConflicts(t *testing.T) {
	base := catalog.Def{Name: "calls", ID: "call_id", Time: "ts", Write: catalog.Direct, Path: "/data/*.ndjson"}

	changes := map[string]catalog.Def{
		"id":    {Name: "calls", ID: "other"},
		"time":  {Name: "calls", Time: "created_at"},
		"write": {Name: "calls", Write: catalog.Engine},
		"path":  {Name: "calls", Path: "/other/*.ndjson"},
	}

	for field, change := range changes {
		t.Run(field, func(t *testing.T) {
			c, _ := open(t, catalog.Options{})

			if _, err := c.Ensure(base, catalog.FromCode); err != nil {
				t.Fatal(err)
			}

			if _, err := c.Ensure(change, catalog.FromCode); !errors.Is(err, catalog.ErrConflict) {
				t.Fatalf("want ErrConflict, got %v", err)
			}
		})
	}
}

func TestEmptyFieldMeansNotSpecified(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	c.Ensure(catalog.Def{Name: "calls", ID: "call_id", Time: "ts"}, catalog.FromYAML)

	def, err := c.Ensure(catalog.Def{Name: "calls", Indexes: []string{"x"}}, catalog.FromCode)
	if err != nil {
		t.Fatal(err)
	}

	if def.ID != "call_id" || def.Time != "ts" {
		t.Fatalf("existing fields lost: %+v", def)
	}
}

func TestYAMLWinsOperatorSettings(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	c.Ensure(catalog.Def{Name: "logs", Durability: catalog.Relaxed, Retention: "30d"}, catalog.FromYAML)

	def, err := c.Ensure(catalog.Def{Name: "logs", Durability: catalog.Strict, Retention: "7d"}, catalog.FromCode)
	if err != nil {
		t.Fatal(err)
	}

	if def.Durability != catalog.Relaxed || def.Retention != "30d" {
		t.Fatalf("code overrode YAML: %+v", def)
	}

	def, _ = c.Ensure(catalog.Def{Name: "logs", Retention: "90d"}, catalog.FromYAML)

	if def.Retention != "90d" {
		t.Fatalf("YAML could not change its own setting: %+v", def)
	}
}

func TestCodeSetsOperatorSettingsWhenYAMLDidNot(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	c.Ensure(catalog.Def{Name: "logs"}, catalog.FromCode)

	def, _ := c.Ensure(catalog.Def{Name: "logs", Durability: catalog.Relaxed}, catalog.FromCode)

	if def.Durability != catalog.Relaxed {
		t.Fatalf("got %+v", def)
	}
}

func TestValidation(t *testing.T) {
	bad := map[string]catalog.Def{
		"empty name":         {Name: ""},
		"bad name":           {Name: "Posts!"},
		"direct needs path":  {Name: "a", Write: catalog.Direct},
		"engine has no path": {Name: "a", Write: catalog.Engine, Path: "/x/*.ndjson"},
		"bad write":          {Name: "a", Write: "both"},
		"bad durability":     {Name: "a", Durability: "maybe"},
		"empty index":        {Name: "a", Indexes: []string{""}},
		"line size too big":  {Name: "a", MaxLineSize: 17 << 20},
	}

	for name, def := range bad {
		t.Run(name, func(t *testing.T) {
			c, _ := open(t, catalog.Options{})

			if _, err := c.Ensure(def, catalog.FromCode); !errors.Is(err, catalog.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
		})
	}
}

func TestImplicit(t *testing.T) {
	off, _ := open(t, catalog.Options{})

	if _, err := off.EnsureImplicit("posts"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("implicit off: want ErrUnknown, got %v", err)
	}

	on, _ := open(t, catalog.Options{AllowImplicit: true})

	def, err := on.EnsureImplicit("posts")
	if err != nil || def.ID != "_id" {
		t.Fatalf("def=%+v err=%v", def, err)
	}

	on.Ensure(catalog.Def{Name: "calls", ID: "call_id"}, catalog.FromCode)

	def, _ = on.EnsureImplicit("calls")

	if def.ID != "call_id" {
		t.Fatalf("implicit changed an existing collection: %+v", def)
	}
}

func TestDropIndex(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	c.Ensure(catalog.Def{Name: "a", Indexes: []string{"x", "y"}}, catalog.FromCode)

	def, err := c.DropIndex("a", "x")
	if err != nil || !slices.Equal(def.Indexes, []string{"y"}) {
		t.Fatalf("def=%+v err=%v", def, err)
	}

	if _, err := c.DropIndex("missing", "x"); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("got %v", err)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	c, path := open(t, catalog.Options{})

	c.Ensure(catalog.Def{Name: "posts", ID: "post_id", Indexes: []string{"status"}}, catalog.FromCode)
	c.Close()

	again, err := catalog.Open(path, catalog.Options{})
	if err != nil {
		t.Fatal(err)
	}

	defer again.Close()

	def, ok := again.Get("posts")

	if !ok || def.ID != "post_id" || !slices.Equal(def.Indexes, []string{"status"}) {
		t.Fatalf("def=%+v ok=%v", def, ok)
	}

	if len(again.List()) != 1 {
		t.Fatalf("list = %v", again.List())
	}
}
