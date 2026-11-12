package ndb

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ndb.Time is written in UTC with milliseconds, always the same width, as
// the other SDKs write a time. So two times compare in the right order also
// as text.
func TestTimeIsWrittenWithAFixedWidth(t *testing.T) {
	zone := time.FixedZone("BRT", -3*3600)

	cases := map[string]time.Time{
		`"2026-11-12T15:00:05.000Z"`: time.Date(2026, 11, 12, 15, 0, 5, 0, time.UTC),
		`"2026-11-12T15:00:05.100Z"`: time.Date(2026, 11, 12, 15, 0, 5, 100_000_000, time.UTC),
		`"2026-11-12T15:00:05.123Z"`: time.Date(2026, 11, 12, 12, 0, 5, 123_999_999, zone),
	}

	for want, at := range cases {
		raw, err := json.Marshal(Time{at})

		if err != nil || string(raw) != want {
			t.Errorf("%v: got %s %v, want %s", at, raw, err, want)
		}
	}
}

func TestTimeReadsAnyRFC3339(t *testing.T) {
	want := time.Date(2026, 11, 12, 15, 0, 5, 100_000_000, time.UTC)

	for _, in := range []string{`"2026-11-12T15:00:05.1Z"`, `"2026-11-12T12:00:05.100-03:00"`, `"2026-11-12T15:00:05.100000000Z"`} {
		var got Time

		if err := json.Unmarshal([]byte(in), &got); err != nil || !got.Equal(want) {
			t.Errorf("%s: got %v %v", in, got, err)
		}
	}

	var empty Time

	if err := json.Unmarshal([]byte(`null`), &empty); err != nil || !empty.IsZero() {
		t.Errorf("null: got %v %v", empty, err)
	}
}

// A time.Time in a filter goes out with the same width.
func TestAFilterWritesATimeWithAFixedWidth(t *testing.T) {
	at := time.Date(2026, 11, 12, 15, 0, 5, 0, time.UTC)
	ptr := &at

	for _, f := range []Filter{Gte("at", at), Lte("at", ptr), Eq("at", at), In("at", at)} {
		raw, _ := json.Marshal(queryOf([]Filter{f}))

		if !json.Valid(raw) || !strings.Contains(string(raw), `"2026-11-12T15:00:05.000Z"`) {
			t.Errorf("filter %s", raw)
		}
	}
}

type stamped struct {
	ID string `json:"_id,omitempty"`
	At Time   `json:"at"`
}

func TestTimeRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	c, _ := Collection[stamped](db, newColl())
	at := time.Date(2026, 11, 12, 15, 0, 5, 0, time.UTC)

	if _, err := c.Insert(ctx, stamped{ID: "a", At: Time{at}}); err != nil {
		t.Fatal(err)
	}

	got, err := c.Query(Gte("at", at.Add(-time.Second)), Lte("at", at)).All(ctx)

	if err != nil || len(got) != 1 || !got[0].At.Equal(at) {
		t.Fatalf("got %v %v", got, err)
	}
}

type plainTimes struct {
	At   time.Time  `ndb:"at"`
	Next *time.Time `ndb:"next"`
	Gone *time.Time `ndb:"gone"`
}

// A time.Time field of the document type goes out with the fixed width:
// the app does not need Time for it.
func TestATimeFieldIsWrittenWithAFixedWidth(t *testing.T) {
	s, _ := schemaOf(reflect.TypeFor[plainTimes]())
	at := time.Date(2026, 11, 12, 12, 0, 5, 0, time.FixedZone("BRT", -3*3600))
	raw, err := s.encode(plainTimes{At: at, Next: &at})

	if err != nil {
		t.Fatal(err)
	}

	want := `{"at":"2026-11-12T15:00:05.000Z","gone":null,"next":"2026-11-12T15:00:05.000Z"}`

	if string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
}
