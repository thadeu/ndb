package wal_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/thadeu/ndb/internal/wal"
)

type got struct {
	line string
	off  int64
	err  error
}

func readAll(t *testing.T, input string, maxLine int) []got {
	t.Helper()

	r := wal.NewLineReader(strings.NewReader(input), 0, maxLine)

	var out []got

	for {
		line, off, err := r.Next()

		if errors.Is(err, io.EOF) {
			return out
		}

		out = append(out, got{string(line), off, err})

		if errors.Is(err, wal.ErrPartialLine) {
			return out
		}
	}
}

func TestLineReader(t *testing.T) {
	big := strings.Repeat("x", 64*1024)

	tests := []struct {
		name  string
		input string
		max   int
		want  []got
	}{
		{"empty input", "", 100, nil},
		{"empty line", "\n", 100, []got{{"", 0, nil}}},
		{"two lines", "a\nbc\n", 100, []got{{"a", 0, nil}, {"bc", 2, nil}}},
		{"crlf", "a\r\nb\n", 100, []got{{"a", 0, nil}, {"b", 3, nil}}},
		{"no final newline", "a\nbc", 100, []got{{"a", 0, nil}, {"bc", 2, wal.ErrPartialLine}}},
		{"line of 64 KiB", big + "\n", 1 << 20, []got{{big, 0, nil}}},
		{"line at the limit", "abcde\n", 5, []got{{"abcde", 0, nil}}},
		{"line over the limit is skipped", "abcdef\nok\n", 5, []got{{"", 0, wal.ErrLineTooLarge}, {"ok", 7, nil}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := readAll(t, tt.input, tt.max)

			if len(out) != len(tt.want) {
				t.Fatalf("got %d results %v, want %d", len(out), out, len(tt.want))
			}

			for i := range out {
				if out[i].line != tt.want[i].line || out[i].off != tt.want[i].off || !errors.Is(out[i].err, tt.want[i].err) {
					t.Fatalf("result %d: got %+v, want %+v", i, out[i], tt.want[i])
				}
			}
		})
	}
}

func TestLineReader16MiB(t *testing.T) {
	const limit = 16 << 20

	line := bytes.Repeat([]byte("y"), limit)
	input := append(append(line, '\n'), append(append([]byte{}, line...), 'z', '\n')...)

	r := wal.NewLineReader(bytes.NewReader(input), 0, limit)

	first, _, err := r.Next()
	if err != nil || len(first) != limit {
		t.Fatalf("16 MiB line: len=%d err=%v", len(first), err)
	}

	_, off, err := r.Next()
	if !errors.Is(err, wal.ErrLineTooLarge) || off != limit+1 {
		t.Fatalf("16 MiB + 1 line: off=%d err=%v", off, err)
	}
}

func TestLineReaderStartOffset(t *testing.T) {
	r := wal.NewLineReader(strings.NewReader("x\n"), 1000, 100)

	_, off, err := r.Next()
	if err != nil || off != 1000 {
		t.Fatalf("off=%d err=%v", off, err)
	}

	if r.Offset() != 1002 {
		t.Fatalf("Offset()=%d", r.Offset())
	}
}

func FuzzLineReader(f *testing.F) {
	f.Add([]byte("a\nbb\r\nccc"), 2)
	f.Add([]byte("\n\n\n"), 1)

	f.Fuzz(func(t *testing.T, data []byte, maxLine int) {
		if maxLine < 1 || maxLine > 1<<16 {
			return
		}

		r := wal.NewLineReader(bytes.NewReader(data), 0, maxLine)
		prev := int64(-1)

		for {
			line, off, err := r.Next()

			if errors.Is(err, io.EOF) {
				break
			}

			if off <= prev || off > int64(len(data)) {
				t.Fatalf("bad offset %d after %d", off, prev)
			}

			prev = off

			if len(line) > maxLine {
				t.Fatalf("line of %d bytes over limit %d", len(line), maxLine)
			}

			if bytes.IndexByte(line, '\n') >= 0 {
				t.Fatal("line contains a newline")
			}

			if errors.Is(err, wal.ErrPartialLine) {
				break
			}
		}

		if r.Offset() > int64(len(data)) {
			t.Fatalf("Offset %d past input %d", r.Offset(), len(data))
		}
	})
}
