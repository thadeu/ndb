package spec_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/spec"
)

// listEngine stores inserts and returns all of them on find. It only proves
// that the runner works until the real engine exists (M01/M03).
type listEngine struct {
	docs []json.RawMessage
}

func (e *listEngine) EnsureCollection(context.Context, string, json.RawMessage) error {
	return nil
}

func (e *listEngine) Do(_ context.Context, _ string, step spec.Step) ([]json.RawMessage, error) {
	switch step.Op {
	case "bulk":
		var body struct {
			Insert []json.RawMessage `json:"docs"`
		}

		if err := json.Unmarshal(step.Body, &body); err != nil {
			return nil, err
		}

		e.docs = append(e.docs, body.Insert...)

		return nil, nil
	case "search":
		return e.docs, nil
	}

	return nil, &spec.CodeError{Code: "unknown_op"}
}

func TestSampleCasesRun(t *testing.T) {
	all, err := spec.Load("../../spec/cases/find")
	if err != nil {
		t.Fatal(err)
	}

	var cases []spec.Case

	for _, c := range all {
		if strings.HasSuffix(c.File, "find_all.json") {
			cases = append(cases, c)
		}
	}

	if len(cases) == 0 {
		t.Fatal("no cases loaded")
	}

	spec.Run(t, cases, func(*testing.T) spec.Engine { return &listEngine{} })
}
