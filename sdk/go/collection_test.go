package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type Call struct {
	CallID   string    `ndb:"_id"`
	Status   string    `ndb:"status,index"`
	DNIS     string    `ndb:"dnis,index"`
	Duration int       `ndb:"duration"`
	TS       time.Time `ndb:"ts,time"`
}

func TestSchemaTags(t *testing.T) {
	type withUnique struct {
		ID    string `ndb:"_id"`
		Email string `ndb:"email,unique"`
		Note  string `ndb:"note"`
		Skip  string `ndb:"-"`
		Plain string
	}

	s, err := schemaOf(reflect.TypeFor[Call]())
	if err != nil {
		t.Fatal(err)
	}

	if s.time != "ts" {
		t.Fatalf("time %q, want ts", s.time)
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
	type idOption struct {
		A string `ndb:"a,id"`
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
		"id option":        reflect.TypeFor[idOption](),
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

	for _, key := range []string{"_id", "status", "dnis", "duration", "ts"} {
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

	if def.Time != "ts" {
		t.Fatalf("definition time %q, want ts", def.Time)
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

	res, err := calls.Update(ctx, And(Eq("_id", "c1"), Eq("_v", 1)), Ops{"$set": {"status": "done"}})
	if err != nil {
		t.Fatal(err)
	}

	if res.Matched != 1 || res.Modified != 1 || res.Doc == nil || res.Doc.Status != "done" {
		t.Fatalf("update: got %+v", res)
	}

	res, err = calls.Update(ctx, And(Eq("_id", "c1"), Eq("_v", 1)), Ops{"$set": {"status": "stale"}})
	if err != nil || res.Matched != 0 || res.Doc != nil {
		t.Fatalf("stale update: want matched 0, got %+v, %v", res, err)
	}

	if _, err := calls.Update(ctx, Eq("_id", "c1"), Ops{"$unset": {"duration": true}}); err != nil {
		t.Fatal(err)
	}

	got, err = calls.Get(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}

	if got.Status != "done" || got.Duration != 0 {
		t.Fatalf("after update: got %+v, want status done and duration unset", got)
	}

	if n, err := calls.Delete(ctx, Eq("_id", "c1")); err != nil || n != 1 {
		t.Fatalf("delete: n=%d err=%v", n, err)
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

func TestSchemaExpiresOption(t *testing.T) {
	type session struct {
		ID        string    `ndb:"_id"`
		ExpiresAt time.Time `ndb:"expires_at,expires"`
	}

	type twoExpires struct {
		A time.Time `ndb:"a,expires"`
		B time.Time `ndb:"b,expires"`
	}

	s, err := schemaOf(reflect.TypeFor[session]())
	if err != nil || s.expires != "expires_at" {
		t.Fatalf("expires %q err %v, want expires_at", s.expires, err)
	}

	if _, err := schemaOf(reflect.TypeFor[twoExpires]()); err == nil {
		t.Fatal("two expires fields must fail")
	}

	body, err := json.Marshal(ensureBody{ExpiresField: s.expires})
	if err != nil || string(body) != `{"expires_field":"expires_at"}` {
		t.Fatalf("ensure body %s err %v", body, err)
	}
}

type Stock struct {
	SKU   string `ndb:"_id"`
	Group string `ndb:"grp,index"`
	Qty   int    `ndb:"qty"`
	Tags  []string
}

func TestUpdateManyUpsertAndGuards(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	stock, err := Collection[Stock](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stock.Insert(ctx, Stock{SKU: "a", Group: "g1", Qty: 1}, Stock{SKU: "b", Group: "g1", Qty: 1}, Stock{SKU: "c", Group: "g2", Qty: 1}); err != nil {
		t.Fatal(err)
	}

	res, err := stock.Update(ctx, Eq("grp", "g1"), Ops{"$inc": {"qty": 4}}, Many())
	if err != nil || res.Matched != 2 || res.Modified != 2 || res.Doc != nil {
		t.Fatalf("many: %+v, %v", res, err)
	}

	guard := And(Eq("_id", "c"), Gte("qty", 1))

	if res, err := stock.Update(ctx, guard, Ops{"$inc": {"qty": -1}}); err != nil || res.Modified != 1 || res.Doc.Qty != 0 {
		t.Fatalf("first decrement: %+v, %v", res, err)
	}

	if res, err := stock.Update(ctx, guard, Ops{"$inc": {"qty": -1}}); err != nil || res.Matched != 0 {
		t.Fatalf("second decrement: %+v, %v", res, err)
	}

	res, err = stock.Update(ctx, Eq("_id", "new"), Ops{"$inc": {"qty": 2}, "$push": {"Tags": "x"}}, Upsert())
	if err != nil || res.Matched != 0 || res.Modified != 1 || res.Doc.Qty != 2 || len(res.Doc.Tags) != 1 {
		t.Fatalf("upsert: %+v, %v", res, err)
	}

	_, err = stock.Update(ctx, Eq("qty", 5), Ops{"$set": {"qty": 6}}, Many())

	if !errors.Is(err, ErrUnindexedMany) {
		t.Fatalf("unindexed many: %v", err)
	}

	_, err = stock.Update(ctx, Eq("_id", "a"), Ops{"$set": {"qty": 1}, "$inc": {"qty": 1}})

	if !errors.Is(err, ErrConflictingUpdate) {
		t.Fatalf("conflict: %v", err)
	}

	if n, err := stock.Delete(ctx, Eq("grp", "g1"), Many()); err != nil || n != 2 {
		t.Fatalf("delete many: n=%d err=%v", n, err)
	}
}

func TestUpdateRejectsPlainOperatorKeysBeforeSending(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	stock, err := Collection[Stock](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	_, err = stock.Update(ctx, Eq("_id", "a"), Ops{"set": {"qty": 1}})

	if err == nil || !strings.Contains(err.Error(), "must start with $") {
		t.Fatalf("got %v", err)
	}
}
