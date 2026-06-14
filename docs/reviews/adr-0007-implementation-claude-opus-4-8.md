# Review report — ADR-0007 implementation (blob / storage-layer port)

- **ADR**: [ADR-0007 — Blob / storage-layer port](../adr/0007-blob-storage-layer-port.md)
- **Phase**: implementation (ADR-0000 review gate #5)
- **Implemented by**: `claude-opus-4-8`
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` discipline (`claude-opus-4-8`) — ⚠️ **self-review** (decider delegated all
  gates this run); findings are tied to captured evidence to stay honest.
- **Realizes**: [FEAT-0000/F21](../feat/0000-feat-v1.md) (blob)

## Verdict: pass — 0 Blockers, 0 Majors, 1 Minor (0 model-attributed)

The implementation conforms to ADR-0007's Contracts, Scenarios, Review checklist, and DoD; `just ci`
exits 0 (pure-Go, no cgo); all 6 scenarios pass; conventions hold.

## Verification (captured)
- `just ci` → **exit 0** (specgen stable, `go fmt` clean, golangci-lint **0 issues**, `go test ./...` ok,
  build, `go mod verify` → all modules verified, tidy-gate clean).
- Scenario→test traceability (6): `blob-roundtrip`, `not-found`, `delete-removes`, `list-by-prefix`,
  `signed-url-unsupported-locally` → `blobcontract.RunContract` subtests; run against **both memory and
  file** backends via `TestScenario_DriverConformanceParity` (= `driver-conformance-parity`).
- Conventions: `any`/`interface{}` in the port = **0**; the port (`blob.go`) imports **no driver lib**
  (`gocloud` appears only in comments); `panic`/`fmt.Print` in non-test code = **0**; ctx-first; `api/fault`.
- Tree vs ADR surface: `blob.go`, `gocloud/gocloud.go`, `blobcontract/contract.go`, `gocloud_test.go` —
  exactly the ADR's repository surface.
- Hygiene: ADR at `Reviewing`; feat F21 `blob: reviewing`; ADR substance unchanged (status bump only);
  identity grep clean.

## ✅ Verified correct (keep)
- **Driver-dep-free port**: `blob.go` imports only `context`+`time`; go-cloud lives solely in `gocloud/` —
  exactly the ADR's C2/§1, keeps gocloud swappable.
- **`gcerrors.Code` → `api/fault` mapping** (NotFound, Unimplemented→Unavailable, else Internal); `Exists`
  returns `(false,nil)` for a missing key — proven by `not-found`/`delete-removes`.
- **`memblob` is the in-memory driver** (no hand-written one) — the substrate rule, cgo-free, fast.
- **Typed `SignMethod`** (the judge's m2) and the §3 mapping note both landed.
- **`signed-url-unsupported-locally`** asserts `fault.Unavailable` on mem/file — the backend-capability gap
  is observable and tested.

## Findings
### Blockers / Major
None.
### Minor
- **M1 (env/adr) — gocloud's `s3blob` pulls a heavy AWS+GCS transitive tree** (a `cloud.google.com/go/compute`
  upgrade was needed to resolve an ambiguous import). S3 round-trip stays unit-untested (the ADR scopes it to
  an integration lane). This is the ADR's acknowledged binary-size/scope consequence, not a code defect —
  the direct dep added is `gocloud.dev/blob` (Apache-2.0). Direction: the P-S integration lane exercises the
  s3 happy-path; consider build-tagging `s3blob` later if binary size matters.

## Definition of Done
ADR Review-checklist **7/7** hold (port + types, no driver import; one gocloud driver spanning mem/file/s3,
memblob in-memory; error mapping + Exists; RunContract on memory+file, list sorted, SignedURL→Unavailable;
api/fault + ctx-first + no-any + no-globals; only gocloud.dev/blob added + tidy + no cgo; every scenario
named/passing + no leak + driver-dep-free).

## Recommendation
**Pass.** Advance ADR-0007 `Reviewing → Implemented`; feat F21 `blob: implemented`. Next: graduate P-D →
ADR-0007 (items → accepted, tier 0) in the roadmap.
