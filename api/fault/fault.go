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
	// InsufficientStorage means a write would take a store past its quota (507 Insufficient Storage, ADR-0292).
	InsufficientStorage Kind = "insufficient_storage"
	// DeadlineExceeded means the upstream did not start its response in time (504 Gateway Timeout, ADR-0151).
	DeadlineExceeded Kind = "deadline_exceeded"
	// PreconditionFailed means a conditional write's condition did not hold, so nothing was written (412).
	PreconditionFailed Kind = "precondition_failed"
	// Internal means an unexpected internal error occurred.
	Internal Kind = "internal"
)

// Error is the typed error used throughout the platform. It carries a Kind (for
// HTTP mapping), an Op (free string describing the operation that failed), a
// human-readable Msg, optional flat Detail (key-value pairs), the wrapped
// error via Err, and the KnownCause it was raised from, if any (ADR-0330).
type Error struct {
	Kind   Kind
	Op     string            // e.g. "store.Get", "blob.Put" — free string, open set
	Msg    string            // human-readable, safe to surface to callers
	Detail map[string]string // deliberately flat, key-value diagnostic pairs
	Err    error             // wrapped error
	// KnownCause names why the error happened; Error() does not print it, so a problem's detail stays the same.
	KnownCause *KnownCause
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Msg, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Msg)
}

// Unwrap returns the wrapped error, enabling errors.As and errors.Is traversal.
func (e *Error) Unwrap() error {
	return e.Err
}

// Is reports whether target is e's KnownCause, so errors.Is finds a known cause through any wrapping.
func (e *Error) Is(target error) bool {
	return e.KnownCause != nil && target == error(e.KnownCause)
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

// InsufficientStoragef builds an InsufficientStorage error (507) with a formatted message.
func InsufficientStoragef(op, format string, a ...any) *Error {
	return &Error{Kind: InsufficientStorage, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// DeadlineExceededf builds a DeadlineExceeded error (504) with a formatted message.
func DeadlineExceededf(op, format string, a ...any) *Error {
	return &Error{Kind: DeadlineExceeded, Op: op, Msg: fmt.Sprintf(format, a...)}
}

// PreconditionFailedf builds a PreconditionFailed error (412) with a formatted message.
func PreconditionFailedf(op, format string, a ...any) *Error {
	return &Error{Kind: PreconditionFailed, Op: op, Msg: fmt.Sprintf(format, a...)}
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

// KnownCause is one named reason for an error, made only by NewKnownCause; it is an error, so errors.Is can find it.
// Each is defined once, beside the code that raises it, as io.EOF is (ADR-0330 Decision 1).
type KnownCause struct {
	kind            Kind
	name, when, fix string
}

// NewKnownCause names its known cause "<package>.<name>", e.g. "kvstore.value-over-cap"; when and fix are one sentence
// each, written for the caller who gets the error.
func NewKnownCause(k Kind, name, when, fix string) *KnownCause {
	return &KnownCause{kind: k, name: name, when: when, fix: fix}
}

// Name is the cause's "<package>.<name>".
func (c *KnownCause) Name() string { return c.name }

// Kind is the kind of every error raised from the cause.
func (c *KnownCause) Kind() Kind { return c.kind }

// When says when the cause happens.
func (c *KnownCause) When() string { return c.when }

// Fix says what the caller does about it.
func (c *KnownCause) Fix() string { return c.fix }

// Error is the cause's name.
func (c *KnownCause) Error() string { return c.name }

// Errorf builds an Error of c's kind whose KnownCause is c, with a formatted message.
func (c *KnownCause) Errorf(op, format string, a ...any) *Error {
	return &Error{Kind: c.kind, Op: op, Msg: fmt.Sprintf(format, a...), KnownCause: c}
}

// Wrapf is Errorf around a lower error, which the Error wraps as Wrapf does.
func (c *KnownCause) Wrapf(err error, op, format string, a ...any) *Error {
	return &Error{Kind: c.kind, Op: op, Msg: fmt.Sprintf(format, a...), Err: err, KnownCause: c}
}

// KnownCauseOf returns the known cause of the first Error in err's chain that has one, as KindOf returns the first
// Error's kind: the outermost cause wins. It returns nil when no Error of the chain has one.
func KnownCauseOf(err error) *KnownCause {
	var ferr *Error
	for errors.As(err, &ferr) {
		if ferr.KnownCause != nil {
			return ferr.KnownCause
		}
		err = ferr.Err
	}
	return nil
}
