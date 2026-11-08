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
