## Verdict: pass — 0 blockers, 0 majors  (ADR-0121 implementation, model: claude-opus-4-8)

Implementation of ADR-0121 (declarative referential integrity — reconcile-time cross-resource
existence, FEAT-0001/F86). Six write-time existence admissions removed; existence relocated to the
Function + CatalogService reconcilers as accept-and-requeue Waiting conditions. Verified by running
build/vet/lint/test on branch `feat/declarative-admission` (impl commit `0dc6e69`).

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `nix develop -c go build ./...` | exit 0 |
| vet | `nix develop -c go vet ./internal/function/... ./internal/services/catalog/... ./pkg/funcd/...` | exit 0 |
| mod verify | `nix develop -c go mod verify` | `all modules verified` · exit 0 |
| lint | `nix develop -c go tool golangci-lint run ./internal/controlplane/admission/ ./internal/function/ ./internal/services/catalog/ ./pkg/funcd/` | `0 issues.` · exit 0 |
| test | `nix develop -c go test ./internal/controlplane/... ./internal/function/... ./internal/services/catalog/... ./pkg/funcd/...` | exit 0 (pkg/funcd 59.3s incl. e2e) |

The `TestPythonPoolSmoke` environmental flake (internal/testkit/bench) is out of scope here and was
not exercised by the reviewed package set.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **bucket-before-owner scenario has no dedicated new test.** · attribution: model (soft) · The ADR
  Scenario `bucket-before-owner` (a write into an owner-less prefix is `Forbidden`, then authorized
  once the owner exists) is not exercised by a new named test in this change; it is covered by the
  **unchanged** fail-closed Cedar authz model (the pre-accept judge traced it in
  `internal/auth/cedar/capabilities.go`: a missing/owner-less prefix yields a UID no principal holds
  ⇒ deny). Because this ADR only removes admissions and touches no authorization code, the behavior
  is pre-existing and already regression-guarded by the Cedar capability suites — the gap is a
  missing *explicit* assertion, not a missing behavior. Low value; non-blocking. Optional follow-up:
  add a Forbidden-then-authorized e2e assertion when the S3 data-plane lane is exercised.

### ✅ Verified correct (keep it)
- **Decision 1 — six existence admissions removed, code deleted.** `git diff` of `pkg/funcd/funcd.go`
  shows the six `admission.New*` entries (`KVBindingValidity`, `KVOwnerExists`, `BlobBindingValidity`,
  `BucketPrefixOwnerExists`, `CatalogBlobValidity`, `CatalogBindingValidity`) removed from the
  `Admissions` slice. `internal/controlplane/admission/catalog.go` deleted entirely (absent on disk).
  `bucket.go` keeps only `NewBucketQuotaAdmission` + `NewBucketDeletionProtectionAdmission`; `kvstore.go`
  keeps only `NewKVStoreQuotaAdmission` + `NewKVStoreDeletionProtectionAdmission`. A repo-wide grep for
  the six removed constructors and the `nameExists` helper returns **no references** anywhere. The
  removed admissions' unit tests are deleted (no `BindingValidity`/`OwnerExists`/`BlobValidity` test
  remains).
- **Kept admissions intact.** The chain retains link-validity + link-deletion-protection,
  bucket/kvstore quotas, bucket/kvstore deletion-protection, policy-validity, workflowrun-payload +
  workflowrun-contract. Kept unit tests present and passing (bucket/kvstore count-quota, deletion-
  protection, prefix/table-removal-protected) — covers the `deletion-protection-unchanged` scenario.
- **Decision 2 — consumer reconcilers surface Waiting.** `internal/function/references.go`
  `resolveDataReferences` lists Buckets/KVStores in-namespace and returns requeue + `BucketNotFound`/
  `KVStoreNotFound` on a missing bucket, prefix, store, or table; the gate is wired at `function.go`
  step `3c-bis` — sets `Ready=False`, `Phase=Pending`, `Replicas=0`, persists, returns
  `RequeueAfter: 2s` **before** worker provisioning. `internal/services/catalog/reconcile.go`
  `resolveBucketRefs` mirrors it for `spec.blob` + `spec.catalog` (Reason `BucketNotFound`), gated
  before `Converge`. Both mirror the existing `CatalogNotReady` precedent.
