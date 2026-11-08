package ndb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
)

func TestOpenRejectsUnknownAddress(t *testing.T) {
	for _, addr := range []string{"ftp://127.0.0.1:7070", "unix://", "/run/ndb.sock", "https://"} {
		if _, err := Connect(addr); err == nil {
			t.Errorf("Connect(%q): want an error", addr)
		}
	}
}

func TestNDJSONRows(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
		fail bool
	}{
		{name: "three rows", in: "{\"a\":1}\n{\"a\":2}\n{\"a\":3}\n", want: 3},
		{name: "empty body", in: "", want: 0},
		{name: "last row cut short", in: "{\"a\":1}\n{\"a\":", want: 1, fail: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got int

			var gotErr error

			for _, err := range ndjsonRows(strings.NewReader(tc.in)) {
				if err != nil {
					gotErr = err

					break
				}

				got++
			}

			if got != tc.want {
				t.Fatalf("rows: got %d, want %d", got, tc.want)
			}

			if (gotErr != nil) != tc.fail {
				t.Fatalf("error: got %v, want failure %v", gotErr, tc.fail)
			}
		})
	}
}

func TestFindStreamsRowsOverBothTransports(t *testing.T) {
	transports := []struct {
		name string
		addr string
	}{
		{name: "unix", addr: daemon.unix},
		{name: "tcp", addr: daemon.tcp},
	}

	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			db := openTest(t, tr.addr)
			name := newColl()

			rawPut(t, db, name, `{}`)
			mustInsert(t, db, name, `[{"_id":"a"},{"_id":"b"},{"_id":"c"}]`)

			rows := rawFind(t, db, name, `{"sort":{"_id":1}}`)

			if len(rows) != 3 {
				t.Fatalf("rows: got %d, want 3", len(rows))
			}

			var first struct {
				ID string `json:"_id"`
			}

			if err := json.Unmarshal(rows[0], &first); err != nil || first.ID != "a" {
				t.Fatalf("first row: got %s (%v), want id a", rows[0], err)
			}
		})
	}
}

func TestKeepAliveReusesConnection(t *testing.T) {
	db := openTest(t, daemon.unix)

	first, err := db.do(context.Background(), http.MethodGet, "/_health", nil)
	if err != nil {
		t.Fatal(err)
	}

	discard(first)

	var reused bool

	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			reused = info.Reused
		},
	}

	ctx := httptrace.WithClientTrace(context.Background(), trace)

	second, err := db.do(ctx, http.MethodGet, "/_health", nil)
	if err != nil {
		t.Fatal(err)
	}

	discard(second)

	if !reused {
		t.Fatal("second request did not reuse the connection")
	}
}

func TestContextIsHonoured(t *testing.T) {
	db := openTest(t, daemon.unix)

	ctx, cancel := context.WithCancel(context.Background())

	cancel()

	_, err := db.do(ctx, http.MethodGet, "/_health", nil)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestErrorsAreTyped(t *testing.T) {
	db := openTest(t, daemon.unix)
	name := newColl()

	rawPut(t, db, name, `{}`)
	mustInsert(t, db, name, `[{"_id":"a","n":1}]`)

	body := []byte(`{"query":{"_id":"a"},"update":{"$bogus":{"n":2}}}`)

	_, err := db.do(context.Background(), http.MethodPost, collPath(name)+"/_update_by_query", body)

	var apiErr *Error

	if !errors.As(err, &apiErr) {
		t.Fatalf("want *Error, got %T: %v", err, err)
	}

	if apiErr.Code != "unknown_operator" || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("got code %q status %d, want unknown_operator 400", apiErr.Code, apiErr.Status)
	}

	if !errors.Is(err, ErrUnknownOperator) {
		t.Fatal("errors.Is(err, ErrUnknownOperator) = false")
	}

	if errors.Is(err, ErrNotFound) {
		t.Fatal("an unknown operator must not match ErrNotFound")
	}
}

func TestUnknownCollectionError(t *testing.T) {
	db := openTest(t, daemon.unix)

	_, err := db.do(context.Background(), http.MethodPost, collPath(newColl())+"/_search", []byte(`{}`))

	if !errors.Is(err, ErrUnknownCollection) {
		t.Fatalf("want ErrUnknownCollection, got %v", err)
	}
}

// rawFind runs a find with a JSON body and returns every row.
func rawFind(t *testing.T, db *DB, name, body string) []json.RawMessage {
	t.Helper()

	resp, err := db.do(context.Background(), http.MethodPost, collPath(name)+"/_search", []byte(body))
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()

	var rows []json.RawMessage

	for row, err := range ndjsonRows(resp.Body) {
		if err != nil {
			t.Fatal(err)
		}

		rows = append(rows, row)
	}

	return rows
}
