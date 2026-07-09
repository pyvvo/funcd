# ADR-0113 implementation review — edge authn PEP (F77)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0113 implementation, model: claude-opus-4-8)

The security keystone of FEAT-0006 (item D of the A→E ingress-hardening chain) is implemented
correctly. Every judge-folded SECURITY item holds under code inspection; the four Go verification
lanes are green; every non-deferred Scenario has a named, un-skipped, passing test. The model built
the PEP exactly to the frozen Contracts, and the fail-closed / reject-before-`store.Get` /
no-injection posture is real, not asserted.

### Verification (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| test | `go test ./internal/edge/... ./internal/dataplane/ ./internal/route/ ./api/types/v1alpha1/ ./api/fault/ ./internal/platform/config/ ./cmd/funcd/ ./pkg/funcd/ -count=1` | exit 0 — all `ok` (incl. `internal/edge/authn`, `internal/dataplane`, `pkg/funcd` 59.8s) |
| lint | `go tool golangci-lint run ./internal/edge/... ./internal/dataplane/... ./pkg/funcd/... ./api/types/... ./internal/platform/config/... ./cmd/funcd/...` | **0 issues**, exit 0 |
| mod | `go mod verify` | exit 0 |

Venom lane (`e2e/env-echo.venom.yml` → `F77 edge authn PEP — /secure is 401 anonymous, served with a
bearer`) reported PASS by the builder on real containerd; trusted as env-verified, not re-run.

### 🔴 Blocker — none

### 🟡 Major — none

### Minor

- **`authn-disabled-passthrough` has no uniquely-named Scenario test.**  ·  attribution: `model`  ·
  The 7th ADR Scenario (no PEP wired + no `authenticated` stance ⇒ served anonymously, back-compat)
  is proven only *implicitly*: the whole legacy `internal/dataplane` suite (`dataplane_test.go`,
  `frontdoor_test.go`, changed by +2 lines each to pass a `nil` enforcer) still serves anonymously,
  and `TestNilEnforcerFailsClosed` exercises the nil-enforcer branch (its `open` corollary is the
  passthrough). Behavior is genuinely covered and back-compat is demonstrated by the unchanged suite
  staying green — but a dedicated `TestScenario…Passthrough` naming the Scenario would close the
  "one named test per Scenario" letter. Non-blocking polish.
- **Internal fn-to-fn bypass rides existing coverage.**  ·  attribution: `model`  ·
  `serveFunction` gates on `if !internal` (`internal/dataplane/dataplane.go:112`) so an internal
  (ADR-0064) request is never edge-authn'd — correct and spoof-proof (the marker is an in-process
  `context` value). No *new* named test asserts it; it is covered by the pre-existing internal-invoke
  tests continuing to pass. A one-line dedicated assertion would be tidier. Non-blocking.

### ✅ Verified correct (keep it)

Security folds (each read in code, not taken on faith):

1. **Fail-closed** — `internal/dataplane/dataplane.go:112-123`: a nil Enforcer passes through **only**
   when `stance != authenticated`; an `authenticated` stance with a nil Enforcer returns
   `fault.Unauthorized` → 401. Proven by `TestNilEnforcerFailsClosed` (dataplane) — no silent
   fail-open.
2. **PEP before `store.Get`** — in `serveFunction` the `Enforce` call (`:113-117`) precedes
   `store.Get` (`:124`) and the activator hop (`:142`). `TestScenarioAuthnRequired401NoWake` requests
   a **nonexistent** function in an `authenticated` namespace and asserts **401, not 404**
   (`dataplane/authn_test.go:74-82`) plus spy-scaler `count==0` — no function-enumeration oracle, no
   wake. The prior `authStance` store read is a **Namespace** read, never a Function read, so it
   leaks nothing about function existence.
3. **No principal/namespace injection** — `Enforce` authorizes against the passed-in resolved
   `target.Namespace` (`internal/edge/authn/authn.go:74`), never a re-read of `X-Funcd-Namespace`.
   The same `ns` governs the stance resolution, the authorization scope, and the function lookup, so
   a caller cannot authorize against namespace A while invoking a function in B; for a Route hit the
   namespace is the server-compiled `m.Namespace`, stronger still.
   `TestScenarioAuthedUnauthorized403` (token scoped to `other`, target `team`) proves the cross-ns
   deny.
