package catalog_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
)

func TestEncryptedFieldsAreAddedAndKept(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	if _, err := c.Ensure(catalog.Def{Name: "users", Encrypted: []string{"cpf"}}, catalog.FromCode); err != nil {
		t.Fatal(err)
	}

	def, err := c.Ensure(catalog.Def{Name: "users", Encrypted: []string{"token"}}, catalog.FromCode)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(def.Encrypted, []string{"cpf", "token"}) {
		t.Fatalf("encrypted %v, want cpf and token", def.Encrypted)
	}
}

func TestEncryptedRefusesWhatCannotWork(t *testing.T) {
	bad := []catalog.Def{
		{Name: "a", Encrypted: []string{"_id"}},
		{Name: "b", Encrypted: []string{"at"}, Time: "at"},
		{Name: "c", Encrypted: []string{"exp"}, ExpiresField: "exp"},
		{Name: "d", Encrypted: []string{"cpf"}, Indexes: []string{"cpf"}},
		{Name: "e", Encrypted: []string{"cpf"}, Unique: []string{"org,cpf"}},
		{Name: "f", Encrypted: []string{"cpf"}, Write: catalog.Direct, Path: "/tmp/f"},
		{Name: "g", Encrypted: []string{"a.b"}},
		{Name: "h", Encrypted: []string{""}},
	}

	for _, d := range bad {
		c, _ := open(t, catalog.Options{})

		if _, err := c.Ensure(d, catalog.FromCode); !errors.Is(err, catalog.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", d.Name, err)
		}
	}
}

func TestBlindFieldsIndexTheirHash(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	def, err := c.Ensure(catalog.Def{
		Name:      "users",
		Encrypted: []string{"cpf", "email"},
		Blind:     map[string]string{"cpf": "digits", "email": ""},
		Unique:    []string{"cpf", "org,email"},
	}, catalog.FromCode)
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(def.Unique, []string{"cpf_bidx", "org,email_bidx"}) || def.Blind["email"] != "exact" {
		t.Fatalf("unique %v blind %v", def.Unique, def.Blind)
	}

	def, err = c.Ensure(catalog.Def{Name: "users", Indexes: []string{"email"}}, catalog.FromCode)
	if err != nil || !slices.Equal(def.Indexes, []string{"email_bidx"}) {
		t.Fatalf("indexes %v %v", def.Indexes, err)
	}

	if _, err := c.Ensure(catalog.Def{Name: "users", Blind: map[string]string{"cpf": "lower"}}, catalog.FromCode); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("a changed normalization: %v, want ErrConflict", err)
	}
}

func TestBlindIsEncryptedAndNeedsAKnownNorm(t *testing.T) {
	c, _ := open(t, catalog.Options{})

	def, err := c.Ensure(catalog.Def{Name: "a", Blind: map[string]string{"cpf": "digits"}}, catalog.FromCode)
	if err != nil || !slices.Equal(def.Encrypted, []string{"cpf"}) {
		t.Fatalf("encrypted %v, err %v", def.Encrypted, err)
	}

	for _, d := range []catalog.Def{
		{Name: "b", Encrypted: []string{"cpf"}, Blind: map[string]string{"cpf": "upper"}},
		{Name: "c", Blind: map[string]string{"_id": "exact"}},
	} {
		c, _ := open(t, catalog.Options{})

		if _, err := c.Ensure(d, catalog.FromCode); !errors.Is(err, catalog.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", d.Name, err)
		}
	}
}
