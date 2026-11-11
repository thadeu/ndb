package main

import (
	"log/slog"
	"testing"

	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/logging"
)

func TestLogOptionsPrecedence(t *testing.T) {
	file := config.Config{LogLevel: "warn", LogFormat: "text", LogBodyMax: 10, LogSlowMs: 20}

	env := func(vals map[string]string) func(string) string {
		return func(k string) string { return vals[k] }
	}

	cases := []struct {
		name   string
		env    map[string]string
		flags  logFlags
		level  slog.Level
		format string
	}{
		{"ndb.yml", nil, logFlags{}, slog.LevelWarn, "text"},
		{"env over ndb.yml", map[string]string{"NDB_LOG_LEVEL": "error", "NDB_LOG_FORMAT": "json"}, logFlags{}, slog.LevelError, "json"},
		{"flag over env", map[string]string{"NDB_LOG_LEVEL": "error"}, logFlags{level: "debug", levelSet: true}, slog.LevelDebug, "text"},
		{"flag quiet", nil, logFlags{level: "quiet", levelSet: true, format: "json", formatSet: true}, logging.LevelQuiet, "json"},
	}

	for _, c := range cases {
		o, err := logOptions(file, env(c.env), c.flags)

		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}

		if o.Level != c.level || o.Format != c.format || o.BodyMax != 10 || o.SlowMs != 20 {
			t.Fatalf("%s: options = %+v", c.name, o)
		}
	}

	if _, err := logOptions(file, env(map[string]string{"NDB_LOG_LEVEL": "loud"}), logFlags{}); err == nil || err.Error()[:13] != "NDB_LOG_LEVEL" {
		t.Fatalf("a bad env level: err = %v, want one that names NDB_LOG_LEVEL", err)
	}

	if _, err := logOptions(file, env(nil), logFlags{format: "xml", formatSet: true}); err == nil {
		t.Fatal("a bad -log-format: want an error")
	}
}

func TestLogSourcesFollowTheOrderOfTheFlags(t *testing.T) {
	noEnv := func(string) string { return "" }
	env := func(k string) string {
		if k == "NDB_LOG_LEVEL" {
			return "debug"
		}

		return ""
	}

	yml := config.Config{InFile: map[string]bool{"log_level": true, "slow_ms": true}}

	cases := []struct {
		cfg   config.Config
		env   func(string) string
		flag  bool
		level string
		slow  string
	}{
		{config.Config{}, noEnv, false, "", ""},
		{yml, noEnv, false, "yml", "yml"},
		{yml, env, false, "env", "yml"},
		{yml, env, true, "flag", "yml"},
	}

	for i, c := range cases {
		got := logSources(c.cfg, c.env, c.flag)

		if got["level"] != c.level || got["slow_ms"] != c.slow || got["body_max"] != "" {
			t.Errorf("case %d: %v", i, got)
		}
	}
}
