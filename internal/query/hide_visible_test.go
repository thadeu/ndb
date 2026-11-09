package query

import (
	"slices"
	"testing"
	"time"
)

// Visible is the request without what Hide added: its where and its paths.
func TestVisibleDropsWhatHideAdded(t *testing.T) {
	q, err := Parse([]byte(`{"query":{"type":"finishcall"}}`), Options{TimeField: "ts"})
	if err != nil {
		t.Fatal(err)
	}

	if err := q.Hide(ExpiredWhere("ts", time.Now())); err != nil {
		t.Fatal(err)
	}

	v := q.Visible()

	if !slices.Equal(q.Paths(), []string{"type", "ts"}) || !slices.Equal(v.Paths(), []string{"type"}) {
		t.Fatalf("paths: hidden %v, visible %v", q.Paths(), v.Paths())
	}

	if !v.Exact("type") || !q.Exact("type") {
		t.Fatal("the request is an exact condition on type, with or without the hidden rows")
	}

	if !v.Match(map[string]any{"type": "finishcall", "ts": "1999-01-01T00:00:00Z"}) || q.Match(map[string]any{"type": "finishcall", "ts": "1999-01-01T00:00:00Z"}) {
		t.Fatal("only the hidden query hides an expired row")
	}
}

// TimeOnly is a request whose only condition is on the time field, with time
// values; the hidden retention does not count.
func TestTimeOnly(t *testing.T) {
	for body, want := range map[string]bool{
		`{"query":{"ts":{"$gte":"2026-10-01T00:00:00Z","$lt":"2026-11-01T00:00:00Z"}}}`: true,
		`{"query":{"ts":{"$lt":"2026-11-01T00:00:00Z"},"type":"x"}}`:                    false,
		`{"query":{"type":"x"}}`:                          false,
		`{"query":{"ts":{"$ne":"2026-11-01T00:00:00Z"}}}`: false,
		`{}`: false,
	} {
		q, err := Parse([]byte(body), Options{TimeField: "ts"})
		if err != nil {
			t.Fatal(err)
		}

		if err := q.Hide(ExpiredWhere("ts", time.Now().AddDate(-1, 0, 0))); err != nil {
			t.Fatal(err)
		}

		if got := q.TimeOnly(); got != want {
			t.Errorf("%s: TimeOnly = %v, want %v", body, got, want)
		}
	}
}
