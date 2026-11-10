package logging_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/logging"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		"":        slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		" quiet ": logging.LevelQuiet,
	}

	for in, want := range cases {
		got, err := logging.ParseLevel(in)

		if err != nil || got != want {
			t.Fatalf("ParseLevel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}

	if _, err := logging.ParseLevel("trace"); err == nil {
		t.Fatal("ParseLevel(trace): want an error")
	}

	for _, name := range []string{"debug", "info", "warn", "error", "quiet"} {
		l, _ := logging.ParseLevel(name)

		if got := logging.LevelName(l); got != name {
			t.Fatalf("LevelName(%v) = %q, want %q", l, got, name)
		}
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]string{"text": "text", "JSON": "json", "": "text"} {
		if got, err := logging.ParseFormat(in); err != nil || got != want {
			t.Fatalf("ParseFormat(%q) = %q, %v", in, got, err)
		}
	}

	if _, err := logging.ParseFormat("xml"); err == nil {
		t.Fatal("ParseFormat(xml): want an error")
	}
}

func TestQuietWritesNothing(t *testing.T) {
	var buf bytes.Buffer

	l := logging.New(&buf, logging.Options{Level: logging.LevelQuiet, Format: "json"})

	l.Error("boom")
	l.Warn("w")
	l.Info("i")
	l.Debug("d")

	if buf.Len() != 0 {
		t.Fatalf("quiet wrote %q", buf.String())
	}

	if l.Enabled(context.Background(), slog.LevelError) {
		t.Fatal("quiet: error is enabled")
	}
}

func TestSetLevelAtRunTime(t *testing.T) {
	var buf bytes.Buffer

	l := logging.New(&buf, logging.Options{Level: slog.LevelInfo, Format: "text"})

	l.Debug("hidden")
	l.SetLevel(slog.LevelDebug)
	l.Debug("shown")

	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "shown") {
		t.Fatalf("log = %q", buf.String())
	}
}

func TestCut(t *testing.T) {
	if got := logging.Cut([]byte("short"), 10); got != "short" {
		t.Fatalf("Cut = %q", got)
	}

	if got := logging.Cut([]byte("0123456789abc"), 4); got != "0123...[cut, 13 bytes]" {
		t.Fatalf("Cut = %q", got)
	}

	// "é" is two bytes: a cut in its middle goes back to its start.
	if got := logging.Cut([]byte("aé-long"), 2); got != "a...[cut, 8 bytes]" {
		t.Fatalf("Cut on a rune = %q", got)
	}
}

func TestBodyZeroMaxIsEmpty(t *testing.T) {
	l := logging.New(&bytes.Buffer{}, logging.Options{Level: slog.LevelDebug, BodyMax: 0})

	if a := l.Body("body", []byte(`{"a":1}`)); !a.Equal(slog.Attr{}) {
		t.Fatalf("Body with max 0 = %v, want an empty attr", a)
	}
}
