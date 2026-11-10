package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The contract cases of spec/streams (see its README): each one is a list of
// steps on a new daemon.

// NdbOnly marks a case of an ndb-only feature (nack, retry_at, due,
// waiting): it runs on ndb, and TestStreamCasesOnRedis skips it.
type streamCase struct {
	Name    string       `json:"name"`
	Stream  string       `json:"stream"`
	NdbOnly bool         `json:"ndb_only"`
	Steps   []streamStep `json:"steps"`
}

type streamStep struct {
	Op       string          `json:"op"`
	Group    string          `json:"group"`
	Consumer string          `json:"consumer"`
	ID       string          `json:"id"`
	Body     json.RawMessage `json:"body"`
	Expect   *struct {
		Body   json.RawMessage   `json:"body"`
		Rows   []json.RawMessage `json:"rows"`
		Error  string            `json:"error"`
		Ignore []string          `json:"ignore"`
	} `json:"expect"`
}

func TestStreamSpecCases(t *testing.T) {
	files, err := filepath.Glob("../../spec/streams/*.json")
	if err != nil || len(files) == 0 {
		t.Fatalf("no stream cases: %v", err)
	}

	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		var c streamCase

		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatalf("%s: %v", f, err)
		}

		t.Run(c.Name, func(t *testing.T) {
			_, h := newHandler(t)

			for i, step := range c.Steps {
				runStreamStep(t, h, c.Stream, i, step)
			}
		})
	}
}

func runStreamStep(t *testing.T, h http.Handler, stream string, i int, step streamStep) {
	t.Helper()

	method, path := streamRoute(stream, step)
	body := string(step.Body)

	if body == "" && method != "GET" && method != "DELETE" {
		body = "{}"
	}

	rec := call(h, method, path, body)
	where := fmt.Sprintf("step %d (%s %s)", i, method, path)

	if step.Expect == nil {
		if rec.Code >= 300 {
			t.Fatalf("%s: status %d: %s", where, rec.Code, rec.Body.String())
		}

		return
	}

	exp := step.Expect

	switch {
	case exp.Error != "":
		if rec.Code < 300 {
			t.Fatalf("%s: want error %s, got %d %s", where, exp.Error, rec.Code, rec.Body.String())
		}

		if got := errorCode(t, rec); got != exp.Error {
			t.Fatalf("%s: error %q, want %q", where, got, exp.Error)
		}
	case rec.Code >= 300:
		t.Fatalf("%s: status %d: %s", where, rec.Code, rec.Body.String())
	case exp.Rows != nil:
		var got []any

		for line := range strings.Lines(rec.Body.String()) {
			got = append(got, decodeAny(t, []byte(line)))
		}

		want := make([]any, len(exp.Rows))

		for i, r := range exp.Rows {
			want[i] = decodeAny(t, r)
		}

		compareJSON(t, where, want, got, exp.Ignore)
	case exp.Body != nil:
		compareJSON(t, where, decodeAny(t, exp.Body), decodeAny(t, rec.Body.Bytes()), exp.Ignore)
	}
}

func streamRoute(s string, step streamStep) (string, string) {
	g := "/" + s + "/_streams/groups/" + step.Group

	switch step.Op {
	case "create":
		return "PUT", "/" + s
	case "append", "trim":
		return "POST", "/" + s + "/_streams/" + step.Op
	case "tail":
		return "POST", "/" + s + "/_streams/read"
	case "info":
		return "GET", "/" + s + "/_streams"
	case "group_create":
		return "PUT", g
	case "group_destroy":
		return "DELETE", g
	case "setid", "read", "ack", "nack", "pending", "claim":
		return "POST", g + "/" + step.Op
	case "consumer_create":
		return "PUT", g + "/consumers/" + step.Consumer
	case "consumer_delete":
		return "DELETE", g + "/consumers/" + step.Consumer
	case "groups":
		return "GET", "/" + s + "/_streams/groups"
	case "consumers":
		return "GET", g + "/consumers"
	case "search", "count":
		return "POST", "/" + s + "/_" + step.Op
	case "put_doc":
		return "POST", "/" + s + "/_doc"
	case "delete":
		return "DELETE", "/" + s + "/_doc/" + step.ID
	}

	panic("unknown op " + step.Op)
}

func decodeAny(t *testing.T, raw []byte) any {
	t.Helper()

	var v any

	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("not JSON: %q", raw)
	}

	return v
}

func compareJSON(t *testing.T, where string, want, got any, ignore []string) {
	t.Helper()

	want, got = strip(want, ignore), strip(got, ignore)

	if !reflect.DeepEqual(want, got) {
		w, _ := json.Marshal(want)
		g, _ := json.Marshal(got)

		t.Fatalf("%s:\n got  %s\n want %s", where, g, w)
	}
}

// strip removes the ignored keys at any depth.
func strip(v any, ignore []string) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}

		for k, val := range x {
			if !contains(ignore, k) {
				out[k] = strip(val, ignore)
			}
		}

		return out
	case []any:
		out := make([]any, len(x))

		for i, val := range x {
			out[i] = strip(val, ignore)
		}

		return out
	}

	return v
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}

	return false
}
