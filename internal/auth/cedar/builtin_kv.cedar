// Always-on KV consistency rules the driver ships (ADR-0074) — NOT user Policies.
//
// kv::write is single-writer: the base permit lets any principal write, and the forbid overrides it
// unless the principal IS the table's owner. Reads have NO built-in permit, so they are DEFAULT-DENY
// until a user Policy grants kv::read. The `resource has owner` guard keeps an owner-less table
// read-only (the forbid still fires when there is no owner to match).
permit(principal, action == Action::"kv::write", resource);

forbid(principal, action == Action::"kv::write", resource)
  unless { resource has owner && principal == resource.owner };
