package query_test

import (
	"testing"

	"github.com/thadeu/ndb/internal/query"
)

// A string in RFC 3339 with a time is an instant on every field, not only
// the time field: two widths or two offsets of one instant are equal.
func TestTimeStringsCompareAsInstants(t *testing.T) {
	cases := []struct {
		where, doc string
		want       bool
	}{
		{`{"next_at":{"$lte":"2026-11-12T15:00:05.100Z"}}`, `{"next_at":"2026-11-12T15:00:05Z"}`, true},
		{`{"next_at":{"$gt":"2026-11-12T15:00:05Z"}}`, `{"next_at":"2026-11-12T15:00:05.100Z"}`, true},
		{`{"next_at":"2026-11-12T15:00:05.000Z"}`, `{"next_at":"2026-11-12T12:00:05-03:00"}`, true},
		{`{"next_at":{"$in":["2026-11-12T15:00:05Z"]}}`, `{"next_at":"2026-11-12T15:00:05.000000000Z"}`, true},
		{`{"next_at":{"$lt":"2026-11-12T15:00:05Z"}}`, `{"next_at":"2026-11-12T12:00:06-03:00"}`, false},
		{`{"next_at":{"$regex":"^2026-11"}}`, `{"next_at":"2026-11-12T15:00:05Z"}`, true},
	}

	for _, c := range cases {
		if got := match(t, c.where, c.doc); got != c.want {
			t.Errorf("%s on %s: got %v, want %v", c.where, c.doc, got, c.want)
		}
	}
}

// Text that only looks like a part of a time stays text.
func TestTextThatIsNotATimeStaysText(t *testing.T) {
	for _, s := range []string{"2026-11-12", "2026-11-12T15:00", "not a time at all, long enough", "2026-13-45T99:00:05Z"} {
		if v := query.FromJSON(s, false); v.Kind != query.String {
			t.Errorf("%q: kind %v, want String", s, v.Kind)
		}
	}

	if v := query.FromJSON("2026-11-12T15:00:05Z", false); v.Kind != query.Time {
		t.Errorf("an RFC 3339 time: kind %v, want Time", v.Kind)
	}
}

// _id is text: an id that looks like a time is found by its exact text.
func TestAnIDThatLooksLikeATimeStaysText(t *testing.T) {
	doc := `{"_id":"2026-11-12T15:00:05Z"}`

	if !match(t, `{"_id":"2026-11-12T15:00:05Z"}`, doc) {
		t.Error("the same id must match")
	}

	if match(t, `{"_id":"2026-11-12T15:00:05.000Z"}`, doc) {
		t.Error("another text is another id")
	}

	if vals := query.Elements(decode(t, doc), "_id", false); len(vals) != 1 || vals[0].Kind != query.String {
		t.Errorf("_id elements: %+v", vals)
	}
}
