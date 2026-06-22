# ADR-0073 implementation review — KV bindings + sub-domains (supersedes ADR-0072)

- **ADR**: [0073](../adr/0073-kv-bindings-and-subdomains.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-23

## Verification (evidence — independently re-run, not trusted)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | OK |
| `go test ./...` | **48 ok, 0 fail** (facade/admission/local/types `-race` clean) |
| `go tool golangci-lint run` (touched pkgs) | **0 issues** (no dead grant helpers) |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff → no new deps** |
| OpenAPI regenerated | `FunctionKV`/`KVTable` present (`api/openapi/funcd.v1alpha1.yaml`); `GrantSpec` empty; staleness test passes |
| grant code removed | `internal/services/kv/binder.go` **deleted**; `GrantSpec struct{}`; no `Binder`/`KVMode`/grant-admission refs remain |

## Scenarios → tests (named, passing)

`TestScenarioKVBindingResolves` · `TestScenarioUnboundAccessDenied` (+ resolver + local + e2e) ·
`TestScenarioOwnerWritesOthersRead` (+ `TestScenarioKVNonOwnerReadsButCannotWrite`) · `TestScenarioSingleWriterPerTable` ·
`TestScenarioBindingValidity` (+ `TestFunctionKVValidateMatrix`) · `TestScenarioValueOverCapRejected` ·
`TestScenarioKVStoreDeletionProtection` + `TestScenarioDeleteReclaims` · `TestScenarioTableRemovalProtected`
(admission) + `TestScenarioTableRemovalReclaimed` (reconciler) · `TestScenarioStoreCountQuota` ·
`TestScenarioKVStoreCreateProvisions`. The **full path** `TestScenarioE2EKVCounterViaContextKV` proves an
**unbound** `context.kv` call is **Forbidden** ("function counter has no KV binding for alias counters"), then with
`spec.kv` + the owned table the counter serves **1 → 2**.

## ✅ Verified correct (keep)

- **Default-deny + owner-only writes are real** (read [kv.go](../../internal/services/kv/kv.go),
  [resolver.go](../../internal/services/kv/resolver.go)): the `BindingResolver` returns `fault.Forbidden` when the
  caller has no `spec.kv` entry; `resolveWrite` returns `Forbidden` unless `b.Owner == fn` (single-writer per table);
  per-op caps → `Invalid`; prefix `<ns>/<store>/<table>/<key>`. Not stubs.
- **Grant is cleanly gone** — `GrantSpec` reverted to `struct{}` (KindGrant stays a reserved placeholder), the grant
  Binder + grant admissions removed, the old grant tests rewritten (no dead code). The supersede is honest: ADR-0072
  carries only its `Superseded by ADR-0073` back-link.
- **Reuses the `spec.links` machinery** — `FunctionKV` ≅ `FunctionLink`; the admissions clone `linkValidity` /
  `linkDeletionProtection` (now scanning `spec.kv`). Same-namespace bindings make deletion-protection a clean
  single-namespace scan — structurally avoiding ADR-0072's cross-ns Blocker.
- **Sub-domains land with per-table owner** — `KVStore.spec.tables[]` + the table-removal lifecycle
  (deletion-protection on Update + reconciler `DropPrefix(<store>/<table>/)`) the judge required.
- **Examples are the wrangler shape with distinct names** (decider-set): `counters → counters-kv → table-counters`
  (js), `pycounters → py-counters → table-counters` (py) — alias ≠ store ≠ table, making the three concepts legible.
- **Hygiene** — no new dep; no `any` in port sigs; `api/fault` kinds coherent (Forbidden/Invalid/Conflict); no
  identity/path leak across all touched files.

## Findings
None (Blocker/Major/Minor). One ADR ambiguity the implementer surfaced + resolved: the owner-exists ⟷
binding-validity apply-order cycle (owner needs the function; binding needs the store) — broken by apply
function-without-`spec.kv` → store → function-with-`spec.kv`, encoded in the e2e and the lima script. Attribution:
`adr` (a real ordering note worth carrying), closed in-impl — not a `model` defect.

## DoD
ADR Review-checklist: **7/7** — Grant reverted; `spec.kv`+`tables[]`+OpenAPI; binding-gated facade (default-deny,
owner-only writes, caps, prefix); structural Validate (unique tables/aliases); admissions (validity, owner-exists,
deletion-protection Delete+Update, quota); one gateway + reconciler Ready/counts/DropPrefix(store + table); examples
+ e2e (unbound-denied + owner 1→2); supersede + reuse + no new dep + no leak.

## Recommendation
**pass** — `Reviewing → Implemented`. The Grant misstep is corrected; KV bindings are the wrangler/`spec.links`
convention with per-table single-writer sub-domains, and fine-grained authz is cleanly teed up for the Cedar IAM ADR.
