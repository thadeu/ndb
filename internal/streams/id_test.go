package streams

import (
	"strings"
	"testing"
)

func TestKeyOrderIsIDOrder(t *testing.T) {
	ids := []ID{{1, 9}, {1, 10}, {2, 0}, {10, 0}, {Max.Ms, Max.Seq}}

	for i := 1; i < len(ids); i++ {
		if ids[i-1].Key() >= ids[i].Key() {
			t.Fatalf("key of %s is not before key of %s", ids[i-1], ids[i])
		}

		if back, ok := FromKey(ids[i].Key()); !ok || back != ids[i] {
			t.Fatalf("FromKey(Key(%s)) = %s, %v", ids[i], back, ok)
		}
	}
}

func TestParse(t *testing.T) {
	cases := map[string]ID{"5-3": {5, 3}, "-": {}, "+": Max}

	for in, want := range cases {
		if got, err := Parse(in, 0); err != nil || got != want {
			t.Fatalf("Parse(%q) = %s, %v", in, got, err)
		}
	}

	if got, _ := Parse("7", Max.Seq); got != (ID{7, Max.Seq}) {
		t.Fatalf("an end of 7 is %s", got)
	}

	for _, bad := range []string{"", "x", "1-x", "-1-2", "1-2-3"} {
		if _, err := Parse(bad, 0); err == nil {
			t.Fatalf("Parse(%q) took a bad ID", bad)
		}
	}
}

func TestUnpad(t *testing.T) {
	row := []byte(`{"_id":"` + (ID{7, 12}).Key() + `","n":1,"ref":{"_id":"x"}}`)

	if got := string(Unpad(row)); got != `{"_id":"7-12","n":1,"ref":{"_id":"x"}}` {
		t.Fatalf("Unpad = %s", got)
	}
}

// PadQuery rewrites only the query: the keys of sort keep their order.
func TestPadQueryKeepsTheSortOrder(t *testing.T) {
	out, err := PadQuery([]byte(`{"query":{"_id":{"$gte":"7","$lte":"7"},"$or":[{"_id":"8-1"}]},"sort":{"z":1,"a":-1,"_id":1}}`))
	if err != nil {
		t.Fatal(err)
	}

	got := string(out)

	for _, want := range []string{(ID{7, 0}).Key(), (ID{7, Max.Seq}).Key(), (ID{8, 1}).Key(), `"sort":{"z":1,"a":-1,"_id":1}`} {
		if !strings.Contains(got, want) {
			t.Fatalf("PadQuery = %s; want %s in it", got, want)
		}
	}
}

// FuzzParse: any text is an ID or an error, never a panic, and an ID comes
// back from its key and from its text.
func FuzzParse(f *testing.F) {
	for _, s := range []string{"1-0", "-", "+", "18446744073709551615-18446744073709551615", "7", "1-", "-1", "0-0", "a-b"} {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		id, err := Parse(s, 0)
		if err != nil {
			return
		}

		if back, ok := FromKey(id.Key()); !ok || back != id {
			t.Fatalf("%q: key %q gives %v, %v", s, id.Key(), back, ok)
		}

		if again, err := Parse(id.String(), 0); err != nil || again != id {
			t.Fatalf("%q: text %q gives %v, %v", s, id.String(), again, err)
		}
	})
}
