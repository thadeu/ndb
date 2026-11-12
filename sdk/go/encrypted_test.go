package ndb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type Person struct {
	ID  string `ndb:"_id"`
	CPF string `ndb:"cpf"`
	Pem string `ndb:"pem"`
}

func TestEncryptedAndBlindOptions(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	col, err := Collection[Person](db, newColl(), Encrypted("pem"), Blind("cpf", "numeric"), Unique("cpf"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Person{ID: "p1", CPF: "390.533.447-05", Pem: "PRIVATE"}); err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, Person{ID: "p2", CPF: "39053344705"}); !errors.Is(err, ErrUniqueViolation) {
		t.Fatalf("err %v, want ErrUniqueViolation", err)
	}

	got, err := col.Query(Eq("cpf", "39053344705")).All(ctx)
	if err != nil || len(got) != 1 || got[0].Pem != "PRIVATE" || got[0].CPF != "390.533.447-05" {
		t.Fatalf("found %+v, err %v", got, err)
	}

	bad, _ := Collection[Person](db, newColl(), Blind("cpf", "upper"))

	if _, err := bad.Insert(ctx, Person{ID: "p1"}); err == nil {
		t.Fatal("the daemon took a bad normalization")
	}
}

func TestEncryptedHandleKeepsTheValueSealed(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)
	name := newColl()

	vault, err := Collection[Person](db, name, Encrypted("pem"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := vault.Insert(ctx, Person{ID: "s1", Pem: "PRIVATE"}); err != nil {
		t.Fatal(err)
	}

	list, err := vault.Encrypted().Query().All(ctx)
	if err != nil || len(list) != 1 || !strings.HasPrefix(list[0].Pem, "ndb:e1:k1:") {
		t.Fatalf("list %+v, err %v", list, err)
	}

	if shown, err := vault.Get(ctx, "s1"); err != nil || shown.Pem != "PRIVATE" {
		t.Fatalf("show %+v, err %v", shown, err)
	}

	sealed, _ := Collection[Person](db, name, Encrypted("pem"), KeepEncrypted())

	if got, _ := sealed.Get(ctx, "s1"); !strings.HasPrefix(got.Pem, "ndb:e1:k1:") {
		t.Fatalf("a KeepEncrypted collection read %q", got.Pem)
	}
}
