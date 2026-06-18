# ADR-0021 Implementation Review — Blob service (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0021 implementation, model: claude-opus-4-8)

The `internal/services/blob` Facade over `blob.Bucket` + the blob `TypeHandler` realize the Contracts. The
judge's Major is **resolved**: `SignedURL` authorizes the granted capability (`verbForSign(opts.Method)` —
`SignPut`→`VerbUpdate`), so a viewer can't mint a presigned write URL. All 5 scenarios pass; it's a `TypeHandler`
on the ADR-0019 dispatcher (no new reconciler). No findings.

**Reviewed against**: ADR-0021 Contracts/Scenarios/Review-checklist/DoD · blueprint "Services / Blob storage /
Internal IAM" · ADR-0019 (pattern), ADR-0007 (blob port), ADR-0018 (PDP), ADR-0002 · FEAT-0000/F23.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet` | exit 0 |
| `go test -count=1 ./...` | PASS (full suite, incl. OpenAPI staleness — regen committed-consistent) |
| scenarios | 5/5 PASS (`blob-facade-roundtrips`, `…-prefixes-by-namespace-and-binding`, `…-authorizes-each-access`, `…-presigns`, `blob-service-reconciles-to-ready`) |
| `golangci-lint run ./...` | **0 issues** |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (no new dep — blob port + gocloud already present) |
| M1 fix | `blob.go:110,117-120`: `SignedURL` → `authorize(verbForSign(opts.Method))`; `SignPut`→`VerbUpdate`, `SignDelete`→`VerbDelete`, else `VerbGet` |
| reuse | blob has no `controller.Reconciler`/`Register` — it's a `services.TypeHandler` registered on the ADR-0019 dispatcher (test: `NewDispatcher(st, nil, blob.NewHandler())`) |
| conventions | no `any`; identity clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **M1 resolved — no presign privilege-escalation** (`blob-facade-presigns`): `SignedURL` authorizes by the
  requested method (`verbForSign`), so a `viewer` is `fault.Forbidden` for a presigned **PUT** (write
  capability) while an authorized dev's GET presign passes the facade. **Keep `verbForSign` — never authorize
  presign as a blanket read.**
- **Correct pattern reuse**: a `TypeHandler` (`Type()==ServiceTypeBlob`) on the **same** ADR-0019 dispatcher
  (`blob-service-reconciles-to-ready` → Ready), **not** a new `Service` reconciler — the one-per-gvk rule holds,
  proving the pattern generalizes (P-P next).
- **Faithful PEP + isolation**: authz before the bucket (`blob-facade-authorizes-each-access`: cross-namespace
  dev + write-viewer both 403), `<ns>/<binding>/<key>` prefix + List-strip (`…-prefixes…` → `["report.txt"]`,
  two namespaces independent). `Get` returns `([]byte, error)` matching the blob port (a miss is an error), and
  `List` flattens `[]Attributes`→stripped keys. `ServiceTypeBlob`/`BlobServiceSpec` added (OpenAPI regenerated);
  `New(Deps)` guards, ctx-first, `api/fault`, `slog`, no globals, no `any`, **no new dep**.

## Definition of Done
ADR Review-checklist: **4/4** hold (facade authz-before-bucket+prefix+strip+roundtrip+presign · TypeHandler→Ready
on the dispatcher · ServiceTypeBlob/BlobServiceSpec+OpenAPI · conventions+no-dep+no-leak). Scenarios: 5/5 named,
un-skipped, passing. ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0021 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 4/4.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0021 `Reviewing → Implemented`, feat F23 → `implemented`. The Step-6 reconcile records P-O's
real edges (ADR-0007/0019/0018/0003/0006). P-P (secrets) follows the same `TypeHandler`+facade path.
