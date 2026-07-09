## Verdict: changes requested — 1 blocker, 0 majors  (ADR-0110 implementation, model: claude-opus-4-8)

Route v2 + the namespace exposure model (F79). The exposure/matching surface is well built,
faithfully mirrors the existing `dataplane.Handler` solo/pooled addressing, and the judge-folded
items (zero-wake gate, empty-exposure normalization, HostRequired + deterministic RouteConflict,
StripPrefix downstream rewrite, resolve-not-proxy) are all implemented and tested. One ADR Scenario
— `internal-invoke-unaffected` — is both untested and actively violated by the shipped code, which
blocks sign-off.

### Verification (captured)

- `nix develop -c go build ./...` → **exit 0**.
- `nix develop -c go test ./internal/edge/... ./internal/route/... ./internal/dataplane/... ./api/types/v1alpha1/... ./pkg/funcd/ -count=1` → **exit 0** (`pkg/funcd` 58.2s; no F79 failures; the noted load-sensitive pool/python flakes did not surface).
- `nix develop -c go tool golangci-lint run <F79 pkgs>` → **0 issues, exit 0**.
- `nix develop -c go mod verify` → **exit 0** (all modules verified; no deps added).
- Venom containerd `env-echo` lane: trusted as env-verified by the builder (`F79-Route-path-resolves-to-the-function-and-serves` PASS); not re-run.

### 🔴 Blocker 1 — internal fn-to-fn invoke IS gated by exposure in `explicit` mode  ·  attribution: model

ADR-0110 Decision §6 (final line): *"Exposure gates only this public listener; the worker-node local
API (fn-to-fn, ADR-0064) is never gated."* Scenario `internal-invoke-unaffected`: *"Given any
exposure mode … invoked over the worker-node local API … succeeds regardless of Routes."*

The fn-to-fn broker forwards through the **same** gated handler as the public listener:
- `pkg/funcd/funcd.go:641-642` builds one `dpHandler := gateway.Chain(dataplane.Handler(c.store, act, p.edgeRouter, …), …)` and sets it into `dpHolder`.
- `pkg/funcd/funcd.go:356` wires the invoke Manager with `local.NewInvoker(dpHolder)`.
- `internal/workernode/local/invoker.go:29-34` synthesizes `POST /function/<target>` (+ `X-Funcd-Namespace`) and calls `p.dataPlane.ServeHTTP` — i.e. the gated handler.
- `internal/dataplane/dataplane.go:70-74`: on a router miss, `exposureMode(r, ns) == ExposureExplicit ⇒ 404` with no bypass for internal-origin requests.

There is no separate ungated internal handler and no internal-origin marker, so an internal invoke
into an `explicit` namespace targeting an unrouted Function is 404ed — exactly the private-but-
internally-callable case `explicit` mode is meant to enable.

Executed evidence (throwaway probe reproducing `proxyInvoker`'s request against the real
`dataplane.Handler` with a non-nil router and no matching route, then removed):

```
internal-style invoke into explicit ns → status 404
--- FAIL: an internal fn-to-fn invoke must NOT be gated by exposure (ADR-0110 §6)
    expected: 200
    actual  : 404
```

Compounding: the `internal-invoke-unaffected` Scenario has **no named test** (`grep -rn
"internal-invoke-unaffected"` → none; no `_test.go` seeds `ExposureExplicit` alongside a fn-to-fn
invoke). The existing fn-to-fn e2e suite (`pkg/funcd/invoke_e2e_test.go`) runs only in the `default`
(implicit) namespace, so it never exercises the invariant that is broken. A missing Scenario test is
itself a Blocker per the review method; here the underlying behavior would also fail it.

**Fix (builder):** exempt the internal path from the exposure gate — e.g. mark internal-origin
requests (a context flag / private header the public listener strips) that `dataplane` honors to
skip the `explicit` 404, or give the invoke broker a handler built without the gate. Then add the
`internal-invoke-unaffected` named test asserting a fn-to-fn invoke into an `explicit` namespace to
an unrouted Function succeeds. The ADR is satisfiable as written → model-attributed, loops back to
`adr-impl`.

### Minor
- Pooled-backend downstream rewrite **through a Route hit** is not directly asserted — `frontdoor_test.go` covers the SOLO case (`/orders/42 → /42`) but no test drives a POOLED backend (`spec.pooling.worker` set) through a matched Route to confirm `/function/<name>`+remainder. The code (`dataplane.go:93-101`) mirrors the pre-F79 `dataplane.Handler` path and the solo half is proven, so this is a coverage nit, not a suspected defect. · attribution: model.

