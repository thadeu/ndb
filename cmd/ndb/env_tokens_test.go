package main

import (
	"strings"
	"testing"
)

// NDB_TOKENS gives scoped tokens in the env, as a JSON list, for an image
// with no config file. Each one needs a name, a secret and a right.
func TestEnvTokens(t *testing.T) {
	lookup := func(value string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			if key == "NDB_TOKENS" && value != "" {
				return value, true
			}

			return "", false
		}
	}

	none, err := envTokens(lookup(""))
	if err != nil || len(none) != 0 {
		t.Fatalf("no NDB_TOKENS: %v %v", none, err)
	}

	got, err := envTokens(lookup(`[{"name":"esl","secret":"s1","write":["outbox","calls*"]},{"name":"kyc","secret":"s2","read":["users"],"decrypt":["users"]}]`))
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 || got[0].Name != "esl" || got[0].Secret != "s1" || strings.Join(got[0].Write, ",") != "outbox,calls*" || got[1].Decrypt[0] != "users" || got[1].Full {
		t.Fatalf("tokens = %+v", got)
	}

	for _, bad := range []string{
		`not json`,
		`[{"secret":"s","read":["a"]}]`,
		`[{"name":"a","read":["a"]}]`,
		`[{"name":"a","secret":"s"}]`,
		`[{"name":"a","secret":"s","read":["a"]},{"name":"a","secret":"t","read":["b"]}]`,
	} {
		if _, err := envTokens(lookup(bad)); err == nil {
			t.Fatalf("%s: want an error", bad)
		}
	}
}
