package wal

import (
	"bufio"
	"errors"
	"io"
)

var (
	// ErrLineTooLarge: the line is over the limit. The reader skipped it and
	// is ready for the next line.
	ErrLineTooLarge = errors.New("wal: line too large")

	// ErrPartialLine: input ended with bytes but no '\n'. The bytes are
	// returned; Offset() stays at the start of the partial line.
	ErrPartialLine = errors.New("wal: partial line at end of input")
)

// LineReader reads '\n'-terminated lines with a size limit. It never uses
// bufio.Scanner, whose 64 KiB default fails on large lines.
type LineReader struct {
	r   *bufio.Reader
	max int
	off int64
	buf []byte
}

// NewLineReader reads from r, whose first byte is at offset start.
func NewLineReader(r io.Reader, start int64, maxLine int) *LineReader {
	return &LineReader{r: bufio.NewReaderSize(r, 64*1024), max: maxLine, off: start}
}

// Offset is the position after the last full line.
func (lr *LineReader) Offset() int64 {
	return lr.off
}

// Next returns the next line without '\n' (and without a final '\r'), and the
// offset where it starts. The line is valid until the next call.
func (lr *LineReader) Next() ([]byte, int64, error) {
	start := lr.off
	lr.buf = lr.buf[:0]
	consumed := 0
	tooLarge := false

	for {
		chunk, err := lr.r.ReadSlice('\n')
		consumed += len(chunk)

		if !tooLarge {
			lr.buf = append(lr.buf, chunk...)

			if len(lr.buf) > lr.max+2 {
				tooLarge = true
				lr.buf = lr.buf[:0]
			}
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if err == nil {
			lr.off += int64(consumed)

			return lr.finish(start, tooLarge)
		}

		if errors.Is(err, io.EOF) {
			return lr.eof(start, consumed, tooLarge)
		}

		return nil, start, err
	}
}

func (lr *LineReader) finish(start int64, tooLarge bool) ([]byte, int64, error) {
	if tooLarge {
		return nil, start, ErrLineTooLarge
	}

	line := lr.buf[:len(lr.buf)-1]

	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}

	if len(line) > lr.max {
		return nil, start, ErrLineTooLarge
	}

	return line, start, nil
}

func (lr *LineReader) eof(start int64, consumed int, tooLarge bool) ([]byte, int64, error) {
	if consumed == 0 {
		return nil, start, io.EOF
	}

	if tooLarge || len(lr.buf) > lr.max {
		return nil, start, ErrPartialLine
	}

	return lr.buf, start, ErrPartialLine
}
