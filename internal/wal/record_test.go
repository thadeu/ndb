package wal_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/wal"
)

var ts = time.Date(2026, 11, 4, 12, 0, 0, 123_000_000, time.UTC)

func TestEncodeSingleOp(t *testing.T) {
	rec := wal.Record{Tx: 42, TS: ts, Ops: []wal.Op{
		{Kind: wal.Put, ID: "ev_1", V: 3, Doc: json.RawMessage(`{"status":"pending"}`)},
	}}

	line, err := wal.Encode(nil, rec)
	if err != nil {
		t.Fatal(err)
	}

	want := `{"_tx":42,"_ts":"2026-11-04T12:00:00.123Z","_op":"put","_id":"ev_1","_v":3,"doc":{"status":"pending"},"_crc":"`

	if !bytes.HasPrefix(line, []byte(want)) {
		t.Fatalf("got %s", line)
	}

	if !json.Valid(line) {
		t.Fatalf("line is not valid JSON: %s", line)
	}
}

func TestRoundTrip(t *testing.T) {
	recs := []wal.Record{
		{Tx: 1, TS: ts, Ops: []wal.Op{{Kind: wal.Put, ID: "a", V: 1, Doc: json.RawMessage(`{"x":1}`)}}},
		{Tx: 2, TS: ts, Ops: []wal.Op{{Kind: wal.Del, ID: "b", V: 4}}},
		{Tx: 3, TS: ts, Ops: []wal.Op{
			{Kind: wal.Put, ID: "c\"q", V: 1, Doc: json.RawMessage(`{"n":{"m":[1,2]}}`)},
			{Kind: wal.Del, ID: "d", V: 2},
		}},
	}

	for _, rec := range recs {
		line, err := wal.Encode(nil, rec)
		if err != nil {
			t.Fatal(err)
		}

		got, err := wal.Decode(line)
		if err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}

		if got.Tx != rec.Tx || !got.TS.Equal(rec.TS) || len(got.Ops) != len(rec.Ops) {
			t.Fatalf("got %+v, want %+v", got, rec)
		}

		for i := range rec.Ops {
			g, w := got.Ops[i], rec.Ops[i]

			if g.Kind != w.Kind || g.ID != w.ID || g.V != w.V || string(g.Doc) != string(w.Doc) {
				t.Fatalf("op %d: got %+v, want %+v", i, g, w)
			}
		}
	}
}

func TestEncodeCompactsDoc(t *testing.T) {
	rec := wal.Record{Tx: 1, TS: ts, Ops: []wal.Op{{Kind: wal.Put, ID: "a", V: 1, Doc: json.RawMessage("{\n  \"x\": 1\n}")}}}

	line, err := wal.Encode(nil, rec)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.ContainsAny(line, "\n\r") {
		t.Fatalf("line has a newline: %q", line)
	}
}

func TestEncodeRejects(t *testing.T) {
	bad := []wal.Record{
		{Tx: 1, TS: ts},
		{Tx: 1, TS: ts, Ops: []wal.Op{{Kind: wal.Put, ID: "a", V: 1, Doc: json.RawMessage(`[1]`)}}},
		{Tx: 1, TS: ts, Ops: []wal.Op{{Kind: wal.Put, ID: "a", V: 1, Doc: json.RawMessage(`{bad`)}}},
		{Tx: 1, TS: ts, Ops: []wal.Op{{Kind: wal.Put, ID: "", V: 1, Doc: json.RawMessage(`{}`)}}},
	}

	for i, rec := range bad {
		if _, err := wal.Encode(nil, rec); err == nil {
			t.Fatalf("record %d: want error", i)
		}
	}
}

