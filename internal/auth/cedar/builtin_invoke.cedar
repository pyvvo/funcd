// Always-on link-as-grant rule the driver ships (ADR-0075) — NOT a user Policy.
//
// A declared link grants invoke by default: a caller may invoke a target it DECLARED in spec.links
// (materialized as the principal's `links` Set of target Function entities). This preserves ADR-0064's
// link-as-grant as defense-in-depth — the PDP self-enforces "declared" without trusting the naming
// Resolver. Operator Policy `forbid`s layer governance (revoke a declared link without editing the
// caller's spec). The `principal has links` guard makes a function with no links inert here.
@id("link-invoke-binding")
permit(principal, action == Action::"link::invoke", resource)
  when { principal has links && principal.links.contains(resource) };
