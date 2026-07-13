// DEV-ONLY variant of builtin_s3.cedar — used solely by `funcdctl dev`. A dev-fidelity relaxation in the
// spirit of ADR-0125's boundaries (no egress isolation, no sandbox): here, the blob SINGLE-WRITER
// ownership invariant is NOT enforced locally. Two dev realities motivate it: (1) a workflow's input drop
// prefix has no in-platform owner, so seeding it (`aws s3 cp` into `landing`) must be allowed; and (2)
// `funcdctl dev` auto-provisions prefix owners from bindings alone, which cannot infer producer/consumer
// intent — so the prod single-writer forbid would wrongly block a producer's own write. Reads STAY
// binding-gated (unchanged); only the write forbid is dropped. NEVER used in production, where the
// single-writer model (write == prefix owner) is the medallion-layer consistency invariant.
//
// read: unchanged — a declared spec.blob binding grants s3::read on the bound (bucket, prefix).
permit(principal, action == Action::"s3::read", resource)
  when { principal has blobBindings && principal.blobBindings.contains(resource) };

// write (DEV): any authenticated principal may write any prefix — no owner gate. There is NO forbid here
// (prod's builtin_s3.cedar has the single-writer forbid); this is the whole relaxation.
permit(principal, action == Action::"s3::write", resource);
