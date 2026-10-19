// Always-on SQL binding-as-grant + single-writer rules (ADR-0233), the KV model: spec.bindings.sql is the read grant, the
// database's `writers` set (its owner plus writer-role grants) the only writers. An execute asks both actions, since a
// RETURNING clause reads.
@id("sql-read-binding")
permit(principal, action == Action::"sql::read", resource)
  when { principal has sqlBindings && principal.sqlBindings.contains(resource) };

@id("sql-write-base")
permit(principal, action == Action::"sql::write", resource);

@id("sql-single-writer")
forbid(principal, action == Action::"sql::write", resource)
  unless { resource has writers && resource.writers.contains(principal) };
