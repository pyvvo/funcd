package local

import (
	"fmt"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// The known causes that only the local API raises (ADR-0330 Decision 2): a missing key, a condition that does not
// hold, readBody's, the KV routes' headers and ttl, a presigned URL's method and expiry, and the Secret routes' missing
// key and body.
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
	ErrSignMethodInvalid = fault.NewKnownCause(fault.Invalid, "local.sign-method-invalid",
		"The method of the presigned URL is not GET, PUT or DELETE.",
		"Sign GET, PUT or DELETE.")
	ErrSignExpiryInvalid = fault.NewKnownCause(fault.Invalid, "local.sign-expiry-invalid",
		fmt.Sprintf("The expiry of the presigned URL is not a duration such as 10m or 1h30m in whole seconds from %s to %s.",
			v1.Duration(minSignExpiry), v1.Duration(maxSignExpiry)),
		"Send an expiry in that range, or none for 15 minutes.")
	ErrSecretKeyNotFound = fault.NewKnownCause(fault.NotFound, "local.secret-key-not-found",
		"No Secret that the Function binds holds the key, or the key starts with FUNCD_.",
		"Add the key to a Secret that the Function binds, or handle its absence.")
	ErrSecretBodyInvalid = fault.NewKnownCause(fault.Invalid, "local.secret-body-invalid",
		`The body of the write is not {"value": "<text>"}, as when the value is not a string.`,
		"Write the value as a string.")
)

// The known causes of the SQL endpoints (ADR-0330 Decision 2): readStatements raises those of the body, and
// internal/services/sql the others. This package holds the SQL port, and no lower one is shared by the service and the
// endpoints.
var (
	ErrSQLBodyMalformed = fault.NewKnownCause(fault.Invalid, "local.sql-body-malformed",
		"The body is not a JSON object whose statements field is a list, as when a parameter is a float NaN or infinity, which JSON cannot hold.",
		`Pass such a float as null or as a string, and send {"statements": [...]} with one ["SQL", params...] list for each statement.`)
	ErrSQLNoStatements = fault.NewKnownCause(fault.Invalid, "local.sql-no-statements",
		"The call holds no statement.",
		"Send at least one statement.")
	ErrSQLTooManyStatements = fault.NewKnownCause(fault.PayloadTooLarge, "local.sql-too-many-statements",
		fmt.Sprintf("The call holds more than %d statements.", MaxSQLStatements),
		fmt.Sprintf("Send at most %d statements in one call.", MaxSQLStatements))
	ErrSQLStatementMalformed = fault.NewKnownCause(fault.Invalid, "local.sql-statement-malformed",
		"A statement is not a list whose first item is its SQL text, or that text is empty.",
		`Send each statement as ["SQL", params...], with its SQL text.`)
	ErrSQLParameterNotScalar = fault.NewKnownCause(fault.Invalid, "local.sql-parameter-not-scalar",
		"A parameter of a statement is a list or an object, other than the one object of its named parameters.",
		"Pass each parameter as a string, a number, a boolean or null.")
	ErrSQLCallerMissing = fault.NewKnownCause(fault.Forbidden, "local.sql-caller-missing",
		"The calling Function no longer exists, so it binds no database.",
		"Call from a Function that exists; a deleted Function's worker stops.")
	ErrSQLNoBinding = fault.NewKnownCause(fault.Forbidden, "local.sql-no-binding",
		"The Function's spec.bindings.sql has no entry for the alias.",
		"Add the binding to spec.bindings.sql, or call an alias that the Function binds.")
	ErrSQLReadDenied = fault.NewKnownCause(fault.Forbidden, "local.sql-read-denied",
		"A Policy forbids the Function to read the database.",
		"Change or remove the Policy that forbids sql::read on the database.")
	ErrSQLWriteDenied = fault.NewKnownCause(fault.Forbidden, "local.sql-write-denied",
		"The Function does not own the database, no RolesAssignment makes it a writer, or a Policy forbids the write.",
		"Execute from the database's owner, or grant the Function sql::write with a RolesAssignment, such as the SQL Data Writer role.")
	// ErrEngineNotReady also marks the dependency check of a binding whose database has no Ready engine (ADR-0233
	// Decision 8).
	ErrEngineNotReady = fault.NewKnownCause(fault.Unavailable, "local.sql-engine-not-ready",
		"The database that the binding names does not exist, or its engine is not Ready yet.",
		"Create the SQLDatabase, or wait until it is Ready, then retry.")
	ErrSQLEngineUnavailable = fault.NewKnownCause(fault.Unavailable, "local.sql-engine-unavailable",
		"The database's engine does not answer, its answer broke off, or it answered with an error.",
		"Retry the call; if it keeps failing, read the SQLDatabase's status and funcd's log.")
	ErrSQLTimeout = fault.NewKnownCause(fault.Unavailable, "local.sql-timeout",
		"The call ran past sql.callTimeout.",
		"Make the statements faster, or raise sql.callTimeout.")
	ErrSQLAnswerTooLarge = fault.NewKnownCause(fault.PayloadTooLarge, "local.sql-answer-too-large",
		"The engine's answer to the call is larger than funcd reads for one call.",
		"Read fewer rows or columns in one call, as with LIMIT.")
	ErrSQLStatementFailed = fault.NewKnownCause(fault.Invalid, "local.sql-statement-failed",
		"The database refused a statement, as when its SQL is not valid, it breaks a constraint or a query writes; an execute then applies none of its statements.",
		"Correct the statement that the detail names, with the database's error.")
)