func TestDecodeDetectsDamage(t *testing.T) {
	line, _ := wal.Encode(nil, wal.Record{Tx: 7, TS: ts, Ops: []wal.Op{{Kind: wal.Put, ID: "a", V: 1, Doc: json.RawMessage(`{"x":"hello"}`)}}})

	flipped := bytes.Replace(line, []byte("hello"), []byte("hellp"), 1)

	cases := map[string][]byte{
		"flipped byte": flipped,
		"cut line":     line[:len(line)-5],
		"empty":        nil,
		"not json":     []byte("garbage"),
	}

	for name, in := range cases {
		if _, err := wal.Decode(in); !errors.Is(err, wal.ErrCorrupt) {
			t.Fatalf("%s: want ErrCorrupt, got %v", name, err)
		}
	}
}

func FuzzDecode(f *testing.F) {
	line, _ := wal.Encode(nil, wal.Record{Tx: 1, TS: ts, Ops: []wal.Op{{Kind: wal.Put, ID: "a", V: 1, Doc: json.RawMessage(`{"x":1}`)}}})

	f.Add(line)
	f.Add([]byte(`{"_crc":"00000000"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		rec, err := wal.Decode(data)
		if err != nil {
			return
		}

		again, err := wal.Encode(nil, rec)
		if err != nil {
			t.Fatalf("decoded record does not encode: %v", err)
		}

		if _, err := wal.Decode(again); err != nil {
			t.Fatalf("re-encoded line does not decode: %v", err)
		}
	})
}

func FuzzRoundTrip(f *testing.F) {
	f.Add(uint64(1), "id", uint64(1), []byte(`{"a":1}`), true)

	f.Fuzz(func(t *testing.T, tx uint64, id string, v uint64, doc []byte, put bool) {
		op := wal.Op{Kind: wal.Del, ID: id, V: v}

		if put {
			op = wal.Op{Kind: wal.Put, ID: id, V: v, Doc: doc}
		}

		line, err := wal.Encode(nil, wal.Record{Tx: tx, TS: ts, Ops: []wal.Op{op}})
		if err != nil {
			return
		}

		if bytes.ContainsAny(line, "\n") {
			t.Fatalf("newline in line %q", line)
		}

		got, err := wal.Decode(line)
		if err != nil {
			t.Fatalf("decode own line %q: %v", line, err)
		}

		if got.Ops[0].ID != id || got.Ops[0].V != v {
			t.Fatalf("got %+v", got.Ops[0])
		}
	})
}

func TestExpiredDeleteRoundTrips(t *testing.T) {
	rec := wal.Record{Tx: 7, TS: ts, Ops: []wal.Op{
		{Kind: wal.Del, ID: "a", V: 2, Exp: true},
		{Kind: wal.Del, ID: "b", V: 3},
	}}

	line, err := wal.Encode(nil, rec)
	if err != nil {
		t.Fatal(err)
	}

	got, err := wal.Decode(line)
	if err != nil {
		t.Fatal(err)
	}

	if !got.Ops[0].Exp || got.Ops[1].Exp {
		t.Fatalf("exp flags: %+v", got.Ops)
	}

	plain, _ := wal.Encode(nil, wal.Record{Tx: 8, TS: ts, Ops: []wal.Op{{Kind: wal.Del, ID: "a", V: 2}}})

	if bytes.Contains(plain, []byte("_exp")) {
		t.Fatalf("a plain delete must not carry _exp: %s", plain)
	}
}

func TestTruncateMarker(t *testing.T) {
	rec := wal.Record{Tx: 41, TS: time.Unix(0, 0).UTC(), Ops: []wal.Op{{Kind: wal.Trunc}}}

	line, err := wal.Encode(nil, rec)
	if err != nil {
		t.Fatal(err)
	}

	got, err := wal.Decode(line)
	if err != nil {
		t.Fatal(err)
	}

	if got.Tx != 41 || !got.IsTruncate() {
		t.Fatalf("got %+v", got)
	}

	if (wal.Record{Ops: []wal.Op{{Kind: wal.Del, ID: "a"}}}).IsTruncate() {
		t.Fatal("a del is not a truncate marker")
	}
}
