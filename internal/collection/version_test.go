package collection

import (
	"encoding/json"
	"testing"
)

// A doc stored with its own "_v" (before the write refused it) answers one
// "_v": the version.
func TestWithVersionDropsAStoredVersion(t *testing.T) {
	cases := map[string]string{
		`{"_v":99,"_id":"a","n":1}`: `{"_v":2,"_id":"a","n":1}`,
		`{"_id":"a","_v":99,"n":1}`: `{"_v":2,"_id":"a","n":1}`,
		`{"_id":"a","n":1,"_v":99}`: `{"_v":2,"_id":"a","n":1}`,
		`{"_v":{"x":1}}`:            `{"_v":2}`,
		`{"_id":"a","m":{"_v":5}}`:  `{"_v":2,"_id":"a","m":{"_v":5}}`,
		`{"_id":"a","s":"_v"}`:      `{"_v":2,"_id":"a","s":"_v"}`,
	}

	for in, want := range cases {
		got := string(withVersion(json.RawMessage(in), 2))

		if got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

func TestNumCompareIsExact(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1152921504606846976", "1152921504606846977", -1},
		{"9223372036854775807", "9223372036854775806", 1},
		{"92233720368547758070", "92233720368547758071", -1},
		{"1.5", "1.50", 0},
		{"1", "1.0", 0},
		{"2", "1.5", 1},
	}

	for _, c := range cases {
		got, ok := numCompare(json.Number(c.a), json.Number(c.b))

		if !ok || got != c.want {
			t.Errorf("%s vs %s = %d %v, want %d", c.a, c.b, got, ok, c.want)
		}
	}
}