### ✅ Verified correct (keep it)
- **Contracts match exactly.** `RouteSpec`/`RouteRule`/`RouteBackend`/`PathType`/`HTTPMethod` (`api/types/v1alpha1/route.go`) and `NamespaceSpec`/`ExposureMode`/`EdgeDefaults` (`namespace.go`) are field-for-field the ADR Contracts; huma `Schema()` enums for PathType/HTTPMethod/ExposureMode; no `any`/`interface{}` in exported signatures.
- **Zero-wake gate.** `dataplane.go:53-74` runs `router.Resolve` first, and on an `explicit` miss writes the 404 **before** `serveFunction`/`activator.ServeHTTP`. `TestScenarioExplicitGatesUnroutedNoWake` (`frontdoor_test.go`) uses a real spy scaler that increments+errors on `ScaleTo` and asserts `count == 0` — a true no-wake assertion.
- **Empty/absent exposure normalization.** `ExposureMode.Normalized()` maps every non-`explicit` value (incl. `""`) to implicit; used in both `dataplane.exposureMode` and `route.modeOf`; the handler gates only on `== ExposureExplicit`. Covered for the absent-Namespace case (`TestScenarioAbsentExposureServesByName`) and the empty-field case (`TestNamespaceExposureValidate`).
- **HostRequired + deterministic RouteConflict.** `reconcile.go:103-148` sorts routes by `(namespace,name)`, first-claim-wins on `(host,path,method)` keys, `HostRequired` for empty host in explicit mode. `TestScenarioCrossNamespaceHostIsolation` proves different-host isolation, HostRequired, and that the `(ns,name)`-first route wins the collision.
- **Replace-all program + delete-unroutes.** `Reconcile` lists all Routes, programs the router replace-all from the Ready set, and a deleted Route drops out (`TestScenarioRouteDeleteUnroutes`, `TestScenarioResolveMissAfterReplaceAll`).
- **Matcher.** exact / segment-aware prefix (`path == p || HasPrefix(path, p+"/")`) / exact-host / method, longest-path-first with host-qualified rows winning ties (`router.go:89-148`); `match-exact-vs-prefix`, `match-host`, `match-method`, `match-longest-prefix`, `StripPrefix` all named + passing. No glob/`:param`/SNI.
- **Resolve, not proxy.** The router returns a `Match`; the handler runs the existing activator hop. `route_e2e_test.go` (real `funcd.New`) asserts an unrouted explicit request 404s and a matched route resolves *past* the gate (not 404) with the activator hop preserved.
- **Wiring.** `p.edgeRouter` is created once (`funcd.go:448`) and shared by the Route reconciler (`ctrl.Register(KindRoute…)`, line 467-471) and the data-plane handler (line 641) — one live table.
- **Tracking / sync.** ADR at `Reviewing`; F79 feat row at `reviewing` (`docs/feat/0006…md:238`); blueprint synced with the front-door refinement + default-deny ingress (`blueprint.md:627-638`, ADR-0110 cited); OpenAPI regenerated (`defaultExposure`, `edgeDefaults`, `RouteSpec`, `RouteRule`, `pathType`). No deps added; conventions (api/fault, slog, ctx-first, one-file components) hold.

### Definition of Done
7 / 8 ADR Review-checklist items hold. Miss: item 5 — its "internal (fn-to-fn) invoke is never
gated" clause is violated (Blocker 1); the other two clauses of that item (explicit-miss 404-no-wake,
implicit fallback) do hold. The generic "every Scenario has a named, un-skipped, passing test" also
fails on `internal-invoke-unaffected`. Both trace to the single Blocker. All attributions: model.

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0110 (implementation) → changes-requested, 1 blocker / 0 majors /
1 minor, 2 model-attributed, DoD 7/8. See docs/reviews/model-scorecard.md.

### Recommendation
One fix unblocks sign-off: stop the exposure gate from applying to the internal worker-node local
API path, and add the `internal-invoke-unaffected` named test (explicit namespace + unrouted target
→ invoke succeeds). Everything else — contracts, the zero-wake gate, normalization, host/conflict
determinism, matcher, replace-all, wiring, doc sync — is correct and should not be regressed. Loops
back to `adr-impl`; the ADR stays `Reviewing`, the feat row stays `reviewing`, the board card stays
`In Progress`.

---

## Re-review: PASS — Blocker resolved, 0 new Blockers/Majors  (2026-07-08)

