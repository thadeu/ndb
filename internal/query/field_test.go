package query_test

import (
	"errors"
	"testing"

	"github.com/thadeu/ndb/internal/query"
)

func TestFieldHookRewritesAConditionEverywhere(t *testing.T) {
	opts := query.Options{Field: func(path string, v any) (string, any, error) {
		if path == "secret" {
			return "", nil, errors.New("no")
		}

		if path != "cpf" {
			return path, v, nil
		}

		return "cpf_h", "H(" + v.(string) + ")", nil
	}}

	q, err := query.Parse([]byte(`{"query":{"$or":[{"cpf":"1"},{"name":"x"}]}}`), opts)
	if err != nil {
		t.Fatal(err)
	}

	if !q.Match(map[string]any{"cpf_h": "H(1)"}) || q.Match(map[string]any{"cpf": "1"}) {
		t.Fatal("the condition was not rewritten")
	}

	if _, err := query.Parse([]byte(`{"query":{"$not":{"secret":1}}}`), opts); err == nil {
		t.Fatal("the hook error was lost")
	}
}
