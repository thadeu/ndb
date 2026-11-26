package collection_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
)

// Get answers the stored bytes with "_v" first, for a doc with its _id
// first (the scan) and for one with its _id later (the decode).
func TestGetAnswersTheDocWithItsVersion(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	for _, d := range []string{`{"_id":"a","n":1}`, `{"n":2,"_id":"b"}`, `{"_id":"c","sub":{"_id":"x"}}`} {
		if _, err := c.Insert(ctx, doc(d)); err != nil {
			t.Fatal(err)
		}
	}

	for id, want := range map[string]string{
		"a": `{"_v":1,"_id":"a","n":1}`,
		"b": `{"_v":1,"n":2,"_id":"b"}`,
		"c": `{"_v":1,"_id":"c","sub":{"_id":"x"}}`,
	} {
		got, ok := c.Get(id)
		if !ok || string(got.Doc) != want {
			t.Errorf("Get(%q) = %s, %v; want %s", id, got.Doc, ok, want)
		}
	}

	if _, ok := c.Get("x"); ok {
		t.Error("the _id of a sub-object must not answer")
	}
}

func TestGetOfADeletedDocAnswersNothing(t *testing.T) {
	c := open(t, t.TempDir(), catalog.Def{})

	if _, err := c.Insert(ctx, doc(`{"_id":"a","n":1}`)); err != nil {
		t.Fatal(err)
	}

	if _, err := c.DeleteID(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get("a"); ok {
		t.Fatal("a deleted doc answered")
	}
}

func TestGetOfAnExpiredDocAnswersNothing(t *testing.T) {
	clk := newClock()
	c := openTTL(t, catalog.Def{Time: "at", TTL: "1h"}, clk, false)

	if _, err := c.Insert(ctx, doc(fmt.Sprintf(`{"_id":"a","at":%q}`, rfc(clk.now())))); err != nil {
		t.Fatal(err)
	}

	if _, ok := c.Get("a"); !ok {
		t.Fatal("a fresh doc did not answer")
	}

	clk.advance(2 * time.Hour)

	if _, ok := c.Get("a"); ok {
		t.Fatal("an expired doc answered")
	}
}
