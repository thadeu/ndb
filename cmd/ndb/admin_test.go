package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

// seedMixed writes an engine collection with three sealed segments and a
// direct collection with one bad line, then closes the engine.
func seedMixed(t *testing.T, root string) (dataDir string) {
	t.Helper()

	files := filepath.Join(root, "calls")

	if err := os.MkdirAll(files, 0o755); err != nil {
		t.Fatal(err)
	}

	lines := `{"_id":"c1","status":"answered","n":1}` + "\n" + "{broken line\n" + `{"_id":"c2","status":"busy","n":22,"meta":{"a":1}}` + "\n"

	if err := os.WriteFile(filepath.Join(files, "a.ndjson"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	dataDir = filepath.Join(root, "data")

	calls := catalog.Def{Name: "calls", Write: catalog.Direct, Path: filepath.Join(files, "*.ndjson"), Indexes: []string{"status"}}

	db, err := engine.Open(config.Config{DataDir: dataDir, AllowImplicit: true, Collections: map[string]catalog.Def{"calls": calls}}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	posts, err := db.Ensure(catalog.Def{Name: "posts", Indexes: []string{"author"}})
	if err != nil || posts.Name != "posts" {
		t.Fatal(err)
	}

	col, err := db.Get("posts")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		if _, err := col.Insert(ctx, []byte(fmt.Sprintf(`{"_id":"p%d","author":"a%d","likes":%d}`, i, i, i*10))); err != nil {
			t.Fatal(err)
		}

		if err := col.Seal(); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	return dataDir
}

func runCmd(t *testing.T, fn func([]string, *strings.Builder) error, args ...string) string {
	t.Helper()

	var out strings.Builder

	if err := fn(args, &out); err != nil {
		t.Fatalf("%v: %v", args, err)
	}

	return out.String()
}

func TestCollectionsListsAndShowsOne(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock}

	table := runCmd(t, func(a []string, o *strings.Builder) error { return collections(a, o) }, base...)

	for _, want := range []string{"name", "calls", "direct", "posts", "engine", "author"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}

	one := runCmd(t, func(a []string, o *strings.Builder) error { return collections(a, o) }, append([]string{"calls"}, base...)...)

	if !strings.Contains(one, "field") || !strings.Contains(one, "status (") {
		t.Errorf("single definition:\n%s", one)
	}

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return collections(a, o) }, append([]string{"-json"}, base...)...)

	var list struct {
		Collections []catalog.Def `json:"collections"`
	}

	if err := json.Unmarshal([]byte(raw), &list); err != nil || len(list.Collections) != 2 {
		t.Fatalf("json: %v\n%s", err, raw)
	}

	var out strings.Builder

	if err := collections(append([]string{"nope"}, base...), &out); err == nil || !strings.Contains(err.Error(), "unknown_collection") {
		t.Fatalf("unknown collection error = %v", err)
	}
}

func TestCommandsThatNeedADaemonSayWhenNoneAnswers(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "ndb.yml")
	body := fmt.Sprintf("data_dir: %s\nlisten:\n  unix: %s\n", t.TempDir(), filepath.Join(t.TempDir(), "dead.sock"))

	if err := os.WriteFile(cfgFile, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder

	for name, err := range map[string]error{
		"collections": collections([]string{"-config", cfgFile}, &out),
		"compact":     compact([]string{"-config", cfgFile, "posts"}, &out),
		"quarantine":  quarantine([]string{"-config", cfgFile, "calls"}, &out),
		"query":       query([]string{"-config", cfgFile, "posts"}, &out),
	} {
		if err == nil || !strings.Contains(err.Error(), "nothing answers on") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestCompactCommandReportsBeforeAndAfter(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock}

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return compact(a, o) }, append([]string{"posts", "-json"}, base...)...)

	var rep compactReport

	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}

	if rep.Collection != "posts" || rep.Before.Segments != 3 || rep.After.Segments != 1 {
		t.Fatalf("report = %+v", rep)
	}

	var out strings.Builder

	err := compact(append([]string{"calls"}, base...), &out)
	if err == nil || !strings.Contains(err.Error(), "not_supported") {
		t.Fatalf("direct compact error = %v", err)
	}

	if err := compact(base, &out); err == nil {
		t.Fatal("compact without a collection must fail")
	}
}

func TestQuarantineCommandListsBadLines(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock}

	table := runCmd(t, func(a []string, o *strings.Builder) error { return quarantine(a, o) }, append([]string{"calls"}, base...)...)

	for _, want := range []string{"file", "offset", "error", "{broken line", "a.ndjson", "showing 1 of 1"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}

	empty := runCmd(t, func(a []string, o *strings.Builder) error { return quarantine(a, o) }, append([]string{"posts"}, base...)...)

	if !strings.Contains(empty, "no quarantined lines") {
		t.Errorf("engine collection:\n%s", empty)
	}

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return quarantine(a, o) }, append([]string{"calls", "-limit", "5", "-json"}, base...)...)

	var rep quarantineReport

	if err := json.Unmarshal([]byte(raw), &rep); err != nil || rep.Total != 1 || rep.Rows[0].Line != "{broken line" {
		t.Fatalf("json: %v\n%s", err, raw)
	}
}

