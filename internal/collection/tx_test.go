package collection_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/crashtest"
	"github.com/thadeu/ndb/internal/fs"
)

var txDef = catalog.Def{Name: "accounts"}

func txUpdate(id, ops string) collection.TxOp {
	dec := json.NewDecoder(strings.NewReader(ops))
	dec.UseNumber()

	var m map[string]any

	if err := dec.Decode(&m); err != nil {
		panic(err)
	}

	return collection.TxOp{Op: "update", ID: id, Update: m}
}

func TestTxIsOneWALRecord(t *testing.T) {
	c, err := collection.Open(collection.Config{Dir: t.TempDir(), FS: fs.OS{}, Def: txDef})
	if err != nil {
		t.Fatal(err)
	}

	defer c.Close()

	if _, err := c.Insert(ctx, doc(`{"_id":"a","balance":10}`), doc(`{"_id":"b","balance":0}`)); err != nil {
		t.Fatal(err)
	}

	before := c.LastTx()

	_, err = c.Tx(ctx, []collection.TxOp{
		txUpdate("a", `{"$inc":{"balance":-4}}`),
		txUpdate("b", `{"$inc":{"balance":4}}`),
		{Op: "insert", Doc: json.RawMessage(`{"_id":"c","balance":1}`)},
		{Op: "delete", ID: "c"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := c.LastTx() - before; got != 1 {
		t.Fatalf("the transaction made %d WAL records, want 1", got)
	}

	before = c.LastTx()

	_, err = c.Tx(ctx, []collection.TxOp{txUpdate("a", `{"$inc":{"balance":-1}}`), {Op: "delete", ID: "nope"}}, nil)

	var coded interface{ Code() string }

	if !errors.As(err, &coded) || coded.Code() != "tx_conflict" {
		t.Fatalf("err %v, want tx_conflict", err)
	}

	if c.LastTx() != before {
		t.Fatal("a failed transaction wrote a record")
	}

	if a, _ := c.Find("a"); !strings.Contains(string(a.Doc), `"balance":6`) {
		t.Fatalf("a = %s, want balance 6", a.Doc)
	}
}

const txTotal = 1_000_000

// transferChild moves 1 from a to b forever, in transactions that also
// count the moves in n, and acks the count of each committed one.
func transferChild() {
	c, err := collection.Open(collection.Config{Dir: os.Getenv("COL_DIR"), FS: fs.OS{}, Def: txDef})
	if err != nil {
		panic(err)
	}

	if _, ok := c.Find("a"); !ok {
		if _, err := c.Insert(ctx, doc(`{"_id":"a","balance":1000000}`), doc(`{"_id":"b","balance":0}`), doc(`{"_id":"n","moves":0}`)); err != nil {
			panic(err)
		}
	}

	for moves := int64(1); ; moves++ {
		_, err := c.Tx(ctx, []collection.TxOp{
			txUpdate("a", `{"$inc":{"balance":-1}}`),
			txUpdate("b", `{"$inc":{"balance":1}}`),
			txUpdate("n", `{"$inc":{"moves":1}}`),
		}, nil)
		if err != nil {
			panic(err)
		}

		crashtest.Ack(moves)
	}
}

func TestTransactionsAreWholeAfterKills(t *testing.T) {
	if testing.Short() {
		t.Skip("heavy: it runs without -short (make test), not in CI")
	}

	dir := t.TempDir()

	crashtest.Run(t, crashtest.Config{
		Child:      "transfer",
		Env:        []string{"COL_DIR=" + dir},
		Iterations: *iterations,
		MaxDelay:   20 * time.Millisecond,
		Check: func(t *testing.T, acked int64) {
			c, err := collection.Open(collection.Config{Dir: dir, FS: fs.OS{}, Def: txDef})
			if err != nil {
				t.Errorf("reopen: %v", err)

				return
			}

			defer c.Close()

			a, b, n := txField(t, c, "a", "balance"), txField(t, c, "b", "balance"), txField(t, c, "n", "moves")

			if a+b != txTotal || b != n {
				t.Errorf("half a transaction: a=%d b=%d moves=%d (want a+b=%d and b=moves)", a, b, n, txTotal)
			}

			if n < acked {
				t.Errorf("acked %d moves, found %d", acked, n)
			}
		},
	})
}

func txField(t *testing.T, c *collection.Collection, id, name string) int64 {
	t.Helper()

	d, ok := c.Find(id)
	if !ok {
		if id == "a" {
			return txTotal
		}

		return 0
	}

	dec := json.NewDecoder(strings.NewReader(string(d.Doc)))
	dec.UseNumber()

	var m map[string]any

	if err := dec.Decode(&m); err != nil {
		t.Fatalf("%s: %v", id, err)
	}

	num, ok := m[name].(json.Number)
	if !ok {
		t.Fatalf("%s has no number %s: %s", id, name, d.Doc)
	}

	n, _ := num.Int64()

	return n
}
