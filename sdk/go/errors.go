package ndb

import "fmt"

// Error is an error that the daemon answered. Code is the stable code of
// the HTTP API, and Status is the HTTP status. Test a code with errors.Is
// and one of the sentinels below.
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "ndb: " + e.Code
	}

	return fmt.Sprintf("ndb: %s: %s", e.Code, e.Message)
}

// Is reports whether target is a sentinel with the same code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)

	return ok && t.Code != "" && t.Code == e.Code
}

// Sentinels for errors.Is. Each one matches every error with its code.
var (
	ErrNotFound          = &Error{Code: "not_found"}
	ErrDuplicateID       = &Error{Code: "duplicate_id"}
	ErrUniqueViolation   = &Error{Code: "unique_violation"}
	ErrCatalogConflict   = &Error{Code: "catalog_conflict"}
	ErrCatalogInvalid    = &Error{Code: "catalog_invalid"}
	ErrUnknownCollection = &Error{Code: "unknown_collection"}
	ErrDirectCollection  = &Error{Code: "direct_collection"}
	ErrInvalidQuery      = &Error{Code: "invalid_query"}
	ErrInvalidPipeline   = &Error{Code: "invalid_pipeline"}
	ErrGroupLimit        = &Error{Code: "group_limit_exceeded"}
	ErrCursorInvalid     = &Error{Code: "cursor_invalid"}
	ErrCursorMismatch    = &Error{Code: "cursor_mismatch"}
	ErrCursorExpired     = &Error{Code: "cursor_expired"}
	ErrRequestTooLarge   = &Error{Code: "request_too_large"}
	ErrLineTooLarge      = &Error{Code: "line_too_large"}
	ErrUnauthorized      = &Error{Code: "unauthorized"}
	ErrNotReady          = &Error{Code: "not_ready"}
	ErrClosed            = &Error{Code: "closed"}
	ErrWatchLagged       = &Error{Code: "watch_lagged"}
	ErrInvalidPosition   = &Error{Code: "invalid_position"}
	ErrHistoryTruncated  = &Error{Code: "history_truncated"}
	ErrTTLNotEnabled     = &Error{Code: "ttl_not_enabled"}
	ErrTTLDerived        = &Error{Code: "ttl_derived"}
	ErrUnknownOperator   = &Error{Code: "unknown_operator"}
	ErrNoUpdate          = &Error{Code: "no_update"}
	ErrConflictingUpdate = &Error{Code: "conflicting_update"}
	ErrInvalidField      = &Error{Code: "invalid_field"}
	ErrUnindexedMany     = &Error{Code: "unindexed_many"}
	ErrEmptyFilter       = &Error{Code: "empty_filter"}
	ErrConfirmRequired   = &Error{Code: "confirm_required"}
	ErrCollectionExists  = &Error{Code: "collection_exists"}
)
