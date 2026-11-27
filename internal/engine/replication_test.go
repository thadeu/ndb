package engine_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/wal"
)

func nextRecord(t *testing.T, rs *engine.RecordStream) engine.Record {
	t.Helper()

	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	rec, err := rs.Next(c)
	if err != nil {
		t.Fatalf("next record: %v", err)
	}

	return rec
}

func TestOpenRecordsGivesWholeRecordsInOrder(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "users"})

	if _, err := col.Insert(ctx, []byte(`{"_id":"a","n":1}`)); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"b","n":2}`), []byte(`{"_id":"c","n":3}`)); err != nil {
		t.Fatal(err)
	}

	rs, err := db.OpenRecords("users", 0)
	if err != nil {
		t.Fatal(err)
	}

	defer rs.Close()

	first := nextRecord(t, rs)
	second := nextRecord(t, rs)

	if len(first.Ops) != 1 || first.Ops[0].ID != "a" || len(second.Ops) != 2 || second.Tx <= first.Tx {
		t.Fatalf("records: %+v %+v", first, second)
	}

	if _, err := col.DeleteID(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	live := nextRecord(t, rs)

	if len(live.Ops) != 1 || live.Ops[0].Kind != wal.Del || db.LastTx("users") != live.Tx {
		t.Fatalf("live record: %+v, last %d", live, db.LastTx("users"))
	}

	after, err := db.OpenRecords("users", second.Tx)
	if err != nil {
		t.Fatal(err)
	}

	defer after.Close()

	if rec := nextRecord(t, after); rec.Tx != live.Tx {
		t.Fatalf("from %d: got tx %d, want %d", second.Tx, rec.Tx, live.Tx)
	}
}

func TestOpenRecordsOfAnUnknownCollection(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	if _, err := db.OpenRecords("nope", 0); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("err = %v", err)
	}

	if db.LastTx("nope") != 0 {
		t.Fatal("last tx of an unknown collection")
	}
}

func TestApplyOpsIsIdempotent(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	ops := []wal.Op{
		{Kind: wal.Put, ID: "a", V: 1, Doc: []byte(`{"_id":"a","n":1}`)},
		{Kind: wal.Put, ID: "b", V: 1, Doc: []byte(`{"_id":"b","n":1}`)},
	}

	update := []wal.Op{{Kind: wal.Put, ID: "a", V: 2, Doc: []byte(`{"_id":"a","n":2}`)}}
	del := []wal.Op{{Kind: wal.Del, ID: "b", V: 2}}

	for range 2 {
		for _, rec := range [][]wal.Op{ops, update, del} {
			if err := db.ApplyOps(ctx, "users", rec); err != nil {
				t.Fatal(err)
			}
		}
	}

	col, err := db.Get("users")
	if err != nil {
		t.Fatal(err)
	}

	doc, ok := col.Get("a")

	if !ok || doc.V != 2 {
		t.Fatalf("a = %+v %v", doc, ok)
	}

	if _, ok := col.Get("b"); ok {
		t.Fatal("b is still there")
	}

	if err := db.ApplyOps(ctx, "users", []wal.Op{{Kind: wal.Trunc}}); err != nil {
		t.Fatal(err)
	}

	if n := rows(t, col); n != 0 {
		t.Fatalf("rows after the truncate op: %d", n)
	}

	if err := db.ApplyOps(ctx, "gone", []wal.Op{{Kind: wal.Trunc}}); err != nil {
		t.Fatalf("truncate of an unknown collection: %v", err)
	}
}

func TestApplyDefCreatesMergesAndDropsIndexes(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))

	if err := db.ApplyDef(catalog.Def{Name: "users", Indexes: []string{"email", "age"}}); err != nil {
		t.Fatal(err)
	}

	if err := db.ApplyDef(catalog.Def{Name: "users", Indexes: []string{"email"}}); err != nil {
		t.Fatal(err)
	}

	def, _ := db.Definition("users")

	if len(def.Indexes) != 1 || def.Indexes[0] != "email" {
		t.Fatalf("indexes = %v", def.Indexes)
	}

	if err := db.ApplyDef(catalog.Def{Name: "logs", Write: catalog.Direct, Path: "/x/*.ndjson"}); err != nil {
		t.Fatal(err)
	}

	if _, ok := db.Definition("logs"); ok {
		t.Fatal("a direct definition was applied")
	}

	if n := len(db.EngineDefs()); n != 1 {
		t.Fatalf("engine defs = %d", n)
	}
}

func TestReplicaSnapshotGivesLiveDocsAtOneTx(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "users"})

	fillPosts(t, col, 10)

	if _, err := col.DeleteID(ctx, "p3"); err != nil {
		t.Fatal(err)
	}

	var (
		at  uint64
		ids = map[string]uint64{}
	)

	err := db.ReplicaSnapshot(ctx, "users", func(tx uint64) error {
		at = tx

		return nil
	}, func(op wal.Op) error {
		ids[op.ID] = op.V

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if at != db.LastTx("users") || len(ids) != 9 {
		t.Fatalf("at %d (last %d), %d docs", at, db.LastTx("users"), len(ids))
	}

	if _, has := ids["p3"]; has {
		t.Fatal("a deleted doc is in the snapshot")
	}

	if err := db.ReplicaSnapshot(ctx, "nope", nil, nil); !errors.Is(err, catalog.ErrUnknown) {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRecordsLeavesOutTheTTLSweep(t *testing.T) {
	db := open(t, cfg(t.TempDir(), nil))
	col := ensure(t, db, catalog.Def{Name: "s", ExpiresField: "exp"})

	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)

	if _, err := col.Insert(ctx, []byte(`{"_id":"a","exp":"`+past+`"}`)); err != nil {
		t.Fatal(err)
	}

	if err := db.ApplyOps(ctx, "copy", []wal.Op{{Kind: wal.Put, ID: "x", V: 1, Doc: []byte(`{"_id":"x"}`)}}); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Sweep(); err != nil {
		t.Fatal(err)
	}

	rs, err := db.OpenRecords("s", 0)
	if err != nil {
		t.Fatal(err)
	}

	defer rs.Close()

	nextRecord(t, rs)

	swept := nextRecord(t, rs)

	if len(swept.Ops) != 0 {
		t.Fatalf("the sweep record has ops: %+v", swept.Ops)
	}
}
