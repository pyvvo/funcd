// Always-on Secret binding-as-grant rule the driver ships (ADR-0321) — NOT a user Policy. spec.bindings.secret is the
// read grant: a Function or a Lake reads a Secret it BOUND (its `secretBindings` Set of Secret entity-refs). A Secret it
// does not bind has no permitting built-in, so it stays default-deny; a namespace Policy forbid revokes a bound one. The
// `principal has secretBindings` guard makes a principal with no spec.bindings.secret inert here.
@id("secret-read-binding")
permit(principal, action == Action::"secret::read", resource)
  when { principal has secretBindings && principal.secretBindings.contains(resource) };