The builder fixed the single Blocker exactly as prescribed. Re-ran the full verification; the fix is
correct, spoof-proof, and adds the missing Scenario test asserting both directions. Signing off.

### Verification (captured, re-run)
- `nix develop -c go build ./...` → **exit 0** — confirms no `local`↔`dataplane` import cycle (the new
  `internal/workernode/local` → `internal/dataplane` edge; `dataplane` does not import `local`).
- `nix develop -c go test ./internal/dataplane/... ./internal/workernode/... ./internal/edge/... ./internal/route/... ./api/types/v1alpha1/... ./pkg/funcd/ -count=1`
  → **exit 0** (all packages `ok`; `pkg/funcd` 58.0s; the noted load-sensitive pool/python flakes did not surface).
- `nix develop -c go tool golangci-lint run <F79 pkgs>` → **0 issues, exit 0** (macOS `ld` version-mismatch warnings are env noise, not diagnostics).
- `nix develop -c go mod verify` → **exit 0** (all modules verified; no deps added).

### How the Blocker was resolved (verified at file:line)
- **Spoof-proof internal marker.** `dataplane.go:28-39` adds an unexported `internalKey struct{}` context
  key with `WithInternal(ctx)` / `isInternal(ctx)`. The marker is a **context value set only in-process**
  — it is *not* a header, so an external client on the public listener (whose request carries a fresh
  context) can never set it. Verified by reading the gate, not asserting it.
- **The gate honors it.** `ServeHTTP` (`dataplane.go:70-94`) computes `internal := isInternal(r.Context())`
  and, when internal, skips **both** the Route front door (`s.router != nil && !internal`, line 73) **and**
  the exposure gate (`s.router != nil && !internal && … == ExposureExplicit`, line 90) — serving the
  `/function/<name>` form directly. Public requests (`!internal`) are unaffected: the front door and the
  `explicit`-miss 404 still apply exactly as before.
- **The broker marks its context.** `invoker.go:34` wraps the in-process request context with
  `dataplane.WithInternal(cctx)` before `p.dataPlane.ServeHTTP` — so every fn-to-fn call is internal-marked.

### The four spot-checks required, confirmed
1. **`TestScenarioInternalInvokeUnaffected` is real, un-skipped, passing, and exercises BOTH cases.**
   `frontdoor_test.go:154-175`: same request (explicit ns `team`, **unrouted** fn `worker`) returns **200**
   with the internal marker and **404** without it. Ran isolated with `-v`: `--- PASS`. No `t.Skip`.
2. **The bypass is genuinely spoof-proof.** The marker is an in-process context value keyed on an
   unexported type (`internalKey struct{}`), unreachable from an HTTP request — read directly in the gate
   at `dataplane.go:70,73,90`. There is no header path into it. Confirmed by inspection.
3. **Zero-wake gate for PUBLIC explicit requests still holds.** `TestScenarioExplicitGatesUnroutedNoWake`
   (`frontdoor_test.go:104-112`) still asserts 404 with `scaler.count() == 0` via the real spy scaler —
   ran isolated: `--- PASS`. The `!internal` guard means the public path is byte-for-byte the prior
   behavior.
4. **No regression in the fn-to-fn suite.** `go test ./internal/workernode/local/ -v` → all pass, incl.
   `TestScenarioInvokeErrorTaxonomy` (success/403/404/422/500/503 propagation), `TestInvokerPropagates`,
   `TestScenarioPolicyRevokesInvoke`. The `default`-namespace fn-to-fn e2e still green.

### Tracking / substance
- **ADR substance unchanged.** ADR-0110 status is `Reviewing`; its Decision §6 ("the worker-node local
  API … is never gated") and the `internal-invoke-unaffected` Scenario were already present in the
  accepted ADR — the fix implements them, it did not alter the ADR. The builder made no ADR edit.
- F79 feat row at `reviewing` (`docs/feat/0006-feat-ingress-hardening.md:238`).
- The Minor from the prior review (no POOLED-backend-through-a-Route assertion) remains an untested-coverage
  nit, not a defect — not a sign-off blocker; leaving it noted.

### Re-review verdict
**pass** — 0 Blockers, 0 Majors, 1 pre-existing Minor (coverage nit). DoD now 8/8 (the previously-failing
item 5 "internal fn-to-fn invoke is never gated" now holds, with its named Scenario test). The single
model-attributed Blocker is closed. Stamping ADR-0110 `Reviewing → Implemented`, F79 → `implemented`,
board card → `Done`.