func TestQueryPrintsATableOrNDJSON(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock}

	run := func(args ...string) string {
		return runCmd(t, func(a []string, o *strings.Builder) error { return query(a, o) }, append(args, base...)...)
	}

	table := run("calls", `{"status":"busy"}`)

	for _, want := range []string{"_id", "status", "meta", "c2", `{"a":1}`, "1 rows", "plan"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}

	if strings.Contains(table, "c1") {
		t.Errorf("where was ignored:\n%s", table)
	}

	nd := run("posts", `{}`, "-sort", `{"likes":-1}`, "-fields", "_id,likes", "-limit", "2", "-ndjson")
	lines := strings.Split(strings.TrimSpace(nd), "\n")

	if len(lines) != 2 || !strings.Contains(lines[0], `"p2"`) || strings.Contains(lines[0], "author") {
		t.Errorf("ndjson:\n%s", nd)
	}

	agg := run("posts", "-aggregate", `[{"$group":{"_id":null,"total":{"$sum":"$likes"}}}]`, "-json")

	if !strings.Contains(agg, `"total":30`) {
		t.Errorf("aggregate:\n%s", agg)
	}

	flagsFirst := run("-limit", "1", "posts", `{}`, "-json")

	if len(strings.Split(strings.TrimSpace(flagsFirst), "\n")) != 1 {
		t.Errorf("flags before positionals:\n%s", flagsFirst)
	}
}

func TestQueryRejectsBadArguments(t *testing.T) {
	var out strings.Builder

	cases := [][]string{
		{},
		{"posts", "not json"},
		{"posts", "[1]"},
		{"posts", "-sort", "x"},
		{"posts", "-aggregate", "{}"},
		{"posts", `{}`, "-aggregate", "[]"},
	}

	for _, args := range cases {
		if err := query(append(args, "-config", "none.yml"), &out); err == nil || strings.Contains(err.Error(), "nothing answers") {
			t.Errorf("%v: err = %v, want an argument error before any connection", args, err)
		}
	}
}

func TestQueryServerErrorsShowTheCode(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	var out strings.Builder

	err := query([]string{"posts", `{"likes":{"$bogus":1}}`, "-config", "none.yml", "-url", "unix://" + sock}, &out)
	if err == nil || !strings.Contains(err.Error(), "invalid_query") {
		t.Fatalf("err = %v", err)
	}
}

func TestStatsOnlineShowsTotalsAndFootnote(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	table := runCmd(t, func(a []string, o *strings.Builder) error { return stats(a, o) }, "-config", "none.yml", "-url", "unix://"+sock)

	for _, want := range []string{"collection", "quarantined", "total", "calls", "posts", "status (ready)", "blobs", "cursors"} {
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return stats(a, o) }, "-config", "none.yml", "-url", "unix://"+sock, "-json")

	var rep statsReport

	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatal(err)
	}

	if rep.Totals.Collections != 2 || rep.Totals.Quarantined == nil || *rep.Totals.Quarantined != 1 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestSizeOnlineUsesTheDaemon(t *testing.T) {
	dir := seedMixed(t, t.TempDir())
	sock, _ := startDaemon(t, dir)

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return size(a, o) },
		"-config", "none.yml", "-url", "unix://"+sock, "-json")

	if !strings.Contains(raw, `"posts"`) || !strings.Contains(raw, `"segment_files": 3`) {
		t.Fatalf("size json:\n%s", raw)
	}
}

