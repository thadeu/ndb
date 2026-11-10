// Package logging is the one logger of the daemon: log/slog with the levels
// debug, info, warn, error and quiet (nothing at all). The level, the size
// of a body in a line and the slow request threshold change at run time.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// LevelQuiet is above every level that ndb uses, so nothing is written.
const LevelQuiet = slog.Level(12)

// The defaults of log_level, log_format, log_body_max and log_slow_ms.
const (
	DefaultLevel   = "info"
	DefaultFormat  = "text"
	DefaultBodyMax = 2048
	DefaultSlowMs  = 1000
)

// ParseLevel reads debug, info, warn, error or quiet (any case).
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info", "":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	case "quiet":
		return LevelQuiet, nil
	}

	return 0, fmt.Errorf("log level %q: want debug, info, warn, error or quiet", s)
}

// LevelName is the name that ParseLevel reads.
func LevelName(l slog.Level) string {
	switch {
	case l <= slog.LevelDebug:
		return "debug"
	case l <= slog.LevelInfo:
		return "info"
	case l <= slog.LevelWarn:
		return "warn"
	case l <= slog.LevelError:
		return "error"
	}

	return "quiet"
}

// ParseFormat reads text or json (any case).
func ParseFormat(s string) (string, error) {
	switch f := strings.ToLower(strings.TrimSpace(s)); f {
	case "text", "json":
		return f, nil
	case "":
		return DefaultFormat, nil
	}

	return "", fmt.Errorf("log format %q: want text or json", s)
}

// Options are the settings of a Logger.
type Options struct {
	Level   slog.Level
	Format  string
	BodyMax int
	SlowMs  int
}

// Logger is a slog.Logger with settings that change at run time.
type Logger struct {
	*slog.Logger

	level   *slog.LevelVar
	format  string
	bodyMax atomic.Int64
	slow    atomic.Int64
}

// New writes to w with o. An unknown format is text.
func New(w io.Writer, o Options) *Logger {
	l := &Logger{level: new(slog.LevelVar), format: o.Format}

	l.level.Set(o.Level)
	l.bodyMax.Store(int64(o.BodyMax))
	l.slow.Store(int64(o.SlowMs))

	ho := &slog.HandlerOptions{Level: l.level}

	if o.Format == "json" {
		l.Logger = slog.New(slog.NewJSONHandler(w, ho))
	} else {
		l.format = "text"
		l.Logger = slog.New(slog.NewTextHandler(w, ho))
	}

	return l
}

// Discard is a Logger that writes nothing (tests, offline commands).
func Discard() *Logger {
	return New(io.Discard, Options{Level: LevelQuiet, Format: "text"})
}

func (l *Logger) Level() slog.Level {
	return l.level.Level()
}

func (l *Logger) SetLevel(v slog.Level) {
	l.level.Set(v)
}

func (l *Logger) Format() string {
	return l.format
}

// BodyMax is the largest body in a line, in bytes; 0 = no bodies.
func (l *Logger) BodyMax() int {
	return int(l.bodyMax.Load())
}

func (l *Logger) SetBodyMax(n int) {
	l.bodyMax.Store(int64(n))
}

// Slow is the duration over which a request is a slow request; 0 = never.
func (l *Logger) Slow() time.Duration {
	return time.Duration(l.slow.Load()) * time.Millisecond
}

// SlowMs is Slow in milliseconds.
func (l *Logger) SlowMs() int {
	return int(l.slow.Load())
}

func (l *Logger) SetSlowMs(n int) {
	l.slow.Store(int64(n))
}

// Body is the attr of a body cut to BodyMax, with the full size after a
// cut. With BodyMax 0 it is an empty attr, which slog does not write.
func (l *Logger) Body(key string, b []byte) slog.Attr {
	max := l.BodyMax()

	if max <= 0 || len(b) == 0 {
		return slog.Attr{}
	}

	return slog.String(key, Cut(b, max))
}

// Cut gives the first max bytes of b (on a UTF-8 boundary) and, when it
// cuts, the marker "...[cut, N bytes]" with the full size.
func Cut(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}

	n := max

	for n > 0 && !utf8.RuneStart(b[n]) {
		n--
	}

	return string(b[:n]) + "...[cut, " + strconv.Itoa(len(b)) + " bytes]"
}
