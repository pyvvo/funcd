# ADR-0024 Implementation Review — `funcdcli` + Go SDK (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0024 implementation, model: claude-opus-4-8)

The typed `pkg/sdk` client + the stdlib `funcdcli` realize the Contracts. The kind-parameterized CRUD
(`Apply`/`Get`/`List`/`Delete` over `v1.Kind`+`v1.Object`, no `any`, no generics) round-trips the **real**
control plane (mounted on `httptest` with authn + RBAC — no mocks): apply→get preserves the object, list
returns all, delete→get is `fault.NotFound`, and a missing-object error maps `problem+json`→`fault.Kind`.
The CLI applies/gets/deletes and pre-flights invalid manifests **offline**. All judge findings (B1 contract
precision, M1 status-keyed error mapping, M2 PUT-then-POST `Apply`, M3 drift guard) are present and proven.
Zero new dependency; SDK/CLI import only `api/**` + stdlib (tests mount `internal/controlplane`).

**Reviewed against**: ADR-0024 Contracts/Scenarios/Review-checklist/DoD · blueprint Layout (`pkg/sdk`,
`cmd/funcdcli` "depends on pkg/sdk only", `api/openapi/generated/` reserved) + §"Shape enforcement" ·
ADR-0005/0018 (the API), ADR-0003 (`Validate`/`NewObject`), ADR-0002 · FEAT-0000/F18.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` | exit 0 |
| `go test ./...` | PASS (full suite, incl. OpenAPI staleness) |
| `go test -race ./pkg/sdk/... ./cmd/funcdcli/...` | PASS |
| scenarios | 7/7 PASS (`sdk-applies-and-gets`, `sdk-lists`, `sdk-deletes`, `sdk-maps-error-to-fault`, `cli-apply-then-get`, `cli-delete`, `cli-validates-before-apply`) + `kinddescriptor-covers-all-kinds` (M3 guard) + `KindFromToken` — each a named, un-skipped test |
| `golangci-lint run ./...` | **0 issues** |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (stdlib `net/http`/`encoding/json`/`flag`, **no new dep**) |
| import discipline | `pkg/sdk` + `cmd/funcdcli` import only `api/types`, `api/fault`, `pkg/sdk`, stdlib; tests mount `internal/controlplane` (depguard-allowed) |
| conventions | no `any` (`v1.Object` interface + `v1.NewObject`); `New(...Option)`; ctx-first; `api/fault`; checked writes (`writef`); identity clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **The wire-shape problem is solved correctly and empirically** (`pkg/sdk/sdk.go` `toWireBody`): the SDK
  lifts `apiVersion`/`kind` into a **nested** `TypeMeta` (huma's request schema is `additionalProperties:false`
  + `required:[TypeMeta,…]` and ignores `,inline`) **and drops `status`** (whose embedded `Status` has the
  same nesting quirk and is server-owned). This was found by the real-server round-trip — exactly the
  no-mocks strategy the ADR committed to. Keep `toWireBody`'s delete-status; a naive `json.Marshal(obj)` 422s.
- **Kind-parameterized core, no `any`** (`Apply`/`Get`/`List`/`Delete`): 4 methods over `v1.Kind`+`v1.Object`,
  decoding via `v1.NewObject(kind)` (two-value form, B1) — faithful to `handlers.go`. `List` allocates each
  element via `NewObject` (m3) since you can't unmarshal into a `[]v1.Object` slice.
- **`Apply` PUT-then-POST** (M2): PUTs the named path; on `fault.NotFound` POSTs the collection. Proven by
  `sdk-applies-and-gets` covering **create** (first apply) **and replace** (second apply changes the handler).
- **Error mapping keys on the JSON `status`** (M1, `problemToFault`): not on Content-Type — so handler faults
  (`application/json`) and huma's 422/401 (`application/problem+json`) both map to the right `fault.Kind`
  (the exact inverse of `fault`'s `kindProblem`). `sdk-maps-error-to-fault` asserts the *Kind*, not a status.
- **Drift guard** (M3, `kinddescriptor-covers-all-kinds`): every `v1.AllKinds()` has a `kindDescriptor` —
  the hand-table can't silently drift from the server's routes.
- **CLI is a thin, testable shell**: `run(ctx,args,out,c)` is the core; `main` only wires `--server`/env →
  `sdk.New` → `run` → exit. Offline pre-flight via the shared `api/types` `Validate()` (`cli-validates-before-apply`
  covers both missing-`resourceGroup` **and** unknown-`kind` → `fault.Invalid`, no network). No cobra; imports
  `pkg/sdk` only.

## Definition of Done
ADR Review-checklist: **6/6** hold (SDK CRUD round-trip · error→fault by status · CLI verbs over the SDK ·
CLI pre-flight · kind-table no-gaps · import-discipline/no-`any`/no-dep/deferrals/no-leak). Scenarios: 7/7
named, un-skipped, passing. ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0024 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 6/6.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0024 `Reviewing → Implemented`, feat F18 → `implemented`. Real edges ADR-0005 + P-L
(= ADR-0018, on ADR-0003); it builds on none of bus/gateway/runtime. The Step-6 reconcile records this and
graduates 0024. Next: P-S (testing + full e2e).
