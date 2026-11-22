// Always-on binding-as-grant rule for a call of an add-on object's engine through funcd (ADR-0338 Decision 5): a
// declared spec.bindings.addon binding grants addon::call on that object, materialized as the principal's
// `addonBindings` Set of AddonObject entity-refs. A call on an object not in it has no permitting built-in, so it
// stays default-deny; an operator Policy forbid revokes a binding's calls.
@id("addon-call-binding")
permit(principal, action == Action::"addon::call", resource)
  when { principal has addonBindings && principal.addonBindings.contains(resource) };
