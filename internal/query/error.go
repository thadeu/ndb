package query

import "fmt"

// Error has a stable code for the API and SDKs: invalid_query,
// cursor_invalid, cursor_mismatch.
type Error struct {
	code string
	msg  string
}

func (e *Error) Error() string {
	return e.code + ": " + e.msg
}

func (e *Error) Code() string {
	return e.code
}

// NewError builds an error with a stable code.
func NewError(code, format string, args ...any) *Error {
	return &Error{code: code, msg: fmt.Sprintf(format, args...)}
}

func invalid(format string, args ...any) *Error {
	return NewError("invalid_query", format, args...)
}
