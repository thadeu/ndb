package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/logging"
)

// logFlags are the values of -log-level and -log-format, and if each was set.
type logFlags struct {
	level, format       string
	levelSet, formatSet bool
}

// logOptions picks the log settings. The order is: the flag, then the env
// (NDB_LOG_LEVEL, NDB_LOG_FORMAT), then ndb.yml, then the default. It is
// the order of the other settings: a flag overrides ndb.yml.
func logOptions(cfg config.Config, getenv func(string) string, fl logFlags) (logging.Options, error) {
	level, levelFrom := cfg.LogLevel, "log_level"
	format, formatFrom := cfg.LogFormat, "log_format"

	if v := getenv("NDB_LOG_LEVEL"); v != "" {
		level, levelFrom = v, "NDB_LOG_LEVEL"
	}

	if v := getenv("NDB_LOG_FORMAT"); v != "" {
		format, formatFrom = v, "NDB_LOG_FORMAT"
	}

	if fl.levelSet {
		level, levelFrom = fl.level, "-log-level"
	}

	if fl.formatSet {
		format, formatFrom = fl.format, "-log-format"
	}

	lv, err := logging.ParseLevel(level)
	if err != nil {
		return logging.Options{}, fmt.Errorf("%s: %w", levelFrom, err)
	}

	f, err := logging.ParseFormat(format)
	if err != nil {
		return logging.Options{}, fmt.Errorf("%s: %w", formatFrom, err)
	}

	return logging.Options{Level: lv, Format: f, BodyMax: cfg.LogBodyMax, SlowMs: cfg.LogSlowMs}, nil
}

// setupLog makes the logger of the daemon and the slog default, so the
// packages that use slog.Default (collection, writer) follow the level.
func setupLog(w io.Writer, o logging.Options) *logging.Logger {
	lg := logging.New(w, o)

	slog.SetDefault(lg.Logger)

	return lg
}