// The known causes of a call of another Function, through invoke or fetch (ADR-0330 Decision 2), which only the local
// API raises.
var (
	ErrNoLink = fault.NewKnownCause(fault.Forbidden, "local.no-link",
		"The Function's spec.bindings.links has no entry for the alias.",
		"Add the link to spec.bindings.links, or call an alias that the Function links.")
	// Admission keeps a linked Function, but the collector and an App's prune delete through the store (ADR-0170 Risks
	// accepted), so a link's target can go before the Function that links it.
	ErrLinkTargetMissing = fault.NewKnownCause(fault.NotFound, "local.link-target-missing",
		"The Function that the link targets no longer exists, as when its App deleted it before the Function that links it.",
		"Create the Function again, or link one that exists.")
	ErrTargetServesHTTP = fault.NewKnownCause(fault.Conflict, "local.target-serves-http",
		"The link's target serves HTTP, which ctx.invoke does not call.",
		"Call it with ctx.fetch, which only the TypeScript shim has, or link a Function whose protocol is cloudevent.")
	ErrTargetNotHTTP = fault.NewKnownCause(fault.Conflict, "local.target-not-http",
		"The link's target does not serve HTTP, which ctx.fetch needs.",
		"Call it with ctx.invoke, or link a Function whose protocol is http.")
	ErrLinkDenied = fault.NewKnownCause(fault.Forbidden, "local.link-denied",
		"A Policy forbids the Function to call the link's target.",
		"Change or remove the Policy that forbids link::invoke on the target.")
	ErrNestedCapReached = fault.NewKnownCause(fault.ResourceExhausted, "local.nested-cap-reached",
		"The target already has as many calls from Functions in flight as invoke.maxNestedInFlight allows.",
		"Retry once a call to the target returns, or raise invoke.maxNestedInFlight.")
	ErrReplyCutOff = fault.NewKnownCause(fault.Unavailable, "local.reply-cut-off",
		"The target's reply broke off before its end: its worker failed during the call, or the link's timeout ended it.",
		"Retry the call, and raise the link's timeout if the target needs longer.")
	ErrReplyTooLarge = fault.NewKnownCause(fault.PayloadTooLarge, "local.reply-too-large",
		fmt.Sprintf("The target's reply is larger than %d MiB, the most that a call carries back.", maxInvokeBytes>>20),
		"Return a smaller reply, or pass the data through a Bucket.")
	ErrFetchPathMalformed = fault.NewKnownCause(fault.NotFound, "local.fetch-path-malformed",
		"The path names no alias: it is not /fetch/<alias>/<path>.",
		"Pass the alias of a link, and a path that starts with /.")
)
