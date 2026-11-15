package kvstore

import "github.com/pyvvo/funcd/api/fault"

// The known causes of the KV service's errors (ADR-0330 Decision 2): internal/services/kv raises them, and each
// endpoint of the worker node's local API that answers one declares it.
var (
	ErrCallerMissing = fault.NewKnownCause(fault.Forbidden, "kvstore.caller-missing",
		"The calling Function no longer exists, so it binds no table.",
		"Call from a Function that exists; a deleted Function's worker stops.")
	ErrNoBinding = fault.NewKnownCause(fault.Forbidden, "kvstore.no-binding",
		"The Function's spec.bindings.kv has no entry for the alias.",
		"Add the binding to spec.bindings.kv, or call an alias that the Function binds.")
	ErrStoreMissing = fault.NewKnownCause(fault.Forbidden, "kvstore.store-missing",
		"The binding names a KVStore that does not exist.",
		"Create the KVStore, or bind one that exists.")
	ErrTableMissing = fault.NewKnownCause(fault.Forbidden, "kvstore.table-missing",
		"The binding names a table that its KVStore does not declare.",
		"Add the table to the KVStore's spec.tables, or bind a table that it declares.")
	ErrReadDenied = fault.NewKnownCause(fault.Forbidden, "kvstore.read-denied",
		"A Policy forbids the Function to read the table.",
		"Change or remove the Policy that forbids kv::read on the table.")
	ErrWriteDenied = fault.NewKnownCause(fault.Forbidden, "kvstore.write-denied",
		"The Function does not own the table, no RolesAssignment makes it a writer, or a Policy forbids the write.",
		"Write from the table's owner, or grant the Function kv::write with a RolesAssignment, such as the KV Data Writer role.")
	ErrValueOverCap = fault.NewKnownCause(fault.PayloadTooLarge, "kvstore.value-over-cap",
		"The value is larger than the store's spec.quota.maxValueBytes.",
		"Write a smaller value, or raise the store's spec.quota.maxValueBytes.")
	ErrKeyOverCap = fault.NewKnownCause(fault.PayloadTooLarge, "kvstore.key-over-cap",
		"The key is longer than the store's spec.quota.maxKeyBytes.",
		"Use a shorter key, or raise the store's spec.quota.maxKeyBytes.")
	ErrKeyOverLimit = fault.NewKnownCause(fault.PayloadTooLarge, "kvstore.key-over-limit",
		"The key is longer than the longest key that any store can hold.",
		"Use a shorter key.")
	ErrNoConditional = fault.NewKnownCause(fault.Unavailable, "kvstore.no-conditional-driver",
		"The KV driver that funcd runs with offers no versioned reads or conditional writes.",
		"Run funcd with a KV driver that offers them, as its memory and Badger drivers do.")
)
