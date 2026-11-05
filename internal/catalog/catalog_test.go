package catalog_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

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

func TestTTLNeedsTime(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	_, err := c.Ensure(catalog.Def{Name: "sessions", TTL: "24h"}, catalog.FromCode)
	if !errors.Is(err, catalog.ErrInvalid) {
		t.Fatalf("ttl without time: %v", err)
	}

	def, err := c.Ensure(catalog.Def{Name: "sessions", Time: "created_at", TTL: "1d"}, catalog.FromCode)
	if err != nil || def.TTL != "1d" {
		t.Fatalf("def=%+v err=%v", def, err)
	}
}

func TestTTLAndExpiresFieldExcludeEachOther(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	_, err := c.Ensure(catalog.Def{Name: "a", Time: "t", TTL: "1h", ExpiresField: "e"}, catalog.FromCode)
	if !errors.Is(err, catalog.ErrInvalid) {
		t.Fatalf("both set in one declaration: %v", err)
	}

	if _, err := c.Ensure(catalog.Def{Name: "b", Time: "t", TTL: "1h"}, catalog.FromCode); err != nil {
		t.Fatal(err)
	}

	_, err = c.Ensure(catalog.Def{Name: "b", ExpiresField: "e"}, catalog.FromCode)
	if !errors.Is(err, catalog.ErrInvalid) {
		t.Fatalf("both set after a merge: %v", err)
	}
}

func TestTTLChangeConflicts(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	if _, err := c.Ensure(catalog.Def{Name: "a", ExpiresField: "expires_at"}, catalog.FromCode); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Ensure(catalog.Def{Name: "a", ExpiresField: "expires_at"}, catalog.FromCode); err != nil {
		t.Fatalf("same value: %v", err)
	}

	if _, err := c.Ensure(catalog.Def{Name: "a", ExpiresField: "other"}, catalog.FromCode); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("changed expires_field: %v", err)
	}

	if _, err := c.Ensure(catalog.Def{Name: "d", Time: "t", TTL: "1h"}, catalog.FromCode); err != nil {
		t.Fatal(err)
	}

	if _, err := c.Ensure(catalog.Def{Name: "d", TTL: "2h"}, catalog.FromCode); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("changed ttl: %v", err)
	}
}

func TestDurationsAreValidated(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	for _, d := range []catalog.Def{
		{Name: "a", Time: "t", TTL: "soon"},
		{Name: "b", Time: "t", TTL: "-1h"},
		{Name: "c", Retention: "forever"},
	} {
		if _, err := c.Ensure(d, catalog.FromCode); !errors.Is(err, catalog.ErrInvalid) {
			t.Fatalf("%+v: %v", d, err)
		}
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"30d":    30 * 24 * time.Hour,
		"2w":     14 * 24 * time.Hour,
		"90m":    90 * time.Minute,
		"1h30m":  90 * time.Minute,
		"1500ms": 1500 * time.Millisecond,
	}

	for in, want := range cases {
		got, err := catalog.ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("%q: got %v err %v, want %v", in, got, err, want)
		}
	}

	for _, bad := range []string{"", "d", "0", "0s", "-5d", "x30d"} {
		if _, err := catalog.ParseDuration(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}
