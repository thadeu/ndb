package server

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The error codes are a contract: docs/dev/http/errors.md lists every code
// the daemon answers, and each SDK has a constant for each one. This test
// fails when the server answers a code that the table does not have, or
// when an SDK misses a code of the table.

// clientNever are codes that no client receives: canceled is logged when
// the client already left.
var clientNever = []string{"canceled"}

func TestEveryServerCodeIsInTheErrorTable(t *testing.T) {
	table := tableCodes(t)

	for _, code := range serverCodes(t) {
		if !slices.Contains(table, code) {
			t.Errorf("the server answers %q, which docs/dev/http/errors.md does not list", code)
		}
	}
}

func TestEverySDKHasEveryCode(t *testing.T) {
	sdks := map[string]*regexp.Regexp{
		"sdk/go/errors.go":           regexp.MustCompile(`Code: "([a-z_]+)"`),
		"sdk/ts/src/errors.ts":       regexp.MustCompile(`\| '([a-z_]+)'`),
		"sdk/ruby/lib/ndb/errors.rb": regexp.MustCompile(`"([a-z_]+)" =>`),
	}

	for _, code := range tableCodes(t) {
		if slices.Contains(clientNever, code) {
			continue
		}

		for file, re := range sdks {
			if !slices.Contains(matches(t, file, re), code) {
				t.Errorf("%s has no constant for %q", file, code)
			}
		}
	}
}

// tableCodes are the codes of the first column of the error table.
func tableCodes(t *testing.T) []string {
	t.Helper()

	var codes []string

	for line := range strings.Lines(read(t, "docs/dev/http/errors.md")) {
		if !strings.HasPrefix(line, "| `") {
			continue
		}

		cell := strings.SplitN(line, "|", 3)[1]

		for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(cell, -1) {
			codes = append(codes, m[1])
		}
	}

	if len(codes) < 40 {
		t.Fatalf("read %d codes from the error table: its format changed", len(codes))
	}

	return codes
}

// serverCodes are the codes the daemon makes: the ones that writeError
// maps (errors.go) and the ones built with a code in the engine.
var codeSources = []*regexp.Regexp{
	regexp.MustCompile(`(?:code|Code): +"([a-z_]+)"`),
	regexp.MustCompile(`(?:NewError|errf|newError|coded)\("([a-z_]+)"`),
	regexp.MustCompile(`errors\.New\("([a-z]+_[a-z_]+)"\)`),
}

func serverCodes(t *testing.T) []string {
	t.Helper()

	var codes []string

	for _, m := range regexp.MustCompile(`(?:return|case) "([a-z_]+)"`).FindAllStringSubmatch(read(t, "internal/server/errors.go"), -1) {
		codes = append(codes, m[1])
	}

	files, err := filepath.Glob(filepath.Join(root(t), "internal", "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.Contains(f, "/bench/") {
			continue
		}

		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		for _, re := range codeSources {
			for _, m := range re.FindAllStringSubmatch(string(b), -1) {
				codes = append(codes, m[1])
			}
		}
	}

	slices.Sort(codes)

	return slices.Compact(codes)
}

func matches(t *testing.T, file string, re *regexp.Regexp) []string {
	t.Helper()

	var out []string

	for _, m := range re.FindAllStringSubmatch(read(t, file), -1) {
		out = append(out, m[1])
	}

	return out
}

func read(t *testing.T, rel string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(root(t), rel))
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func root(t *testing.T) string {
	t.Helper()

	dir, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	return dir
}