func TestExpirePersistTTLCommands(t *testing.T) {
	dir := t.TempDir()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Ensure(catalog.Def{Name: "sessions", ExpiresField: "exp"}); err != nil {
		t.Fatal(err)
	}

	col, err := db.Get("sessions")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"s1"}`)); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock}

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return ttl(a, o) }, append([]string{"sessions", "s1", "-json"}, base...)...)

	var tr ttlReport

	if err := json.Unmarshal([]byte(raw), &tr); err != nil || tr.TTL != -1 || tr.ExpiresAt != nil {
		t.Fatalf("ttl before = %s, %v", raw, err)
	}

	raw = runCmd(t, func(a []string, o *strings.Builder) error { return expire(a, o) }, append([]string{"sessions", "s1", "30m", "-json"}, base...)...)

	var er expireReport

	if err := json.Unmarshal([]byte(raw), &er); err != nil || er.V != 2 || er.ExpiresAt == "" {
		t.Fatalf("expire = %s, %v", raw, err)
	}

	table := runCmd(t, func(a []string, o *strings.Builder) error { return ttl(a, o) }, append([]string{"sessions", "s1"}, base...)...)

	for _, want := range []string{"ttl", "seconds", "s1", "expires at"} {
		if !strings.Contains(table, want) {
			t.Errorf("ttl table lacks %q:\n%s", want, table)
		}
	}

	table = runCmd(t, func(a []string, o *strings.Builder) error { return persist(a, o) }, append([]string{"sessions", "s1"}, base...)...)

	if !strings.Contains(table, "version") || !strings.Contains(table, "3") {
		t.Errorf("persist table:\n%s", table)
	}

	raw = runCmd(t, func(a []string, o *strings.Builder) error { return expire(a, o) }, append([]string{"sessions", "s1", "3600", "-json"}, base...)...)

	if err := json.Unmarshal([]byte(raw), &er); err != nil || er.V != 4 {
		t.Fatalf("expire seconds = %s, %v", raw, err)
	}

	var out strings.Builder

	if err := expire(append([]string{"sessions", "nope", "1h"}, base...), &out); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("expire missing = %v", err)
	}

	if err := expire(append([]string{"sessions", "s1"}, base...), &out); err == nil {
		t.Fatal("expire without a duration must fail")
	}
}

func TestUpdateDeleteCommands(t *testing.T) {
	dir := t.TempDir()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Ensure(catalog.Def{Name: "stock", Indexes: []string{"g"}, ExpiresField: "exp"}); err != nil {
		t.Fatal(err)
	}

	col, err := db.Get("stock")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"a","g":1,"qty":2}`), []byte(`{"_id":"b","g":1,"qty":2}`)); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock, "-json"}

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return update(a, o) },
		append([]string{"stock", `{"_id":"a","$inc":{"qty":-1}}`, "--expire-in", "60s"}, base...)...)

	var up updateReport

	if err := json.Unmarshal([]byte(raw), &up); err != nil || up.Matched != 1 || up.Modified != 1 || !strings.Contains(string(up.Doc), `"qty": 1`) && !strings.Contains(string(up.Doc), `"qty":1`) {
		t.Fatalf("update = %s, %v", raw, err)
	}

	raw = runCmd(t, func(a []string, o *strings.Builder) error { return update(a, o) },
		append([]string{"stock", `{"g":1,"$set":{"x":1}}`, "--many"}, base...)...)

	var many updateReport

	if err := json.Unmarshal([]byte(raw), &many); err != nil || many.Matched != 2 || many.Doc != nil {
		t.Fatalf("update many = %s, %v", raw, err)
	}

	raw = runCmd(t, func(a []string, o *strings.Builder) error { return remove(a, o) },
		append([]string{"stock", `{"g":1}`, "--many"}, base...)...)

	var del deleteReport

	if err := json.Unmarshal([]byte(raw), &del); err != nil || del.Deleted != 2 {
		t.Fatalf("delete = %s, %v", raw, err)
	}

	var out strings.Builder

	if err := update(append([]string{"stock", `{"_id":"a"}`}, base...), &out); err == nil || !strings.Contains(err.Error(), "no_update") {
		t.Fatalf("update without operators = %v", err)
	}
}

func TestGetUpdateDeleteByIDCommands(t *testing.T) {
	dir := t.TempDir()

	db, err := engine.Open(config.Config{DataDir: dir, AllowImplicit: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	col, err := db.GetOrCreate("stock")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := col.Insert(ctx, []byte(`{"_id":"a","qty":2}`)); err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	sock, _ := startDaemon(t, dir)

	base := []string{"-config", "none.yml", "-url", "unix://" + sock, "-json"}

	raw := runCmd(t, func(a []string, o *strings.Builder) error { return get(a, o) }, append([]string{"stock", "a"}, base...)...)

	if !strings.Contains(raw, `"_id":"a"`) || !strings.Contains(raw, `"_v":1`) {
		t.Fatalf("get = %s", raw)
	}

	raw = runCmd(t, func(a []string, o *strings.Builder) error { return update(a, o) },
		append([]string{"stock", "a", `{"$inc":{"qty":3}}`}, base...)...)

	var up updateReport

	if err := json.Unmarshal([]byte(raw), &up); err != nil || up.Modified != 1 || !strings.Contains(string(up.Doc), `"qty": 5`) {
		t.Fatalf("update by id = %s, %v", raw, err)
	}

	raw = runCmd(t, func(a []string, o *strings.Builder) error { return remove(a, o) }, append([]string{"stock", "a"}, base...)...)

	var del deleteReport

	if err := json.Unmarshal([]byte(raw), &del); err != nil || del.Deleted != 1 {
		t.Fatalf("delete by id = %s, %v", raw, err)
	}

	var out strings.Builder

	if err := get(append([]string{"stock", "a"}, base...), &out); err == nil || !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("get after delete = %v", err)
	}
}

func TestHealthCommand(t *testing.T) {
	sock, _ := startDaemon(t, t.TempDir())

	out := runCmd(t, func(a []string, o *strings.Builder) error { return health(a, o) }, "-config", "none.yml", "-url", "unix://"+sock)

	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("health = %q", out)
	}

	var sb strings.Builder

	err := health([]string{"-config", "none.yml", "-url", "unix://" + filepath.Join(t.TempDir(), "none.sock")}, &sb)
	if err == nil {
		t.Fatal("health on a missing socket must fail")
	}
}
