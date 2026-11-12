package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/logging"
)

// The settings of the daemon, GET and PUT /_cluster/settings. They are
// stored in the data dir (internal/settings), so a PUT holds across a
// restart and goes in the backups. PUT /_log changes the same log settings
// until the next start only.
//
// The answer gives each setting with where its value comes from: flag, env
// or yml (set when the daemon started; they win at the next start), stored
// (PUT /_cluster/settings), or default. The encryption key is shown masked
// and cannot change.

// logSettings are the log settings that /_cluster/settings stores, by their
// name in the answer and their key in the store.
var logSettings = []struct{ name, key string }{
	{"level", "log.level"},
	{"body_max", "log.body_max"},
	{"slow_ms", "log.slow_ms"},
}

// logSources is where each log setting came from at start.
type logSources struct {
	mu  sync.Mutex
	src map[string]string
}

// SetLogSources tells where the log settings came from at start (by name:
// level, body_max, slow_ms; a name not given is default).
func (s *Server) SetLogSources(src map[string]string) {
	s.logSrc.mu.Lock()
	defer s.logSrc.mu.Unlock()

	s.logSrc.src = src
}

func (ls *logSources) get(name string) string {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if v := ls.src[name]; v != "" {
		return v
	}

	return "default"
}

func (ls *logSources) set(name, source string) {
	ls.mu.Lock()
	defer ls.mu.Unlock()

	if ls.src == nil {
		ls.src = map[string]string{}
	}

	ls.src[name] = source
}

// DB is the attached engine, or nil while the daemon recovers.
func (s *Server) DB() *engine.DB {
	return s.db.Load()
}

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	if r.PathValue("id") != "settings" {
		unknownRoute(w)

		return
	}

	writeJSON(w, http.StatusOK, s.settingsAnswer(db, nil))
}

func (s *Server) settingsAnswer(db *engine.DB, warnings []string) map[string]any {
	key := db.Key()

	values := map[string]any{
		"level":    logging.LevelName(s.log.Level()),
		"body_max": s.log.BodyMax(),
		"slow_ms":  s.log.SlowMs(),
	}

	logOut := map[string]any{}

	for _, ls := range logSettings {
		logOut[ls.name] = map[string]any{"value": values[ls.name], "source": s.logSrc.get(ls.name)}
	}

	enc := map[string]any{
		"key":         key.Masked(),
		"key_id":      key.ID,
		"fingerprint": key.Fingerprint(),
		"source":      key.Source,
		"created_at":  key.Created.UTC().Format(time.RFC3339),
	}

	if rot := db.RotationState(); rot.From != "" {
		enc["rotation"] = map[string]any{"from": rot.From, "pending": append([]string{}, rot.Pending...)}
	}

	out := map[string]any{
		"encryption": enc,
		"log":        logOut,
	}

	if len(warnings) > 0 {
		out["warnings"] = warnings
	}

	return out
}

type settingsReq struct {
	Log *struct {
		Level   *string `json:"level"`
		BodyMax *int    `json:"body_max"`
		SlowMs  *int    `json:"slow_ms"`
	} `json:"log"`
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request, db *engine.DB) {
	if r.PathValue("id") != "settings" {
		unknownRoute(w)

		return
	}

	vals, err := s.readSettings(w, r)
	if err != nil {
		writeError(w, err)

		return
	}

	stored := map[string]json.RawMessage{}

	for name, v := range vals {
		raw, _ := json.Marshal(v)
		stored["log."+name] = raw
	}

	if err := db.Settings().Set(stored); err != nil {
		writeError(w, err)

		return
	}

	var warnings []string

	for _, ls := range logSettings {
		v, ok := vals[ls.name]
		if !ok {
			continue
		}

		if from := s.logSrc.get(ls.name); from == "flag" || from == "env" || from == "yml" {
			warnings = append(warnings, fmt.Sprintf("%s is set by the %s of the daemon: it wins at the next start", ls.key, from))
		}

		s.applyLog(ls.name, v)
		s.logSrc.set(ls.name, "stored")
	}

	s.logInfo(w, r, "settings changed", slog.Any("log", vals))

	writeJSON(w, http.StatusOK, s.settingsAnswer(db, warnings))
}

// readSettings reads and checks the body of a PUT: the log settings to set,
// by name.
func (s *Server) readSettings(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	body, err := readBody(w, r, 4096)
	if err != nil {
		return nil, err
	}

	var top map[string]json.RawMessage

	if err := json.Unmarshal(body, &top); err != nil {
		return nil, badRequest("the body must be a JSON object")
	}

	for k := range top {
		switch k {
		case "log":
		case "encryption":
			return nil, badRequest("the encryption key cannot change (no rotation yet); to use your own key, set NDB_ENCRYPTION_KEY on a new data dir")
		default:
			return nil, badRequest("unknown setting %q: the settings are log.level, log.body_max and log.slow_ms", k)
		}
	}

	var req settingsReq

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil || req.Log == nil {
		return nil, badRequest("the body is {\"log\": {\"level\", \"body_max\", \"slow_ms\"}}")
	}

	vals := map[string]any{}

	if req.Log.Level != nil {
		if _, err := logging.ParseLevel(*req.Log.Level); err != nil || *req.Log.Level == "" {
			return nil, badRequest("log.level must be debug, info, warn, error or quiet")
		}

		vals["level"] = *req.Log.Level
	}

	for name, v := range map[string]*int{"body_max": req.Log.BodyMax, "slow_ms": req.Log.SlowMs} {
		if v == nil {
			continue
		}

		if *v < 0 {
			return nil, badRequest("log.%s must be zero or more", name)
		}

		vals[name] = *v
	}

	if len(vals) == 0 {
		return nil, badRequest("nothing to set")
	}

	return vals, nil
}

// applyLog sets one log setting on the logger. The value was checked.
func (s *Server) applyLog(name string, v any) {
	switch name {
	case "level":
		level, _ := logging.ParseLevel(v.(string))
		s.log.SetLevel(level)
	case "body_max":
		s.log.SetBodyMax(v.(int))
	case "slow_ms":
		s.log.SetSlowMs(v.(int))
	}
}

// ApplyStoredLog sets the log settings stored in the data dir, except the
// ones a flag, the env or ndb.yml set at start. The daemon calls it once,
// after recovery.
func (s *Server) ApplyStoredLog(db *engine.DB) {
	for _, ls := range logSettings {
		if s.logSrc.get(ls.name) != "default" {
			continue
		}

		raw, ok := db.Settings().Get(ls.key)
		if !ok {
			continue
		}

		var v any

		if ls.name == "level" {
			var level string

			if json.Unmarshal(raw, &level) != nil {
				continue
			}

			if _, err := logging.ParseLevel(level); err != nil {
				continue
			}

			v = level
		} else {
			var n int

			if json.Unmarshal(raw, &n) != nil || n < 0 {
				continue
			}

			v = n
		}

		s.applyLog(ls.name, v)
		s.logSrc.set(ls.name, "stored")
	}
}
