package server_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestStreamCasesOnRedis runs the cases of spec/streams against a real
// Redis, each step turned into its Redis command: when Redis gives what a
// case expects, the contract of ndb streams is the behavior of Redis. It
// runs only with NDB_REDIS_ADDR (a Redis 7.4 or newer that it may flush):
//
//	docker run --rm -p 6390:6379 redis:7.4
//	NDB_REDIS_ADDR=127.0.0.1:6390 go test -run TestStreamCasesOnRedis ./internal/server/
//
// Values are compared as text (Redis keeps strings) and ndb-only fields
// (_ts) are left out; the ndb-only fields of a pending answer get their
// value with no nack (waiting 0, retry_at null). A case with a normal
// collection (not_a_stream) has no Redis meaning and is skipped, and so is
// a case marked ndb_only (a delayed retry: Redis has none).
func TestStreamCasesOnRedis(t *testing.T) {
	addr := os.Getenv("NDB_REDIS_ADDR")

	if addr == "" {
		t.Skip("set NDB_REDIS_ADDR to a Redis to compare with")
	}

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
			t.Fatal(err)
		}

		t.Run(c.Name, func(t *testing.T) {
			if c.NdbOnly {
				t.Skip("an ndb-only case")
			}

			for _, s := range c.Steps {
				if s.Op == "put_doc" || s.Op == "create" {
					t.Skip("a normal collection has no Redis meaning")
				}
			}

			r := dialRedis(t, addr)

			defer r.conn.Close()

			r.do("FLUSHALL")

			for i, step := range c.Steps {
				runRedisStep(t, r, c.Stream, i, step)
			}
		})
	}
}

func runRedisStep(t *testing.T, r *redis, s string, i int, step streamStep) {
	t.Helper()

	got, err := redisStep(r, s, step)
	where := fmt.Sprintf("step %d (%s)", i, step.Op)

	if step.Expect == nil {
		if err != nil {
			t.Fatalf("%s: %v", where, err)
		}

		return
	}

	exp := step.Expect
	ignore := append([]string{"_ts"}, exp.Ignore...)

	switch {
	case exp.Error != "":
		if code := redisCode(err); code != exp.Error {
			t.Fatalf("%s: Redis gave %v (%q), want %s", where, err, code, exp.Error)
		}
	case err != nil:
		t.Fatalf("%s: %v", where, err)
	case exp.Rows != nil:
		want := make([]any, len(exp.Rows))

		for i, row := range exp.Rows {
			want[i] = decodeAny(t, row)
		}

		compareJSON(t, where, asText(want), asText(got), ignore)
	default:
		compareJSON(t, where, asText(decodeAny(t, exp.Body)), asText(got), ignore)
	}
}

// redisCode maps a Redis error to the ndb code of the same case.
func redisCode(err error) string {
	if err == nil {
		return ""
	}

	msg := err.Error()

	switch {
	case strings.Contains(msg, "equal or smaller"), strings.Contains(msg, "exhausted the last possible ID"):
		return "stream_id_too_small"
	case strings.Contains(msg, "LIMIT cannot be used without"), strings.Contains(msg, "LIMIT argument must be"):
		return "invalid_request"
	case strings.Contains(msg, "greater than 0-0"), strings.Contains(msg, "Invalid stream ID"):
		return "invalid_stream_id"
	case strings.HasPrefix(msg, "BUSYGROUP"):
		return "group_exists"
	case strings.HasPrefix(msg, "NOGROUP"):
		return "unknown_group"
	}

	return msg
}