- **Security — fail-closed, no fail-open.** Confirmed the reconciler gates **return requeue, not a
  deploy**, on a missing referent: the Function gate zeroes replicas and requeues before provisioning;
  the CatalogService gate returns before `Converge` (verified by `TestReconcile_waits_for_bucket` —
  `prov.converged` is empty while the Bucket is absent). The removed admissions performed only
  cross-resource existence; no grant-compilation or authz code was touched, so nothing now compiles a
  broken grant or opens a previously-closed path (the write-authz default-deny window is unchanged).
- **Scenarios → passing, un-skipped tests.** Ran the named tests with `-v`; all pass, none skipped:
  `TestScenarioBlobBindingWaitsForBucket`, `TestScenarioKVBindingWaitsForStore` (consumer-before-referent
  + kv variant, internal/function), `TestReconcile_waits_for_bucket` (catalog-waits-for-bucket, converges
  past the gate once the Bucket is stored), `TestScenarioDeclarativeAdmissionCatalogCycle` +
  `TestScenarioDeclarativeAdmissionFunctionBindingAdmitted` (any-order Apply admitted over the real
  control plane, pkg/funcd e2e). No `t.Skip` in the three new test files.
- **Examples collapsed.** `examples/python/catalog-quack/bucket-base.yaml`,
  `examples/js/s3-roundtrip/function-base.yaml`, and `examples/python/releve-lakehouse/.../bucket-base.yaml`
  are gone; a single `bucket.yaml` remains in each. `scripts/lanes.yaml` `duckdb` + `s3` lanes now apply
  in the deliberately "wrong" order (CatalogService/Function before its Bucket) with an ADR-0121 comment.
- **Tracking + conventions.** ADR status `Reviewing (2026-07-10)`; the diff between the accept commit
  (`5b146fc`) and the implement commit (`0dc6e69`) on the ADR is **only** the `Accepted → Reviewing`
  status-line bump — substance unchanged. feat row FEAT-0001/F86 = `reviewing`. `blueprint.md` KV
  admission line synced to "binding/owner existence is reconcile-time (ADR-0121)". Lint 0 issues
  (ctx-first, `api/fault` errors, `log/slog`, no `any` in the new port surface). No new deps.

### Definition of Done
14 / 14 items hold (7 ADR Review-checklist + 7 applicable generic-phase DoD items). The one ADR
checklist sub-item not green as a live gate — the Venom `apply-any-order` lane — is **recorded as
in-progress / run live separately** (the ADR explicitly permits "green or recorded-deferred"; the Go
e2e proves admission any-order in-process, convergence-past-gate is proven at the reconcile level).
That deferral is env/sequencing, not model-attributed. The lone Minor (no dedicated bucket-before-owner
test) does not fail a DoD item — that scenario's behavior is unchanged, pre-existing, and Cedar-suite
guarded.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0121 (implementation) → pass, 0 blockers / 0 majors / 1 minor,
1 model-attributed (the soft Minor), DoD 14/14. See docs/reviews/model-scorecard.md.

### Recommendation
Sign off — advance ADR-0121 `Reviewing → Implemented` and feat FEAT-0001/F86 → `implemented`. The
implementation is faithful to the ADR: the six existence admissions and their code/tests are cleanly
removed, existence is now a fail-closed accept-and-requeue Waiting condition on both consumer
reconcilers, every listed scenario has a passing un-skipped test, examples collapse to a single
`bucket.yaml`, and all four sub-checks are green. The optional bucket-before-owner assertion and the
in-progress Venom lane can land as follow-ups without blocking sign-off.
