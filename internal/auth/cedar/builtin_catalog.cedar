// Always-on binding-as-grant rule the driver ships for the catalog serving layer (ADR-0137) — NOT a
// user Policy. Mirrors the READ half of builtin_s3.cedar token-for-token: spec.catalogs is the
// query grant. There is NO write half — catalog::query is read-shaped (query-only in V1; DDL stays
// the engine's pinned single writer, ADR-0086), so no writers-set forbid.
//
// query: a declared spec.catalogs binding grants catalog::query by default — a function may query a
// CatalogService it BOUND (materialized as the principal's `catalogBindings` Set of CatalogService
// entity-refs). The `principal has catalogBindings` guard makes a function with no spec.catalogs inert
// here; queries on catalogs NOT in `catalogBindings` have no permitting built-in, so they stay
// default-deny. Operator Policy `forbid`s revoke a binding's query. External Identities are granted
// via a Catalog-scoped RolesAssignment (a compiled permit), never this built-in.
@id("catalog-query-binding")
permit(principal, action == Action::"catalog::query", resource)
  when { principal has catalogBindings && principal.catalogBindings.contains(resource) };
