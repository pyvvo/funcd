package local

import (
	"fmt"

	"github.com/pyvvo/funcd/api/fault"
)

// The known causes that only the local API raises (ADR-0330 Decision 2): a missing key, a condition that does not
// hold, readBody's, and the KV routes' headers and ttl.
var (
	ErrKeyNotFound = fault.NewKnownCause(fault.NotFound, "local.key-not-found",
		"The key does not exist, or it expired.",
		"Write the key first, or handle its absence.")
	ErrKeyAbsent = fault.NewKnownCause(fault.PreconditionFailed, "local.key-absent",
		"The write names a version with If-Match, but the key does not exist.",
		"Read the key again, or create it with If-None-Match: *.")
	ErrVersionMismatch = fault.NewKnownCause(fault.PreconditionFailed, "local.version-mismatch",
		"The key is at another version than If-Match names, or exists although If-None-Match: * asks that it does not; the answer's ETag is its version.",
		"Read the key again, then retry with its version.")
	ErrBodyTooLarge = fault.NewKnownCause(fault.PayloadTooLarge, "local.body-too-large",
		"The request's body is larger than the local API takes for the call.",
		"Send a smaller body.")
	ErrBodyUnreadable = fault.NewKnownCause(fault.Invalid, "local.body-unreadable",
		"funcd could not read the request's body, as when its chunked encoding is broken.",
		"Send a well-formed body.")
	ErrIfNoneMatchRepeated = fault.NewKnownCause(fault.Invalid, "local.if-none-match-repeated",
		"If-None-Match is sent on more than one line.",
		"Send one If-None-Match: * line.")
	ErrIfNoneMatchNotStar = fault.NewKnownCause(fault.Invalid, "local.if-none-match-not-star",
		"A write's If-None-Match is not *.",
		"Send If-None-Match: * to create the key, or If-Match to replace it.")
	ErrConditionsBoth = fault.NewKnownCause(fault.Invalid, "local.conditions-both",
		"A write sends both If-None-Match and If-Match.",
		"Send one of them.")
	ErrTTLInvalid = fault.NewKnownCause(fault.Invalid, "local.ttl-invalid",
		fmt.Sprintf("The ttl is not a whole number of seconds from 1 to %d.", maxTTLSeconds),
		"Send a ttl in that range.")
	ErrDeleteIfNoneMatch = fault.NewKnownCause(fault.Invalid, "local.delete-if-none-match",
		"A delete sends If-None-Match.",
		"Send If-Match with the key's version, or no condition.")
	ErrIfMatchRepeated = fault.NewKnownCause(fault.Invalid, "local.if-match-repeated",
		"If-Match is sent on more than one line.",
		"Send one If-Match line with one version.")
	ErrIfMatchMalformed = fault.NewKnownCause(fault.Invalid, "local.if-match-malformed",
		"The version is empty or holds a space, a quote or a control character, so If-Match is not one strong entity-tag.",
		"Pass the version that a read or a write of the key returned.")
)
