package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"
)

type Call struct {
	CallID   string    `ndb:"call_id,id"`
	Status   string    `ndb:"status,index"`
	DNIS     string    `ndb:"dnis,index"`
	Duration int       `ndb:"duration"`
	TS       time.Time `ndb:"ts,time"`
}

func TestSchemaTags(t *testing.T) {
	type withUnique struct {
		ID    string `ndb:"_id,id"`
		Email string `ndb:"email,unique"`
		Note  string `ndb:"note"`
		Skip  string `ndb:"-"`
		Plain string
	}

	s, err := schemaOf(reflect.TypeFor[Call]())
	if err != nil {
		t.Fatal(err)
	}

	if s.id != "call_id" || s.time != "ts" {
		t.Fatalf("id %q time %q, want call_id and ts", s.id, s.time)
	}

	if !reflect.DeepEqual(s.indexes, []string{"status", "dnis"}) {
		t.Fatalf("indexes %v, want [status dnis]", s.indexes)
	}

	u, err := schemaOf(reflect.TypeFor[withUnique]())
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(u.unique, []string{"email"}) {
		t.Fatalf("unique %v, want [email]", u.unique)
	}

	if len(u.fields) != 4 {
		t.Fatalf("fields %d, want 4 (_id, email, note, Plain)", len(u.fields))
	}
}

func TestSchemaTagErrors(t *testing.T) {
	type twoIDs struct {
		A string `ndb:"a,id"`
		B string `ndb:"b,id"`
	}

	type twoTimes struct {
		A time.Time `ndb:"a,time"`
		B time.Time `ndb:"b,time"`
	}

	type unknownOption struct {
		A string `ndb:"a,sparse"`
	}

	type noName struct {
		A string `ndb:",index"`
	}

	type unexported struct {
		a string `ndb:"a,index"`
	}

	cases := map[string]reflect.Type{
		"two id tags":      reflect.TypeFor[twoIDs](),
		"two time tags":    reflect.TypeFor[twoTimes](),
		"unknown option":   reflect.TypeFor[unknownOption](),
		"empty name":       reflect.TypeFor[noName](),
		"unexported field": reflect.TypeFor[unexported](),
		"not a struct":     reflect.TypeFor[int](),
	}

	for name, typ := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := schemaOf(typ); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestEncodeDecodeUsesTagNames(t *testing.T) {
	ts := time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)

	s, err := schemaOf(reflect.TypeFor[Call]())
	if err != nil {
		t.Fatal(err)
	}

	raw, err := s.encode(Call{CallID: "c1", Status: "answered", Duration: 42, TS: ts})
	if err != nil {
		t.Fatal(err)
	}

	var doc map[string]any

	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"call_id", "status", "dnis", "duration", "ts"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("document has no key %q: %s", key, raw)
		}
	}

	var got Call

	if err := s.decode(raw, reflect.ValueOf(&got).Elem()); err != nil {
		t.Fatal(err)
	}

	if got.CallID != "c1" || got.Duration != 42 || !got.TS.Equal(ts) {
		t.Fatalf("decode: got %+v", got)
	}
}

func TestCollectionEnsuresOnFirstUse(t *testing.T) {
	db := openTest(t, daemon.unix)
	name := newColl()

	if _, err := Collection[Call](context.Background(), db, name); err != nil {
		t.Fatal(err)
	}

	if !db.ensured[name] {
		t.Fatal("collection was not marked as ensured")
	}

	def := catalogDef(t, db, name)

	if def.ID != "call_id" || def.Time != "ts" {
		t.Fatalf("definition id %q time %q, want call_id and ts", def.ID, def.Time)
	}

	slices.Sort(def.Indexes)

	if !reflect.DeepEqual(def.Indexes, []string{"dnis", "status"}) {
		t.Fatalf("definition indexes %v, want [dnis status]", def.Indexes)
	}
}

func TestInsertGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	calls, err := Collection[Call](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	ts := time.Date(2026, 11, 5, 10, 30, 0, 0, time.UTC)

	if _, err := calls.Insert(ctx, Call{CallID: "c1", Status: "answered", Duration: 42, TS: ts}); err != nil {
		t.Fatal(err)
	}

	got, err := calls.Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != "answered" || got.Duration != 42 || !got.TS.Equal(ts) {
		t.Fatalf("get: got %+v", got)
	}

	v, err := calls.Update(ctx, "c1", map[string]any{"status": "done"}, IfVersion(1))
	if err != nil {
		t.Fatal(err)
	}

	if v != 2 {
		t.Fatalf("version after update: got %d, want 2", v)
	}

	_, err = calls.Update(ctx, "c1", map[string]any{"status": "stale"}, IfVersion(1))

	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update: want ErrVersionConflict, got %v", err)
	}

	if _, err := calls.Update(ctx, "c1", nil, Unset("duration")); err != nil {
		t.Fatal(err)
	}

	got, err = calls.Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != "done" || got.Duration != 0 {
		t.Fatalf("after update: got %+v, want status done and duration unset", got)
	}

	if err := calls.Delete(ctx, "c1"); err != nil {
		t.Fatal(err)
	}

	if _, err := calls.Get(ctx, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: want ErrNotFound, got %v", err)
	}
}

func TestInsertDuplicateID(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	calls, err := Collection[Call](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.Insert(ctx, Call{CallID: "c1"}); err != nil {
		t.Fatal(err)
	}

	if _, err := calls.Insert(ctx, Call{CallID: "c1"}); !errors.Is(err, ErrDuplicateID) {
		t.Fatalf("want ErrDuplicateID, got %v", err)
	}
}

// catalogEntry is one collection of GET /v1/c.
type catalogEntry struct {
	Name    string   `json:"name"`
	ID      string   `json:"id"`
	Time    string   `json:"time"`
	Indexes []string `json:"indexes"`
}

// catalogDef returns the definition of one collection from GET /v1/c.
func catalogDef(t *testing.T, db *DB, name string) catalogEntry {
	t.Helper()

	resp, err := db.do(context.Background(), http.MethodGet, "/v1/c", nil)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	var list struct {
		Collections []catalogEntry `json:"collections"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}

	for _, c := range list.Collections {
		if c.Name == name {
			return c
		}
	}

	t.Fatalf("collection %q not in the catalog", name)

	return catalogEntry{}
}
