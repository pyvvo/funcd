// Always-on binding-as-grant rules for a Function's start and read of a Job's runs (ADR-0349 Decision 1): a declared
// spec.bindings.job binding grants job::start and job::read on that Job, materialized as the principal's `jobBindings`
// Set of Job entity-refs. A Job that no binding names has no permitting built-in, so it stays default-deny; an
// operator Policy forbid revokes either action.
@id("job-start-binding")
permit(principal, action == Action::"job::start", resource)
  when { principal has jobBindings && principal.jobBindings.contains(resource) };

@id("job-read-binding")
permit(principal, action == Action::"job::read", resource)
  when { principal has jobBindings && principal.jobBindings.contains(resource) };
