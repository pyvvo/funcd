package secrets

import "github.com/pyvvo/funcd/api/fault"

// The known causes of the secret service's errors (ADR-0330 Decision 2): this package's resolver and
// internal/services/secret raise them, and each endpoint of the worker node's local API that answers one declares it.
var (
	ErrCallerMissing = fault.NewKnownCause(fault.NotFound, "secrets.caller-missing",
		"The calling Function no longer exists, so it binds no Secret.",
		"Call from a Function that exists; a deleted Function's worker stops.")
	ErrSecretMissing = fault.NewKnownCause(fault.NotFound, "secrets.secret-missing",
		"A Secret that the Function binds does not exist.",
		"Create the Secret, or remove it from the Function's spec.bindings.secret.")
	ErrReadDenied = fault.NewKnownCause(fault.Forbidden, "secrets.read-denied",
		"A Policy forbids the Function to read a Secret that it binds.",
		"Change or remove the Policy that forbids secret::read on the Secret.")
	ErrValueNotText = fault.NewKnownCause(fault.Invalid, "secrets.value-not-text",
		"A Secret's value is not UTF-8 text, or it holds a NUL byte.",
		"Store the value as UTF-8 text with no NUL byte, such as in base64.")
	ErrNoBinding = fault.NewKnownCause(fault.Forbidden, "secrets.no-binding",
		"The Function's spec.bindings.secret does not list the Secret.",
		"Add the Secret to spec.bindings.secret, or write a Secret that the Function binds.")
	ErrWriteDenied = fault.NewKnownCause(fault.Forbidden, "secrets.write-denied",
		"The Function does not own the Secret, no RolesAssignment makes it a writer, or a Policy forbids the write.",
		"Write from the Secret's owner, or grant the Function secret::write with a RolesAssignment, such as the Secret "+
			"Data Writer role.")
	ErrKeyInvalid = fault.NewKnownCause(fault.Invalid, "secrets.key-invalid",
		"The key is not an environment-variable name: a letter or an underscore, then letters, digits and underscores.",
		"Use such a key.")
	ErrControlPlaneAway = fault.NewKnownCause(fault.Unavailable, "secrets.control-plane-away",
		"The Function's worker node does not reach its control plane, which holds the Secret and makes the write.",
		"Retry the write; reads go on meanwhile.")
	ErrWriteContended = fault.NewKnownCause(fault.Conflict, "secrets.write-contended",
		"The Secret changed under each attempt to write it, as when other writes of it run at the same time.",
		"Retry the write.")
)
