package collection

import (
	"encoding/json"
	"strings"
	"testing"
)

// slowOwnID is the check of Get before M29: a full decode of the doc.
func slowOwnID(doc []byte, id string) bool {
	var own struct {
		ID string `json:"_id"`
	}

	return json.Unmarshal(doc, &own) == nil && own.ID == id
}

// The docs are valid JSON: a stored document always is.
func TestOwnIDGivesTheAnswerOfAFullDecode(t *testing.T) {
	docs := []string{
		`{"_id":"a1","n":1}`,
		`{"_id":"a1"}`,
		`{ "_id" : "a1" , "n":1}`,
		"{\n\t\"_id\":\"a1\"\n}",
		`{"n":1,"_id":"a1"}`,
		`{"n":1}`,
		`{}`,
		`{"_id":"a1","_id":"b2"}`,
		`{"_id":"b2","_id":"a1"}`,
		`{"_id":"a1","sub":{"_id":"x"}}`,
		`{"sub":{"_id":"a1"}}`,
		`{"_id":"a1x"}`,
		`{"_id":"a"}`,
		`{"_id":"\u0061\u0031"}`,
		`{"_id":1}`,
		`{"_id":null}`,
		`{"_id":"a1","note":"_id"}`,
		`{"_id":"a1","note":"\"_id\""}`,
		`{"_idx":"a1"}`,
		`[{"_id":"a1"}]`,
	}

	for _, doc := range docs {
		for _, id := range []string{"a1", "a", "b2", "x", "", "a\"1", "\xff"} {
			if got, want := ownID([]byte(doc), id), slowOwnID([]byte(doc), id); got != want {
				t.Errorf("ownID(%s, %q) = %v, want %v", doc, id, got, want)
			}
		}
	}
}

func TestOwnIDTakesTheFastPathWithNoAlloc(t *testing.T) {
	doc := []byte(`{"_id":"a1","n":1,"note":"a short note"}`)

	if n := testing.AllocsPerRun(100, func() { ownID(doc, "a1") }); n != 0 {
		t.Fatalf("allocs = %v, want 0", n)
	}
}

// BenchmarkOwnID is the check of the own _id on a 1 KB doc, before (decode)
// and after (scan).
func BenchmarkOwnID(b *testing.B) {
	doc := []byte(`{"_id":"a1","note":"` + strings.Repeat("x", 1000) + `","n":1}`)

	b.Run("decode", func(b *testing.B) {
		b.ReportAllocs()

		for range b.N {
			slowOwnID(doc, "a1")
		}
	})

	b.Run("scan", func(b *testing.B) {
		b.ReportAllocs()

		for range b.N {
			ownID(doc, "a1")
		}
	})
}

func FuzzOwnID(f *testing.F) {
	f.Add([]byte(`{"_id":"a1","n":1}`), "a1")
	f.Add([]byte(`{"_id":"a1","_id":"b"}`), "a1")
	f.Add([]byte(`{"_id":"a1" ,"x":{"_id":"a1"}}`), "a1")

	f.Fuzz(func(t *testing.T, doc []byte, id string) {
		if !json.Valid(doc) {
			return
		}

		if got, want := ownID(doc, id), slowOwnID(doc, id); got != want {
			t.Fatalf("ownID(%s, %q) = %v, want %v", doc, id, got, want)
		}
	})
}
