package crypt

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func testBox(t *testing.T) *Box {
	t.Helper()

	b, err := New("k1", bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestSealOpenKeepsTheJSONType(t *testing.T) {
	b := testBox(t)

	for _, v := range []string{`"12345678909"`, `42`, `{"a":[1,2]}`, `null`} {
		sealed, err := b.Seal("users/cpf", json.RawMessage(v))
		if err != nil {
			t.Fatal(err)
		}

		if !strings.HasPrefix(sealed, "ndb:e1:k1:") || strings.Contains(sealed, "1234") {
			t.Fatalf("sealed %q", sealed)
		}

		plain, err := b.Open("users/cpf", sealed)
		if err != nil || string(plain) != v {
			t.Fatalf("open %s, %v; want %s", plain, err, v)
		}
	}
}

func TestSealUsesANewNonceEachTime(t *testing.T) {
	b := testBox(t)

	x, _ := b.Seal("users/cpf", json.RawMessage(`"1"`))
	y, _ := b.Seal("users/cpf", json.RawMessage(`"1"`))

	if x == y {
		t.Fatal("same ciphertext twice")
	}
}

func TestOpenRefusesAnotherPlaceOrABadValue(t *testing.T) {
	b := testBox(t)
	sealed, _ := b.Seal("users/cpf", json.RawMessage(`"1"`))

	bad := []struct{ place, val string }{
		{"users/rg", sealed},
		{"users/cpf", sealed[:len(sealed)-2]},
		{"users/cpf", "ndb:e1:k2:" + strings.TrimPrefix(sealed, "ndb:e1:k1:")},
		{"users/cpf", "ndb:e1:k1:%%%"},
		{"users/cpf", "plain"},
	}

	for _, c := range bad {
		if _, err := b.Open(c.place, c.val); !errors.Is(err, ErrOpen) {
			t.Fatalf("open %q at %s: %v, want ErrOpen", c.val, c.place, err)
		}
	}
}

func TestHashNormalizesAndSeparatesPlaces(t *testing.T) {
	b := testBox(t)

	a, err := b.Hash("users/cpf", Digits, json.RawMessage(`"123.456.789-09"`))
	if err != nil {
		t.Fatal(err)
	}

	same, _ := b.Hash("users/cpf", Digits, json.RawMessage(`"12345678909"`))
	other, _ := b.Hash("admins/cpf", Digits, json.RawMessage(`"12345678909"`))
	mail1, _ := b.Hash("users/email", Lower, json.RawMessage(`"A@B.com"`))
	mail2, _ := b.Hash("users/email", Lower, json.RawMessage(`"a@b.com"`))
	exact, _ := b.Hash("users/email", Exact, json.RawMessage(`"A@B.com"`))

	switch {
	case !strings.HasPrefix(a, "ndb:h1:k1:") || len(a) != len("ndb:h1:k1:")+32:
		t.Fatalf("hash %q", a)
	case a != same:
		t.Fatal("digits did not normalize")
	case a == other:
		t.Fatal("two collections share a hash")
	case mail1 != mail2 || exact == mail2:
		t.Fatal("lower or exact is wrong")
	}
}

func TestIsSealed(t *testing.T) {
	if !IsSealed(json.RawMessage(`"ndb:e1:k1:abc"`)) || IsSealed(json.RawMessage(`"abc"`)) || IsSealed(json.RawMessage(`1`)) {
		t.Fatal("IsSealed")
	}
}
