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
