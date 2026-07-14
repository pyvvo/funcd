// Always-on KV binding-as-grant + single-writer rules the driver ships (ADR-0074/0076/0136) — NOT user
// Policies. Read and write live in ONE file, mirroring builtin_s3.cedar: spec.kv is the read grant; the
// table's `writers` set is the single writer.
//
// read: a declared spec.kv binding grants kv::read by default — a function may read a table it BOUND
// (materialized as the principal's `kvBindings` Set of KVTable entity-refs), the read-side mirror of
// builtin_invoke.cedar's link-as-grant. NOT default-allow: reads on tables NOT in `kvBindings` have no
// permitting built-in, so they stay default-deny (a user Policy must permit a cross-binding read). The
// `principal has kvBindings` guard makes a function with no spec.kv inert here. A binding grants READ
// only, never write.
permit(principal, action == Action::"kv::read", resource)
  when { principal has kvBindings && principal.kvBindings.contains(resource) };

// write: single-writer, generalized (ADR-0136). The base permit lets any principal write; the forbid
// overrides it unless the principal is in the table's `writers` set — the legacy owner (as one entry,
// back-compat) PLUS any writer-role RolesAssignment grant (so an external Identity can be a writer). An
// owner-less, writer-less table has no `writers` attr ⇒ the forbid always fires ⇒ read-only (default-deny).
permit(principal, action == Action::"kv::write", resource);

forbid(principal, action == Action::"kv::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
