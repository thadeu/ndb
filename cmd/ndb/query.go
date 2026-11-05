package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/thadeu/ndb/internal/tui"
)

// previewRows is how many rows decide the columns of the table.
const previewRows = 200

// runQuery is "ndb query <collection> '<where>'": find or aggregate on a
// running daemon. Rows come as a table, or as NDJSON with -json.
func runQuery(args []string) error {
	return query(args, os.Stdout)
}

func query(args []string, out io.Writer) error {
	fl := flag.NewFlagSet("ndb query", flag.ContinueOnError)

	cf := addConnFlags(fl)
	ndjson := fl.Bool("ndjson", false, "print rows as NDJSON (same as -json)")
	sortSpec := fl.String("sort", "", `sort, JSON: '{"ts":-1}'`)
	fields := fl.String("fields", "", "comma-separated fields to return")
	limit := fl.Int("limit", 0, "rows to return (0 = the server default)")
	pipeline := fl.String("aggregate", "", `run an aggregation pipeline, JSON: '[{"$group":...}]'`)

	pos, err := parseInterspersed(fl, args)
	if err != nil {
		return err
	}

	usage := errors.New(`usage: ndb query <collection> ['<where-json>'] [-sort JSON] [-fields a,b] [-limit N] [-json]
       ndb query <collection> -aggregate '<pipeline-json>' [-json]`)

	if len(pos) < 1 || len(pos) > 2 {
		return usage
	}

	path, body, err := queryRequest(pos, *sortSpec, *fields, *limit, *pipeline)
	if err != nil {
		return err
	}

	t, err := cf.resolve(fl)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	c, err := t.need()
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}

	r, err := c.call("POST", path, body)
	if err != nil {
		return err
	}

	if *cf.asJSON || *ndjson {
		_, err = out.Write(r.Body)

		return err
	}

	printRows(out, r)

	return nil
}

// queryRequest builds the path and body of find or aggregate.
func queryRequest(pos []string, sortSpec, fields string, limit int, pipeline string) (string, []byte, error) {
	base := "/v1/c/" + url.PathEscape(pos[0])

	if pipeline != "" {
		if len(pos) == 2 || sortSpec != "" || fields != "" || limit != 0 {
			return "", nil, errors.New("-aggregate takes the pipeline only: put where, sort and limit inside it")
		}

		raw, err := validJSON("-aggregate", pipeline, '[')
		if err != nil {
			return "", nil, err
		}

		body, err := json.Marshal(map[string]json.RawMessage{"pipeline": raw})

		return base + "/aggregate", body, err
	}

	req := map[string]any{}

	if len(pos) == 2 {
		raw, err := validJSON("where", pos[1], '{')
		if err != nil {
			return "", nil, err
		}

		req["where"] = raw
	}

	if sortSpec != "" {
		raw, err := validJSON("-sort", sortSpec, '{')
		if err != nil {
			return "", nil, err
		}

		req["sort"] = raw
	}

	if fields != "" {
		req["fields"] = strings.Split(fields, ",")
	}

	if limit != 0 {
		req["limit"] = limit
	}

	body, err := json.Marshal(req)

	return base + "/find", body, err
}

// validJSON checks that s is JSON of the kind open ('{' or '[').
func validJSON(what, s string, open byte) (json.RawMessage, error) {
	s = strings.TrimSpace(s)

	if !json.Valid([]byte(s)) || s == "" || s[0] != open {
		return nil, fmt.Errorf("%s: want a JSON %s, got %q", what, map[byte]string{'{': "object", '[': "array"}[open], s)
	}

	return json.RawMessage(s), nil
}

// pair is one top-level key of a row, in the order of the row.
type pair struct {
	key string
	val json.RawMessage
}

// orderedPairs reads a JSON object and keeps the key order.
func orderedPairs(raw []byte) ([]pair, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))

	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, errors.New("row is not a JSON object")
	}

	var out []pair

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}

		key, _ := tok.(string)

		var val json.RawMessage

		if err := dec.Decode(&val); err != nil {
			return nil, err
		}

		out = append(out, pair{key, val})
	}

	return out, nil
}

func printRows(out io.Writer, r reply) {
	lines := bytes.Split(bytes.TrimSpace(r.Body), []byte("\n"))

	if len(bytes.TrimSpace(r.Body)) == 0 {
		lines = nil
	}

	var (
		cols    []string
		index   = map[string]int{}
		numeric = map[string]bool{}
		rows    []map[string]string
	)

	for i, line := range lines {
		pairs, err := orderedPairs(line)
		if err != nil {
			continue
		}

		row := map[string]string{}

		for _, p := range pairs {
			if _, seen := index[p.key]; !seen && i < previewRows {
				index[p.key] = len(cols)
				cols = append(cols, p.key)
				numeric[p.key] = true
			}

			row[p.key] = cell(p.val)

			if len(p.val) > 0 && !isNumber(p.val) {
				numeric[p.key] = false
			}
		}

		rows = append(rows, row)
	}

	tb := tui.Table{}

	for _, name := range cols {
		align := tui.Left

		if numeric[name] {
			align = tui.Right
		}

		tb.Columns = append(tb.Columns, tui.Column{Title: name, Align: align})
	}

	for _, row := range rows {
		cells := make([]string, len(cols))

		for i, name := range cols {
			cells[i] = row[name]
		}

		tb.Rows = append(tb.Rows, cells)
	}

	if len(cols) == 0 {
		fmt.Fprintln(out, "0 rows")
	} else {
		tb.Render(out, styleFor(out))
	}

	fmt.Fprintln(out, queryFootnote(len(rows), r))
}

// cell prints a JSON value: strings bare, scalars as written, nested values
// as compact JSON.
func cell(v json.RawMessage) string {
	if len(v) > 0 && v[0] == '"' {
		var s string

		if json.Unmarshal(v, &s) == nil {
			return s
		}
	}

	var buf bytes.Buffer

	if json.Compact(&buf, v) != nil {
		return string(v)
	}

	return buf.String()
}

func isNumber(v json.RawMessage) bool {
	return len(v) > 0 && (v[0] == '-' || v[0] >= '0' && v[0] <= '9')
}

func queryFootnote(n int, r reply) string {
	parts := []string{fmt.Sprintf("%s rows", tui.Thousands(int64(n)))}

	if plan := r.Header.Get("X-Ndb-Plan"); plan != "" {
		parts = append(parts, "plan "+plan)
	}

	if ex := r.Header.Get("X-Ndb-Examined"); ex != "" {
		parts = append(parts, "examined "+ex)
	}

	if r.Header.Get("X-Ndb-Next") != "" {
		parts = append(parts, "more rows exist: raise -limit")
	}

	return strings.Join(parts, " · ")
}
