package crypt

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func rotated(t *testing.T) (*Box, string, string) {
	t.Helper()

	b := testBox(t)

	sealed, err := b.Seal("users/cpf", json.RawMessage(`"390"`))
	if err != nil {
		t.Fatal(err)
	}

	oldHash, _ := b.Hash("users/cpf", Numeric, json.RawMessage(`"390"`))

	if err := b.Rotate("k2", bytes.Repeat([]byte{3}, 32), bytes.Repeat([]byte{4}, 32)); err != nil {
		t.Fatal(err)
	}

	return b, sealed, oldHash
}

// After a rotation, new values use k2 and the k1 values still open.
func TestARotatedBoxSealsWithTheNewKeyAndOpensTheOld(t *testing.T) {
	b, old, _ := rotated(t)

	if b.ID() != "k2" || !b.Rotating() {
		t.Fatalf("id %s rotating %v", b.ID(), b.Rotating())
	}

	sealed, _ := b.Seal("users/cpf", json.RawMessage(`"390"`))

	if !strings.HasPrefix(sealed, "ndb:e1:k2:") || b.Prefix() != "ndb:e1:k2:" {
		t.Fatalf("sealed %s prefix %s", sealed, b.Prefix())
	}

	for _, s := range []string{old, sealed} {
		if plain, err := b.Open("users/cpf", s); err != nil || string(plain) != `"390"` {
			t.Errorf("open %s: %s %v", s, plain, err)
		}
	}

	b.DropOld()

	if _, err := b.Open("users/cpf", old); err == nil || b.Rotating() {
		t.Error("the old key still opens after DropOld")
	}
}

// SealDoc and HashDoc move a k1 value to k2; a k2 value stays.
func TestADocOfTheOldKeyIsSealedAgain(t *testing.T) {
	b, old, oldHash := rotated(t)

	doc := json.RawMessage(`{"_id":"u1","cpf":"` + old + `","cpf_bidx":"` + oldHash + `"}`)
	blind := map[string]Norm{"cpf": Numeric}

	doc, err := b.HashDoc("users", blind, doc)
	if err != nil {
		t.Fatal(err)
	}

	doc, err = b.SealDoc("users", []string{"cpf"}, doc)
	if err != nil {
		t.Fatal(err)
	}

	newHash, _ := b.Hash("users/cpf", Numeric, json.RawMessage(`"390"`))

	if !strings.Contains(string(doc), `"cpf":"ndb:e1:k2:`) || !strings.Contains(string(doc), newHash) {
		t.Fatalf("doc %s", doc)
	}

	again, _ := b.SealDoc("users", []string{"cpf"}, doc)

	if !bytes.Equal(again, doc) {
		t.Errorf("a k2 value was sealed again: %s", again)
	}

	if h, ok := b.OldHash("users/cpf", Numeric, json.RawMessage(`"390"`)); !ok || h != oldHash {
		t.Errorf("old hash %s %v, want %s", h, ok, oldHash)
	}
}
