// Always-on binding-as-grant rules the driver ships for the S3 frontend (ADR-0080) — NOT user
// Policies. Mirrors builtin_kv_read.cedar + builtin_kv.cedar token-for-token: spec.blob is the read
// grant, the Bucket prefix's owner is the single writer.
//
// read: a declared spec.blob binding grants s3::read by default — a function may read a (bucket,
// prefix) it BOUND (materialized as the principal's `blobBindings` Set of BlobPrefix entity-refs).
// The `principal has blobBindings` guard makes a function with no spec.blob inert here; reads on
// prefixes NOT in `blobBindings` have no permitting built-in, so they stay default-deny. Operator
// Policy `forbid`s revoke a binding's read; the binding grants READ only, never write.
permit(principal, action == Action::"s3::read", resource)
  when { principal has blobBindings && principal.blobBindings.contains(resource) };

// write: single-writer. The base permit lets any principal write, and the forbid overrides it unless
// the principal IS the prefix's owner. Reads have NO write built-in. The `resource has owner` guard
// keeps an owner-less prefix read-only (the forbid still fires when there is no owner to match).
// WITHOUT the base permit even the owner could not write.
permit(principal, action == Action::"s3::write", resource);

forbid(principal, action == Action::"s3::write", resource)
  unless { resource has owner && principal == resource.owner };
