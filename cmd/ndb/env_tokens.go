package main

import (
	"encoding/json"
	"fmt"

	"github.com/thadeu/ndb/internal/server"
)

// envToken is one scoped token of NDB_TOKENS: the same rights as tokens:
// in ndb.yml, with the secret in the value instead of a file.
type envToken struct {
	Name    string   `json:"name"`
	Secret  string   `json:"secret"`
	Read    []string `json:"read"`
	Write   []string `json:"write"`
	Admin   []string `json:"admin"`
	Decrypt []string `json:"decrypt"`
}

// envTokens reads NDB_TOKENS, a JSON list of scoped tokens, for an image
// whose only setting is the env (Ndb Cloud). Each token needs a name, a
// secret and at least one right; names do not repeat.
func envTokens(lookup func(string) (string, bool)) ([]server.Token, error) {
	value, ok := lookup("NDB_TOKENS")
	if !ok || value == "" {
		return nil, nil
	}

	var list []envToken

	if err := json.Unmarshal([]byte(value), &list); err != nil {
		return nil, fmt.Errorf("NDB_TOKENS: %w", err)
	}

	out := make([]server.Token, 0, len(list))
	seen := map[string]bool{}

	for i, t := range list {
		switch {
		case t.Name == "" || t.Secret == "":
			return nil, fmt.Errorf("NDB_TOKENS: token %d needs a name and a secret", i+1)
		case len(t.Read)+len(t.Write)+len(t.Admin)+len(t.Decrypt) == 0:
			return nil, fmt.Errorf("NDB_TOKENS: token %s has no right", t.Name)
		case seen[t.Name]:
			return nil, fmt.Errorf("NDB_TOKENS: token %s twice", t.Name)
		}

		seen[t.Name] = true

		out = append(out, server.Token{Name: t.Name, Secret: t.Secret, Read: t.Read, Write: t.Write, Admin: t.Admin, Decrypt: t.Decrypt})
	}

	return out, nil
}
