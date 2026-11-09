package jsondoc_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/thadeu/ndb/internal/jsondoc"
)

// Decode gives the same values as encoding/json with UseNumber.
func TestDecodeMatchesEncodingJSON(t *testing.T) {
	docs := []string{
		`{"a":1,"b":1.5,"c":-2e3,"d":"x\u00e9y","e":[1,"two",{"three":3}],"f":null,"g":true,"h":{"i":{"j":[]}}}`,
		`{"big":12345678901234567890,"tiny":1e-300,"neg":-0}`,
		`{"dup":1,"dup":2}`,
		"  {\"spaces\": \"ok\"}\n",
		`{"emoji":"\ud83d\ude00","esc":"\"\\\/\b\f\n\r\t"}`,
	}

	for _, d := range docs {
		got, err := jsondoc.Decode([]byte(d))
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}

		var want map[string]any

		dec := json.NewDecoder(bytes.NewReader([]byte(d)))
		dec.UseNumber()

		if err := dec.Decode(&want); err != nil {
			t.Fatal(err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\n got %#v\nwant %#v", d, got, want)
		}
	}
}

func TestDecodeRejectsWhatIsNotAnObject(t *testing.T) {
	for _, d := range []string{`[1]`, `"x"`, `{"a":`, ``} {
		if _, err := jsondoc.Decode([]byte(d)); err == nil {
			t.Fatalf("%q decoded", d)
		}
	}
}

func eslDoc() []byte {
	m := map[string]any{"type": "finishcall", "customer": "cust-042", "ts": "2026-11-07T10:11:12Z", "duration": 123, "call_id": "call-1-abcdef"}

	vars := map[string]any{}

	for i := range 21 {
		vars[fmt.Sprintf("variable_%d", i)] = fmt.Sprintf("%016x", i*7919)
	}

	m["variables"] = vars

	for i := range 15 {
		m[fmt.Sprintf("field_%d", i)] = fmt.Sprintf("value %d with some text", i)
	}

	b, _ := json.Marshal(m)

	return b
}

// Pick gives the same values as Decode for the keys it picks, and leaves out
// the keys a document does not have.
func TestPickMatchesDecode(t *testing.T) {
	raw := eslDoc()
	full, _ := jsondoc.Decode(raw)

	got, err := jsondoc.Pick(raw, []string{"ts", "variables", "duration", "nope"})
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := got["nope"]; ok || len(got) != 3 {
		t.Fatalf("Pick = %v", got)
	}

	for _, k := range []string{"ts", "variables", "duration"} {
		if !reflect.DeepEqual(got[k], full[k]) {
			t.Fatalf("%s: Pick %#v, Decode %#v", k, got[k], full[k])
		}
	}
}

func BenchmarkDecodeWhole(b *testing.B) {
	raw := eslDoc()

	for b.Loop() {
		_, _ = jsondoc.Decode(raw)
	}
}

func BenchmarkPickTwoFields(b *testing.B) {
	raw := eslDoc()

	for b.Loop() {
		_, _ = jsondoc.Pick(raw, []string{"ts", "type"})
	}
}