4. **Connection-scoped Identity** — the `Identity` comes solely from `creds.Lookup(ctx, token)`
   (`authn.go:64`), the token extracted from `Authorization: Bearer` / `X-Api-Key` (`:87-95`); no
   principal is ever read from the request body/header.
5. **Enforce-never-decide** — the PEP calls `auth.Authorizer.Authorize(Request{Verb: VerbGet,
   Kind: KindFunction, Namespace: target.Namespace})` (Action-empty ⇒ RBAC) and only maps the
   verdict to a fault (`:70-82`); it embeds no policy. `TestScenarioAuthedButUnauthorized403` shows
   the PDP — not the PEP — is the denier.
6. **Internal fn-to-fn bypass** — see Minor above; correct.
7. **Route-open-overrides-namespace** — `authStance` returns a set Route mode first
   (`dataplane.go:148-160`); `TestScenarioRouteOpenOverridesNamespace` un-gates `/pub` in an
   `authenticated`-default namespace with no token → 200.

Contracts & conventions:

- `Enforcer`/`Deps`/`Credentials`/`AuthMode`/`RouteAuth`/`EdgeAuth` match the ADR Contracts exactly;
  `Credentials` is a **local** one-method interface — `internal/edge/authn` does not import
  `internal/controlplane`. No `any`/`interface{}` in exported signatures; `api/fault` for all errors;
  ctx-first; `log/slog`; no `panic`/`fmt.Print*`.
- Stance surface: `RouteSpec.Auth`/`RouteAuth`, `EdgeDefaults.Auth`/`EdgeAuth`, enum `Schema()` and
  `validateAuthMode` (empty|authenticated|open) with the `TestRouteAndNamespaceAuthValidate` matrix
  rejecting `"public"`/`"nope"`. OpenAPI regenerated (`api/openapi/funcd.v1alpha1.yaml`, +25 lines).
- Router/reconciler carry `AuthMode` end-to-end: `Entry.Auth` → `compiled.auth` → `Match.Auth`
  (`router.go`), compiled from `rt.Spec.Auth.Mode` in `reconcile.go:225-229`.
- Facade wiring: `WithEdgeAuth()` sets `edgeAuthEnabled`; the Enforcer is built from
  `c.credentials`+`c.authorizer` only when enabled (`funcd.go:667-668`), nil otherwise (fail-closed
  for `authenticated`, passthrough for `open`). Config `server.auth.edge` (`FUNCD_AUTH_EDGE`) →
  `cmd/funcd/main.go:226`.
- Venom lane is non-cascading: only the `secure-echo` Route is `authenticated`; the default namespace
  stays `open`, so the existing anonymous curls are unaffected.
- ADR at `Reviewing` with an intact Accepted acceptance-note (single lifecycle bump; the file is the
  in-flight untracked batch artifact — no prior committed baseline to diff, content internally
  consistent). F77 feat row at `reviewing`; the row's "authenticated-by-default" reconciled to the
  phased opt-in and "Cedar decides" reconciled to "the PDP — RBAC in V1, Cedar in V2".

### Definition of Done

7 / 7 ADR Review-checklist items hold (Contracts match; PEP inside serving path before the activator,
spy-scaler asserted; ADR-0018 bearer scheme + connection-scoped Identity; delegated authz with
401/403; stance resolution incl. Route-open override; `WithEdgeAuth`-unset passthrough/fail-closed
with RFC 9457; Go e2e + Venom green + F77 row advanced). Generic phase DoD holds (real logic, no
stubs; conventions; deps unchanged; tree matches the ADR surface). Misses: none blocking — two Minor
naming-of-test nits (`model`), no attribution against correctness.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0113 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 7/7.
See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. The security keystone is sound: fail-closed, reject-before-`store.Get` (no enumeration
oracle), no principal/namespace injection, connection-scoped identity, genuine PDP delegation — all
verified in code and by passing scenario tests, judge folds held. Stamp ADR-0113 `Implemented`,
advance F77 → `implemented`, move the board card to Done. The two Minor test-naming nits are optional
polish for the builder, not a re-loop.
