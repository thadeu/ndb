package backup_test

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/backup"
	"github.com/thadeu/ndb/internal/catalog"
)

// fixture writes a one-collection backup with no WAL line.
func fixture(t *testing.T, opts backup.Options) []byte {
	t.Helper()

	var buf bytes.Buffer

	src := backup.Source{
		Defs: []catalog.Def{{Name: "log", Write: catalog.Engine}},
		WAL:  func(string, func([]byte) error) error { return nil },
	}

	if err := backup.Write(&buf, src, opts); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestWriteThenExtract(t *testing.T) {
	buf := bytes.NewBuffer(fixture(t, backup.Options{Version: "test"}))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")

	if !strings.HasPrefix(lines[0], `{"ndb_backup":1,"version":"test"`) {
		t.Fatalf("header = %s", lines[0])
	}

	if !strings.HasPrefix(lines[len(lines)-1], `{"_section":"end","lines":`) {
		t.Fatalf("last line = %s", lines[len(lines)-1])
	}

	stage := filepath.Join(t.TempDir(), "stage")

	if err := backup.Extract(buf, stage); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(stage, "catalog.bolt")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractReadsGzipByMagicBytes(t *testing.T) {
	plain := bytes.NewBuffer(fixture(t, backup.Options{}))

	var gz bytes.Buffer

	zw := gzip.NewWriter(&gz)
	zw.Write(plain.Bytes())
	zw.Close()

	if err := backup.Extract(&gz, filepath.Join(t.TempDir(), "stage")); err != nil {
		t.Fatal(err)
	}
}

func TestExtractRejectsBadInput(t *testing.T) {
	whole := string(fixture(t, backup.Options{}))
	lines := strings.Split(strings.TrimSpace(whole), "\n")

	cases := map[string]string{
		"not ndb":      "hello\nworld\n",
		"empty":        "",
		"no end line":  strings.Join(lines[:len(lines)-1], "\n") + "\n",
		"cut mid line": whole[:len(whole)-10],
		"bad crc":      strings.Replace(whole, `"collections":["log"]`, `"collections":["log" ]`, 1),
		"after end":    whole + "{}\n",
	}

	for name, in := range cases {
		stage := filepath.Join(t.TempDir(), "stage")

		if err := backup.Extract(strings.NewReader(in), stage); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