// redisStep runs one step as Redis commands and gives the answer in the
// shape of the ndb answer.
func redisStep(r *redis, s string, step streamStep) (any, error) {
	var b map[string]any

	if len(step.Body) > 0 {
		_ = json.Unmarshal(step.Body, &b)
	}

	g, c := step.Group, step.Consumer

	switch step.Op {
	case "append":
		args := []string{"XADD", s, text(b["id"], "*")}

		for k, v := range b["doc"].(map[string]any) {
			args = append(args, k, text(v, ""))
		}

		id, err := r.do(args...)

		return map[string]any{"_id": id}, err
	case "trim":
		args := []string{"XTRIM", s, "MINID"}
		at := text(b["min_id"], "")

		if v, ok := b["max_len"]; ok {
			args[2], at = "MAXLEN", text(v, "")
		}

		if b["approx"] == true {
			args = append(args, "~")
		}

		args = append(args, at)

		if v, ok := b["limit"]; ok {
			args = append(args, "LIMIT", text(v, ""))
		}

		n, err := r.do(args...)

		return map[string]any{"deleted": n}, err
	case "delete":
		n, err := r.do("XDEL", s, step.ID)

		return map[string]any{"deleted": n}, err
	case "info":
		return r.streamInfo(s)
	case "count":
		n, err := r.do("XLEN", s)

		return map[string]any{"count": n}, err
	case "search":
		return r.search(s, b)
	case "group_create":
		if _, err := r.do("XGROUP", "CREATE", s, g, text(b["start"], "$"), "MKSTREAM"); err != nil {
			return nil, err
		}

		return r.groupHead(s, g)
	case "setid":
		if _, err := r.do("XGROUP", "SETID", s, g, text(b["id"], "$")); err != nil {
			return nil, err
		}

		return r.groupHead(s, g)
	case "group_destroy":
		_, err := r.do("XGROUP", "DESTROY", s, g)

		return map[string]any{"deleted": true}, err
	case "consumer_create":
		n, err := r.do("XGROUP", "CREATECONSUMER", s, g, c)

		return map[string]any{"created": n == int64(1)}, err
	case "consumer_delete":
		n, err := r.do("XGROUP", "DELCONSUMER", s, g, c)

		return map[string]any{"pending": n}, err
	case "groups":
		return r.groups(s)
	case "consumers":
		return r.consumers(s, g)
	case "read":
		return r.read(s, g, b)
	case "tail":
		return r.tail(s, b)
	case "ack":
		args := []string{"XACK", s, g}

		for _, id := range b["ids"].([]any) {
			args = append(args, id.(string))
		}

		n, err := r.do(args...)

		return map[string]any{"acked": n}, err
	case "pending":
		return r.pending(s, g, b)
	case "claim":
		return r.claim(s, g, b)
	}

	return nil, fmt.Errorf("no Redis command for %s", step.Op)
}

func text(v any, def string) string {
	switch x := v.(type) {
	case nil:
		return def
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}

	return fmt.Sprint(v)
}

// asText turns every number and bool into its text, as Redis keeps them.
func asText(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}

		for k, val := range x {
			out[k] = asText(val)
		}

		return out
	case []any:
		out := make([]any, len(x))

		for i, val := range x {
			out[i] = asText(val)
		}

		return out
	case float64, int64, bool:
		return text(x, "")
	}

	return v
}

// entry turns [id, [f, v, ...]] into {"_id": id, f: v}; a nil field list
// (a deleted entry) gives {"_id": id, "_deleted": true}.
func entry(v any) map[string]any {
	pair := v.([]any)
	out := map[string]any{"_id": pair[0]}

	if pair[1] == nil {
		out["_deleted"] = true

		return out
	}

	fields := pair[1].([]any)

	for i := 0; i+1 < len(fields); i += 2 {
		out[fields[i].(string)] = fields[i+1]
	}

	return out
}

func entries(v any) []any {
	out := []any{}

	list, _ := v.([]any)

	for _, e := range list {
		out = append(out, entry(e))
	}

	return out
}

// pairs reads a Redis reply of key, value, key, value.
func pairs(v any) map[string]any {
	out := map[string]any{}

	list, _ := v.([]any)

	for i := 0; i+1 < len(list); i += 2 {
		out[list[i].(string)] = list[i+1]
	}

	return out
}

func (r *redis) streamInfo(s string) (any, error) {
	v, err := r.do("XINFO", "STREAM", s)
	if err != nil {
		return nil, err
	}

	m := pairs(v)
	first := "0-0"

	if e, ok := m["first-entry"].([]any); ok {
		first = e[0].(string)
	}

	return map[string]any{
		"length": m["length"], "first_id": first, "last_id": m["last-generated-id"],
		"entries_added": m["entries-added"], "max_deleted_id": m["max-deleted-entry-id"], "groups": m["groups"],
	}, nil
}

