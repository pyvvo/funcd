# ADR-0072 implementation review — KV as a declarative resource (KVStore + Grant + single-writer)

- **ADR**: [0072](../adr/0072-kv-as-a-declarative-resource.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-22

## Verification (evidence — independently re-run, not trusted)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | OK |
| `go test ./...` | **48 packages ok, 0 fail** (facade/admission/local `-race` clean) |
| `go tool golangci-lint run` (touched pkgs) | **0 issues** |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff → no new deps** |
| OpenAPI regenerated | `api/openapi/funcd.v1alpha1.yaml` carries `KVStore`/`KVStoreSpec`/`maxValueBytes` (21 refs); spec-generation tests pass |

## Scenarios → tests (named, passing)

`TestScenarioKVStoreCreateProvisions` · `TestScenarioGrantBindsFunctionToStore` · `TestScenarioUngrantedAccessDenied`
(+ `TestScenarioKVUngrantedDenied`, local) · `TestScenarioSingleWriterEnforcedAdmission` · `TestScenarioReaderGrantAllowsGetNotPut`
(+ local) · `TestScenarioValueOverCapRejected` · `TestScenarioStoreCountQuota` · `TestScenarioKVStoreDeletionProtection`
(grants **and** non-empty data) · `TestScenarioDeleteReclaims` · and the **full path** `TestScenarioE2EKVCounterViaContextKV`
(pkg/funcd) — an **ungranted** `context.kv` call is **Forbidden** (logged "has no Grant for binding"), then after the
`rw` Grant is applied the counter serves **1 → 2**. Supporting: types roundtrip/Validate, Binder resolve/dangling,
store-scoped-prefix isolation, grant-validity references.

## ✅ Verified correct (keep)

- **Default-deny is real** (read [kv.go](../../internal/services/kv/kv.go)): the facade requires a `Binder`,
  `Resolve` returns `fault.Forbidden` on no Grant, `resolveWrite` returns `Forbidden` unless `Mode==rw`, and per-op
  caps return `fault.Invalid` before the write reaches the driver — not stubs. The **caller function is threaded**
  through the local-API KV port + `registerKV` to the Binder (the prior `sandboxIdentity` collapse is gone), exactly
  the Major the judge flagged.
- **Grant gate replaces the per-call `KindService` PDP check** — `Authorizer` is removed from `FacadeDeps`;
  control-plane CRUD of `KVStore`/`Grant` stays PDP-gated at the server. Matches the ADR's pinned decision.
- **Three admissions** clone the links.go shape (single-writer ≤1 `rw` → `Conflict`; store-count quota → `Invalid`;
  deletion-protection on referencing Grants **and** non-empty data via a locally-defined `KVProber` — the package
  stays a near-leaf, no kvstore import).
- **Reconciler** sets Ready + `grantRefs`, and Delete → `DropPrefix(<ns>/<name>/)` via a locally-defined
  `PrefixDropper` type-asserted from the driver (memory driver gained `DropPrefix`).
- **Same-namespace scope honoured** — `GrantSpec` has no `storeNamespace`; the cross-ns Blocker the judge raised is
  designed out, as accepted.
- **Examples migrated** — both kv-counter examples carry a `KVStore` + an `rw` `Grant`; the e2e proves the gate.
- **Hygiene** — no new dep; no `any` in port signatures; `api/fault` kinds coherent (Forbidden/Invalid/Conflict);
  no identity/path leak across all 36 changed/created files.

## Findings
None (Blocker/Major/Minor). One self-resolved ADR gap surfaced + handled by the implementer: the control-plane CRUD
plumbing (REST routes/handlers/`stampTypeMeta`) was implied but not enumerated in the ADR — added by cloning the
Grant CRUD path; caught by the e2e (empty TypeMeta) and fixed. Attribution: `adr` (under-specified plan), already
closed in-impl — not a `model` defect.

## DoD
ADR Review-checklist: **7/7** — types + GrantSpec + KindKVStore + OpenAPI; grant-required facade (default-deny,
ro-cannot-write, function threaded, store prefix, PDP replaced); per-op caps; the three admissions; reconciler
Ready+grantRefs + DropPrefix; examples + e2e (grant-gated + ungranted denial); reuse + no new dep + no leak.

## Recommendation
**pass** — `Reviewing → Implemented`. The KV control-plane foundation is in; the typed-record engine (backlog) can
build on the single-writer ownership lock this establishes.
