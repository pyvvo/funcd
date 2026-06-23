// Always-on binding-as-read-grant rule the driver ships (ADR-0076) — NOT a user Policy.
//
// A declared spec.kv binding grants kv::read by default: a function may read a table it BOUND
// (materialized as the principal's `kvBindings` Set of KVTable entity-refs). This is the read-side
// mirror of builtin_invoke.cedar's link-as-grant — the binding is the capability. It is NOT a return
// to default-allow: reads on tables NOT in `kvBindings` have no permitting built-in, so they stay
// default-deny (a user Policy must permit a cross-binding read). The `principal has kvBindings` guard
// makes a function with no spec.kv inert here. Operator Policy `forbid`s revoke a binding's read; the
// owner-write rule (builtin_kv.cedar) is untouched — a binding grants READ only, never write.
permit(principal, action == Action::"kv::read", resource)
  when { principal has kvBindings && principal.kvBindings.contains(resource) };
