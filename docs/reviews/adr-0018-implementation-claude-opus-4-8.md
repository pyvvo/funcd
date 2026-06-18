# ADR-0018 Implementation Review — API server (authn, RBAC, admission, PDP) (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 1 minor (ADR-attributed)  (ADR-0018 implementation, model: claude-opus-4-8)

The `auth.Authorizer` PDP + built-in RBAC + authn middleware + store-backed `Handlers` (over ADR-0005's seam)
realize the Contracts. The judge's Blocker is **resolved**: the `Handlers` use **non-generic `v1.Object`
helpers** (no generics, no `any`) and are forbidigo-clean. All 8 scenarios pass, ADR-0005's 5 existing tests
still pass (seam intact), and default-deny + staged 401→403→400 hold. No model-attributed findings.

**Reviewed against**: ADR-0018 Contracts/Scenarios/Review-checklist/DoD · blueprint "API Server / Security
model / Internal IAM" · ADR-0005 (the seam), ADR-0006 (store), ADR-0003 (kinds), ADR-0002 (conventions,
forbidigo) · FEAT-0000/F07.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Command | Result |
|---|---|---|
| compiles | `go build ./...` | **exit 0** |
| vets | `go vet ./internal/auth/... ./internal/controlplane/...` | **exit 0** |
| tests | `go test -count=1 ./...` | **exit 0** (full suite) |
| scenarios | `go test -v ./internal/auth/... ./internal/controlplane/...` | 8/8 new PASS + ADR-0005's 5 (`TypedOperationRoundtrip`, `ErrorIsProblemJSON`, `OpenAPIDocServed`, `SpecGeneratedFromGo`, `SpecReflectsGoShape`) still PASS |
| lints | `go tool golangci-lint run ./...` | **0 issues** |
| deps | `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (no new dep — built-in RBAC zero-dep) |
| B1 fix | grep `[T any]` / `\bany\b` (minus `...any`) in new code | **none** — non-generic helpers, forbidigo-clean |
| ADR-0005 seam | `git diff` controlplane.go/routes.go/routes_rest.go/stubs.go | **empty** — interface/routes/bridge untouched; the real `Handlers` *implements* the frozen seam |
| import graph | grep `internal/controller`/`internal/runtime` in auth/controlplane | **none** — API writes the store; the controller reconciles it |
| identity | local username / `/Users/` / email | **clean** |

## 🔴 Blockers / 🟡 Major
None.

## Minor
- **m1 — huma's `,inline` `TypeMeta` schema mismatch (attribution: `adr` → ADR-0005, *not* the model).** The
  generated request schema expects a nested `TypeMeta` object (huma doesn't honor `json:",inline"`), and a
  body that passes that schema decodes via stdlib to an **empty** `TypeMeta` — which `store.Create`'s
  `validateMeta` would reject. P-L handles this **correctly** by stamping `TypeMeta` from the route's kind
  (`stampTypeMeta`, k8s-style — the endpoint owns apiVersion/kind), proven by `crud-roundtrips-through-store`.
  The *underlying* quirk lives in ADR-0005's huma setup; a future ADR-0005-successor could make `TypeMeta`
  round-trip cleanly (response-side too). **Does not count against the model** — the stamping is the right fix.

## ✅ Verified correct — keep it
- **B1 resolved cleanly** — `handlers.go` uses six **non-generic** `v1.Object` helpers (`authorize`/`getObj`/
  `listObj`/`createObj`/`replaceObj`/`deleteObj`) + 75 thin typed-assert methods; no generics, no `any`
  (forbidigo green). The pointer/value bridge the judge flagged is handled by `*o.(*v1.Function)` on the way
  out — exactly the recommended shape.
- **One PDP, default-deny, in its own package** — `auth.Authorizer` + typed `Identity`/`Verb`/`Role`/`Request`/
  `Decision`; `rbac` is one file in its own subpackage and **classifies scope via `v1.Kind.Namespaced()`**
  (the judge's m2 — no hardcoded list, can't drift). `authcontract.Run` runs against the driver
  (`authorizer-contract-holds`); unknown role / unscoped principal → deny.
- **Staged 401→403→400, no write on reject** — authn middleware rejects missing/unknown creds **401**
  (`unauthenticated-rejected`), constant-time compare, spec/docs public; `authorize` precedes admission
  precedes `store.*` in every helper, so a denied (403) or invalid (400) request never persists.
  `rbac-viewer-is-read-only`, `rbac-denies-out-of-namespace`, `admin-spans-namespaces-and-cluster-kinds`,
  `admission-rejects-invalid` all hold.
- **Real store-backed CRUD over the frozen seam** — `crud-roundtrips-through-store` creates→gets→lists→deletes
  with real persistence (uid stamped, 404 after delete); the full 75-method interface is implemented
  (compile-enforced via `NewStoreHandlers() Handlers`); store `fault` kinds (404/409) pass through ADR-0005's
  bridge; `ReplaceX` is read-RV-then-update. ADR-0005's files are **untouched**.
- Conventions: `NewServer(Deps)` deps-struct + required-dep guards, ctx-first, `api/fault`, `slog` (logger
  nil-guarded), no globals, no `any`, **no new dependency**; the rule-#3 PDP/API bundling is kept cleanly
  separable (`internal/auth` is its own package + suite).

## Definition of Done
ADR Review-checklist: **6/6** hold (PDP+RBAC+contract+default-deny · authn 401+constant-time+public-paths ·
RBAC viewer/cross-ns/admin · admission 400+no-persist · store-backed 75-method CRUD+seam-passthrough ·
seam-unchanged+conventions+no-dep+no-leak). Scenarios: 8/8 named, un-skipped, passing. ADR substance
unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0018 (implementation) → pass, 0/0/1, **0 model-attributed** (the 1 minor is
`adr`-attributed to ADR-0005), DoD 6/6. See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0018 `Reviewing → Implemented`, feat F07 → `implemented`. The Step-6 roadmap reconcile
should set P-L's real build edges (ADR-0005/0003/0006), demote the `ADR-0015` `depends_on` to integration-only,
and mark "quota" V2 (all flagged in the ADR). The huma/`,inline` `TypeMeta` quirk (m1) is worth a note on a
future ADR-0005 successor — not blocking, not the model's.
