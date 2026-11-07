package ndb

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestRawFilters(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	calls, err := Collection[Call](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.InsertMany(ctx, []Call{{CallID: "c1", Status: "answered", Duration: 10}, {CallID: "c2", Status: "failed", Duration: 40}, {CallID: "c3", Status: "answered", Duration: 50}}); err != nil {
		t.Fatal(err)
	}

	form, err := RawQueryJSON([]byte(`{"status":{"$in":["answered","failed"]},"duration":{"$gte":30}}`))
	if err != nil {
		t.Fatal(err)
	}

	rows, err := calls.Query(form).Sort(Asc("duration")).All(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if got := callIDs(rows); !slices.Equal(got, []string{"c2", "c3"}) {
		t.Fatalf("RawJSON = %v", got)
	}

	// Raw mixes with the builders; a key in both goes to $and.
	rows, err = calls.Query(RawQuery(map[string]any{"status": "answered"}), Gte("duration", 20)).All(ctx)
	if err != nil || !slices.Equal(callIDs(rows), []string{"c3"}) {
		t.Fatalf("Raw + Gte = %v, %v", callIDs(rows), err)
	}

	for _, bad := range []string{`[]`, `"x"`, `{`, `null`} {
		if _, err := RawQueryJSON([]byte(bad)); err == nil {
			t.Errorf("RawQueryJSON(%s): want an error", bad)
		}
	}
}

func TestFindBody(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	calls, err := Collection[Call](db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := calls.InsertMany(ctx, []Call{{CallID: "c1", Status: "answered", Duration: 10}, {CallID: "c2", Status: "answered", Duration: 40}, {CallID: "c3", Status: "answered", Duration: 50}}); err != nil {
		t.Fatal(err)
	}

	body := `{"query":{"status":"answered"},"sort":{"duration":-1},"limit":2,"count":true}`

	page, err := calls.Search(ctx, []byte(body))
	if err != nil {
		t.Fatal(err)
	}

	if got := callIDs(page.Rows); !slices.Equal(got, []string{"c3", "c2"}) || page.Total != 3 || page.Next == "" {
		t.Fatalf("page 1 = %v, total %d, next %q", got, page.Total, page.Next)
	}

	page, err = calls.Search(ctx, []byte(`{"query":{"status":"answered"},"sort":{"duration":-1},"limit":2,"after":"`+page.Next+`"}`))
	if err != nil {
		t.Fatal(err)
	}

	if got := callIDs(page.Rows); !slices.Equal(got, []string{"c1"}) || page.Next != "" {
		t.Fatalf("page 2 = %v, next %q", got, page.Next)
	}

	_, err = calls.Search(ctx, []byte(`{"query":{"status":{"$bad":1}}}`))
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("bad operator: %v", err)
	}

	if _, err := calls.Search(ctx, []byte(`[1]`)); err == nil {
		t.Fatal("a non-object body must fail")
	}
}

func callIDs(rows []Call) []string {
	out := make([]string, len(rows))

	for i, r := range rows {
		out[i] = r.CallID
	}

	return out
}
