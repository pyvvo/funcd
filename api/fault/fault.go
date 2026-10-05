// Package fault is the shared error taxonomy for the funcd platform (ADR-0002).
// It is stdlib-only and imported "up" by internal/** and pkg/**; it imports nothing
// beyond the standard library. Every layer builds errors from this package; the API
// edge maps them to RFC 9457 application/problem+json.
package fault

import (
	"errors"
	"fmt"
)

// Kind is the machine-readable error kind — the stable identifier that maps to HTTP
// status codes and problem+json type URIs. Kinds are an open set but the enumerated
// constants below cover every case the platform needs.
type Kind string

const (
	// Invalid means the request is malformed or a required dependency is missing.
	Invalid Kind = "invalid"
	// NotFound means the requested resource does not exist.
	NotFound Kind = "not_found"
	// Conflict means the request conflicts with the current state (e.g. duplicate name).
	Conflict Kind = "conflict"
	// Unauthorized means authentication is required but was not provided or is invalid.
	Unauthorized Kind = "unauthorized"
	// Forbidden means the authenticated identity lacks permission.
	Forbidden Kind = "forbidden"
	// Unavailable means the service is temporarily unable to handle the request.
	Unavailable Kind = "unavailable"
	// ResourceExhausted means a rate/quota limit was exceeded (429 Too Many Requests).
	ResourceExhausted Kind = "resource_exhausted"
	// PayloadTooLarge means a payload, key, object or record is over a size cap funcd enforces (413
	// Content Too Large when it answers HTTP).
	PayloadTooLarge Kind = "payload_too_large"
	// Internal means an unexpected internal error occurred.
	Internal Kind = "internal"
)

// Error is the typed error used throughout the platform. It carries a Kind (for
// HTTP mapping), an Op (free string describing the operation that failed), a
// human-readable Msg, optional flat Detail (key-value pairs), and the wrapped
// cause via Err.
type Error struct {
	Kind   Kind
	Op     string            // e.g. "store.Get", "blob.Put" — free string, open set
	Msg    string            // human-readable, safe to surface to callers
	Detail map[string]string // deliberately flat, key-value diagnostic pairs
	Err    error             // wrapped cause
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Msg)
}

// Unwrap returns the wrapped cause, enabling errors.As and errors.Is traversal.
func (e *Error) Unwrap() error {
	return e.Err
}

// KindOf walks the error chain via errors.As and returns the first Kind found.
// If no Kind is found it returns Internal — unknown errors are internal faults.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	var ferr *Error
	if errors.As(err, &ferr) {
		return ferr.Kind
	}
	return Internal
}

// NotFoundf builds a NotFound error with a formatted message.
func NotFoundf(op, format string, a ...any) *Error {
	return &Error{Kind: NotFound, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// Invalidf builds an Invalid error with a formatted message.
func Invalidf(op, format string, a ...any) *Error {
	return &Error{Kind: Invalid, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// Conflictf builds a Conflict error with a formatted message.
func Conflictf(op, format string, a ...any) *Error {
	return &Error{Kind: Conflict, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// Unauthorizedf builds an Unauthorized error with a formatted message.
func Unauthorizedf(op, format string, a ...any) *Error {
	return &Error{Kind: Unauthorized, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// Forbiddenf builds a Forbidden error with a formatted message.
func Forbiddenf(op, format string, a ...any) *Error {
	return &Error{Kind: Forbidden, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// Unavailablef builds an Unavailable error with a formatted message.
func Unavailablef(op, format string, a ...any) *Error {
	return &Error{Kind: Unavailable, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// ResourceExhaustedf builds a ResourceExhausted error (429) with a formatted message.
func ResourceExhaustedf(op, format string, a ...any) *Error {
	return &Error{Kind: ResourceExhausted, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// PayloadTooLargef builds a PayloadTooLarge error (413) with a formatted message.
func PayloadTooLargef(op, format string, a ...any) *Error {
	return &Error{Kind: PayloadTooLarge, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// Internalf builds an Internal error with a formatted message.
func Internalf(op, format string, a ...any) *Error {
	return &Error{Kind: Internal, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// Wrapf wraps an existing error with a Kind, Op, and formatted message. It is the
// primary tool for annotating errors as they propagate up through layers.
func Wrapf(err error, k Kind, op, format string, a ...any) *Error {
	msg := fmt.Sprintf(format, a...)
	return &Error{Kind: k, Op: op, Msg: msg, Err: err}
}
