package ndb

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// urlCase is one case of spec/url/cases.json: the token a client takes from
// NDB_URL, the token option and NDB_TOKEN.
type urlCase struct {
	Name   string  `json:"name"`
	URL    string  `json:"url"`
	Env    *string `json:"env"`
	Option *string `json:"option"`
	Secret string  `json:"secret"`
	Want   struct {
		Token    *string `json:"token"`
		Database *string `json:"database"`
		Base     string  `json:"base"`
		Error    bool    `json:"error"`
	} `json:"want"`
}

func TestURLCases(t *testing.T) {
	b, err := os.ReadFile("../../spec/url/cases.json")
	if err != nil {
		t.Fatal(err)
	}

	var file struct {
		Cases []urlCase `json:"cases"`
	}

	if err := json.Unmarshal(b, &file); err != nil {
		t.Fatal(err)
	}

	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			env := ""

			if c.Env != nil {
				env = *c.Env
			}

			t.Setenv("NDB_TOKEN", env)

			var opts []ConnectOption

			if c.Option != nil {
				opts = append(opts, WithToken(*c.Option))
			}

			db, err := Connect(c.URL, opts...)

			if c.Want.Error {
				if err == nil {
					t.Fatalf("Connect(%q): no error", c.URL)
				}

				if c.Secret != "" && strings.Contains(err.Error(), c.Secret) {
					t.Fatalf("the error shows the token: %v", err)
				}

				return
			}

			if err != nil {
				t.Fatal(err)
			}

			want := ""

			if c.Want.Token != nil {
				want = *c.Want.Token
			}

			if db.token != want {
				t.Fatalf("token %q, want %q", db.token, want)
			}

			wantDB := ""

			if c.Want.Database != nil {
				wantDB = *c.Want.Database
			}

			if db.database != wantDB {
				t.Fatalf("database %q, want %q", db.database, wantDB)
			}

			if c.Want.Base != "" && db.base != c.Want.Base {
				t.Fatalf("base %q, want %q", db.base, c.Want.Base)
			}

			shown := fmt.Sprintf("%v %+v %#v", db, db, db)

			if c.Secret != "" && strings.Contains(shown, c.Secret) {
				t.Fatalf("the printed handle shows the token: %s", shown)
			}
		})
	}
}
