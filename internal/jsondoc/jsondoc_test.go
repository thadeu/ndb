package jsondoc_test

import (
	"bytes"
	"encoding/json"
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
