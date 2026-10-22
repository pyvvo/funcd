// Always-on binding-as-grant rule the driver ships for the lake serving layer (ADR-0137) — NOT a
// user Policy. Mirrors the READ half of builtin_s3.cedar token-for-token: spec.bindings.lake is the
// query grant. There is NO write half — lake::query is read-shaped (query-only in V1; DDL stays
// the engine's pinned single writer, ADR-0086), so no writers-set forbid.
//
// query: a declared spec.bindings.lake binding grants lake::query by default — a function may query a
// Lake it BOUND (materialized as the principal's `lakeBindings` Set of Lake
// entity-refs). The `principal has lakeBindings` guard makes a function with no spec.bindings.lake inert
// here; queries on lakes NOT in `lakeBindings` have no permitting built-in, so they stay
// default-deny. Operator Policy `forbid`s revoke a binding's query. External Identities are granted
// via a Lake-scoped RolesAssignment (a compiled permit), never this built-in.
@id("lake-query-binding")
permit(principal, action == Action::"lake::query", resource)
  when { principal has lakeBindings && principal.lakeBindings.contains(resource) };