func (r *redis) search(s string, b map[string]any) (any, error) {
	start, end := "-", "+"

	if q, ok := b["query"].(map[string]any); ok {
		if rng, ok := q["_id"].(map[string]any); ok {
			start, end = text(rng["$gte"], "-"), text(rng["$lte"], "+")
		}
	}

	cmd := []string{"XRANGE", s, start, end}

	if sort, ok := b["sort"].(map[string]any); ok && sort["_id"] == float64(-1) {
		cmd = []string{"XREVRANGE", s, end, start}
	}

	if n, ok := b["limit"]; ok {
		cmd = append(cmd, "COUNT", text(n, ""))
	}

	v, err := r.do(cmd...)

	return entries(v), err
}

func (r *redis) groupHead(s, g string) (any, error) {
	groups, err := r.groups(s)
	if err != nil {
		return nil, err
	}

	for _, gi := range groups.([]any) {
		m := gi.(map[string]any)

		if m["name"] == g {
			return map[string]any{"group": g, "last_delivered_id": m["last_delivered_id"]}, nil
		}
	}

	return nil, errors.New("NOGROUP")
}

func (r *redis) groups(s string) (any, error) {
	v, err := r.do("XINFO", "GROUPS", s)
	if err != nil {
		return nil, err
	}

	out := []any{}

	for _, g := range v.([]any) {
		m := pairs(g)

		out = append(out, map[string]any{
			"name": m["name"], "consumers": m["consumers"], "pending": m["pending"],
			"last_delivered_id": m["last-delivered-id"], "entries_read": m["entries-read"], "lag": m["lag"],
		})
	}

	return out, nil
}

func (r *redis) consumers(s, g string) (any, error) {
	v, err := r.do("XINFO", "CONSUMERS", s, g)
	if err != nil {
		return nil, err
	}

	out := []any{}

	for _, c := range v.([]any) {
		m := pairs(c)

		out = append(out, map[string]any{"name": m["name"], "pending": m["pending"]})
	}

	return out, nil
}

func (r *redis) read(s, g string, b map[string]any) (any, error) {
	cmd := []string{"XREADGROUP", "GROUP", g, text(b["consumer"], ""), "COUNT", text(b["count"], "10")}

	if b["no_ack"] == true {
		cmd = append(cmd, "NOACK")
	}

	from := ">"

	if b["pending"] == true {
		from = text(b["after"], "0")
	}

	v, err := r.do(append(cmd, "STREAMS", s, from)...)
	if err != nil || v == nil {
		return []any{}, err
	}

	stream := v.([]any)[0].([]any)

	return entries(stream[1]), nil
}

// tail is XREAD: a read without a group.
func (r *redis) tail(s string, b map[string]any) (any, error) {
	v, err := r.do("XREAD", "COUNT", text(b["count"], "10"), "STREAMS", s, text(b["after"], "0"))
	if err != nil || v == nil {
		return []any{}, err
	}

	stream := v.([]any)[0].([]any)

	return entries(stream[1]), nil
}

func (r *redis) pending(s, g string, b map[string]any) (any, error) {
	if b["count"] == nil {
		v, err := r.do("XPENDING", s, g)
		if err != nil {
			return nil, err
		}

		list := v.([]any)
		cons := []any{}

		if rows, ok := list[3].([]any); ok {
			for _, c := range rows {
				pair := c.([]any)
				n, _ := strconv.ParseInt(pair[1].(string), 10, 64)

				cons = append(cons, map[string]any{"name": pair[0], "count": n})
			}
		}

		return map[string]any{"count": list[0], "lowest": list[1], "highest": list[2], "consumers": cons, "waiting": int64(0)}, nil
	}

	cmd := []string{"XPENDING", s, g}

	if idle, ok := b["idle_ms"]; ok {
		cmd = append(cmd, "IDLE", text(idle, ""))
	}

	cmd = append(cmd, text(b["start"], "-"), text(b["end"], "+"), text(b["count"], ""))

	if c, ok := b["consumer"]; ok {
		cmd = append(cmd, text(c, ""))
	}

	v, err := r.do(cmd...)

	if err != nil {
		return nil, err
	}

	out := []any{}

	for _, p := range v.([]any) {
		row := p.([]any)

		out = append(out, map[string]any{"_id": row[0], "consumer": row[1], "idle_ms": row[2], "deliveries": row[3], "retry_at": nil})
	}

	return out, nil
}

