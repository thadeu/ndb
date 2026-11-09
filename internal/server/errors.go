package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/collection"
	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/wal"
	"github.com/thadeu/ndb/internal/writer"
)

// apiError is an error the server makes itself, before the engine runs.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string {
	return e.code + ": " + e.msg
}

func badRequest(format string, args ...any) *apiError {
	return &apiError{status: http.StatusBadRequest, code: "invalid_request", msg: fmt.Sprintf(format, args...)}
}

// writeError answers with the error body of plan §12.1 and the status of
// its code. Errors without a code are internal: the message stays server-side.
func writeError(w http.ResponseWriter, err error) {
	code := codeOf(err)
	status := statusOf(code)
	msg := messageOf(err, code)

	if code == "internal" {
		slog.Error("request failed", "err", err)

		msg = "internal error"
	}

	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func codeOf(err error) string {
	var api *apiError

	if errors.As(err, &api) {
		return api.code
	}

	var coded interface{ Code() string }

	if errors.As(err, &coded) {
		return coded.Code()
	}

	switch {
	case errors.Is(err, catalog.ErrConflict):
		return "catalog_conflict"
	case errors.Is(err, catalog.ErrNotStream):
		return "not_a_stream"
	case errors.Is(err, catalog.ErrInvalid):
		return "catalog_invalid"
	case errors.Is(err, catalog.ErrUnknown):
		return "unknown_collection"
	case errors.Is(err, engine.ErrDirect):
		return "direct_collection"
	case errors.Is(err, engine.ErrNotSupported):
		return "not_supported"
	case errors.Is(err, engine.ErrOutOfWindow):
		return "out_of_window"
	case errors.Is(err, engine.ErrPITROff):
		return "pitr_off"
	case errors.Is(err, engine.ErrExists):
		return "collection_exists"
	case errors.Is(err, engine.ErrConfirm):
		return "confirm_required"
	case errors.Is(err, wal.ErrLineTooLarge):
		return "line_too_large"
	case errors.Is(err, writer.ErrClosed):
		return "closed"
	case errors.Is(err, collection.ErrStopped):
		return "stopping"
	case errors.Is(err, context.Canceled):
		return "canceled"
	}

	return "internal"
}

func statusOf(code string) int {
	switch code {
	case "not_found", "unknown_collection", "unknown_route", "unknown_group":
		return http.StatusNotFound
	case "duplicate_id", "unique_violation", "catalog_conflict", "direct_collection", "collection_exists", "group_exists":
		return http.StatusConflict
	case "request_too_large", "line_too_large":
		return http.StatusRequestEntityTooLarge
	case "method_not_allowed":
		return http.StatusMethodNotAllowed
	case "unauthorized":
		return http.StatusUnauthorized
	case "forbidden":
		return http.StatusForbidden
	case "history_truncated":
		return http.StatusGone
	case "group_limit_exceeded", "lookup_too_large":
		return http.StatusUnprocessableEntity
	case "canceled":
		return StatusClientClosed
	case "not_ready", "closed", "stopping":
		return http.StatusServiceUnavailable
	case "internal":
		return http.StatusInternalServerError
	}

	return http.StatusBadRequest
}

// messageOf drops the "code: " prefix that the engine errors carry.
func messageOf(err error, code string) string {
	var api *apiError

	if errors.As(err, &api) {
		return api.msg
	}

	return strings.TrimPrefix(err.Error(), code+": ")
}

// readBody reads at most limit bytes. A longer body is request_too_large.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))

	if err == nil {
		return body, nil
	}

	var tooBig *http.MaxBytesError

	if errors.As(err, &tooBig) {
		return nil, &apiError{status: http.StatusRequestEntityTooLarge, code: "request_too_large", msg: fmt.Sprintf("body is over %d bytes (max_line_size)", limit)}
	}

	return nil, badRequest("cannot read body: %v", err)
}

// decode reads a JSON body. Numbers stay json.Number, so big integers keep
// their digits, and an unknown field is an error.
func decode(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return badRequest("body is not valid JSON: %v", err)
	}

	return nil
}

// decodeOptional is decode, but an empty body leaves v as it is.
func decodeOptional(body []byte, v any) error {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}

	return decode(body, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(v)
}
