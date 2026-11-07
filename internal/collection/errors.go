package collection

import (
	"errors"
	"fmt"
)

// Error is a write error with a stable code for the API and SDKs.
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

func errf(code, format string, args ...any) *Error {
	return &Error{code: code, msg: fmt.Sprintf(format, args...)}
}

// isCode tells if err is an *Error with this code.
func isCode(err error, code string) bool {
	var e *Error

	return errors.As(err, &e) && e.code == code
}