func (r *redis) claim(s, g string, b map[string]any) (any, error) {
	consumer, idle := text(b["consumer"], ""), text(b["min_idle_ms"], "0")
	justIDs := b["just_ids"] == true

	if cursor, ok := b["cursor"]; ok {
		cmd := []string{"XAUTOCLAIM", s, g, consumer, idle, text(cursor, "0-0"), "COUNT", text(b["count"], "100")}

		if justIDs {
			cmd = append(cmd, "JUSTID")
		}

		v, err := r.do(cmd...)
		if err != nil {
			return nil, err
		}

		list := v.([]any)
		deleted := []any{}

		if len(list) > 2 && list[2] != nil {
			deleted = list[2].([]any)
		}

		return map[string]any{"cursor": list[0], "entries": claimed(list[1], justIDs), "deleted": deleted}, nil
	}

	// XCLAIM gives no list of deleted entries: they are the pending IDs
	// whose entry is gone, found before the claim.
	cmd := []string{"XCLAIM", s, g, consumer, idle}
	deleted := []any{}

	for _, id := range b["ids"].([]any) {
		cmd = append(cmd, id.(string))

		if v, _ := r.do("XRANGE", s, id.(string), id.(string)); len(v.([]any)) == 0 {
			if p, _ := r.do("XPENDING", s, g, id.(string), id.(string), "1"); len(p.([]any)) > 0 {
				deleted = append(deleted, id)
			}
		}
	}

	if idle, ok := b["idle_ms"]; ok {
		cmd = append(cmd, "IDLE", text(idle, ""))
	}

	if n, ok := b["retry_count"]; ok {
		cmd = append(cmd, "RETRYCOUNT", text(n, ""))
	}

	if b["force"] == true {
		cmd = append(cmd, "FORCE")
	}

	if justIDs {
		cmd = append(cmd, "JUSTID")
	}

	v, err := r.do(cmd...)

	return map[string]any{"entries": claimed(v, justIDs), "deleted": deleted}, err
}

func claimed(v any, justIDs bool) []any {
	if justIDs {
		out := []any{}

		if list, ok := v.([]any); ok {
			out = append(out, list...)
		}

		return out
	}

	out := []any{}

	list, _ := v.([]any)

	for _, e := range list {
		if e != nil {
			out = append(out, entry(e))
		}
	}

	return out
}

// ---- a small RESP2 client

type redis struct {
	conn net.Conn
	rd   *bufio.Reader
}

func dialRedis(t *testing.T, addr string) *redis {
	t.Helper()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Redis at %s: %v", addr, err)
	}

	return &redis{conn: conn, rd: bufio.NewReader(conn)}
}

func (r *redis) do(args ...string) (any, error) {
	var sb strings.Builder

	fmt.Fprintf(&sb, "*%d\r\n", len(args))

	for _, a := range args {
		fmt.Fprintf(&sb, "$%d\r\n%s\r\n", len(a), a)
	}

	if _, err := r.conn.Write([]byte(sb.String())); err != nil {
		return nil, err
	}

	return r.reply()
}

func (r *redis) reply() (any, error) {
	line, err := r.rd.ReadString('\n')
	if err != nil {
		return nil, err
	}

	line = strings.TrimSuffix(line, "\r\n")
	body := line[1:]

	switch line[0] {
	case '+':
		return body, nil
	case '-':
		return nil, errors.New(body)
	case ':':
		n, err := strconv.ParseInt(body, 10, 64)

		return n, err
	case '$':
		n, _ := strconv.Atoi(body)

		if n < 0 {
			return nil, nil
		}

		buf := make([]byte, n+2)

		if _, err := io_ReadFull(r.rd, buf); err != nil {
			return nil, err
		}

		return string(buf[:n]), nil
	case '*':
		n, _ := strconv.Atoi(body)

		if n < 0 {
			return nil, nil
		}

		out := make([]any, n)

		for i := range out {
			v, err := r.reply()
			if err != nil {
				var redisErr error = err

				out[i] = redisErr.Error()

				continue
			}

			out[i] = v
		}

		return out, nil
	}

	return nil, fmt.Errorf("bad RESP line %q", line)
}

func io_ReadFull(rd *bufio.Reader, buf []byte) (int, error) {
	n := 0

	for n < len(buf) {
		m, err := rd.Read(buf[n:])

		n += m

		if err != nil {
			return n, err
		}
	}

	return n, nil
}
