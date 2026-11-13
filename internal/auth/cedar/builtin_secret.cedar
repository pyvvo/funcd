// Always-on Secret binding-as-grant rule the driver ships (ADR-0321) — NOT a user Policy. spec.bindings.secret is the
// read grant: a Function or a Lake reads a Secret it BOUND (its `secretBindings` Set of Secret entity-refs). A Secret it
// does not bind has no permitting built-in, so it stays default-deny; a namespace Policy forbid revokes a bound one. The
// `principal has secretBindings` guard makes a principal with no spec.bindings.secret inert here.
@id("secret-read-binding")
permit(principal, action == Action::"secret::read", resource)
  when { principal has secretBindings && principal.secretBindings.contains(resource) };

// write: single-writer, the KV and SQL pair (ADR-0327). The base permit lets any principal write; the forbid overrides
// it unless the principal is in the Secret's `writers` set, its owner plus writer-role grants. A Secret with neither, or
// a missing one, has no `writers` attr, so the forbid always fires.
@id("secret-write-base")
permit(principal, action == Action::"secret::write", resource);

@id("secret-single-writer")
forbid(principal, action == Action::"secret::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
