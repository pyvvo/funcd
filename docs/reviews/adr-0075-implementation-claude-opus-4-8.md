# ADR-0075 implementation review — Cedar invoke authorization (`link::invoke`)

- **ADR**: [0075](../adr/0075-cedar-invoke-authorization.md) · **phase**: implementation · **model**: claude-opus-4-8
- **Verdict**: **pass** (DoD met, no Blockers/Majors) · 2026-06-23

## Verification (evidence — independently re-run, not trusted)

| check | result |
|---|---|
| `CGO_ENABLED=0 go build ./...` | OK |
| `go test ./...` | **50 ok, 0 fail** (`internal/auth/...` + `internal/workernode/local/...` `-race` clean) |
| `go tool golangci-lint run` (touched pkgs) | **0 issues** |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff → no new dep** (reuses ADR-0074's cedar-go) |
| ADR-0074 KV tests + `authcontract` | **green** (unaffected) |

## Scenarios → tests (named, passing)

`declared-link-invokes` (`cedar.TestScenarioDeclaredLinkInvokes` + e2e `TestScenarioPolicyRevokesInvokeE2E` step 1) ·
`undeclared-alias-denied` (`local.…InvokeErrorTaxonomy/no_link`, e2e `TestScenarioUnlinkedAliasDeniedE2E`, +
defense-in-depth `cedar.TestScenarioUndeclaredNotInLinks`) · `policy-revokes-invoke` (`cedar` + `local` handler
gate + e2e `TestScenarioPolicyRevokesInvokeE2E` step 2) · `rbac-and-kv-unaffected` (`cedar.TestScenarioRBACAndKVUnaffected`
+ all ADR-0074 KV tests stay green).

## ✅ Verified correct (keep)

- **No SSRF / undeclared-invoke impossible — two gates.** The `Resolver` still `Forbidden`s an undeclared alias
  (naming default-deny, untouched), **and** the built-in only permits `caller.links.contains(resource)` (proven by
  `TestScenarioUndeclaredNotInLinks`: a Function resource not in `links` is denied). Defense-in-depth — keep both.
- **Forbid wins** — a declared link + a `forbid` `Policy` ⇒ Forbidden (operator revoke without editing the caller),
  proven at unit + handler + e2e level (the greeter is never reached).
- **Principal connection-scoped** — `Function::"<ns>/<caller>"` built in `NewHandler` from the fixed `Ref`, never
  the request body.
- **Built-in Cedar lives in `.cedar` files, embedded** ([builtin_kv.cedar](../../internal/auth/cedar/builtin_kv.cedar),
  [builtin_invoke.cedar](../../internal/auth/cedar/builtin_invoke.cedar) via `//go:embed`) — both the KV owner-write
  and the invoke link-as-grant rules are now authored as real Cedar (legible/editable), not inline Go strings
  (decider request, applied to both built-ins).
- **Reuses the framework** — one action (`link::invoke`), `Function` as a resource type, one built-in, the
  `EntityProvider` extended for a Function resource + the principal's `links` Set (`cedar-go` `types.NewSet` /
  `Set.Contains`, verified). No new dep, no new `auth.Authorizer` port types. `NewHandler`/`NewManager` gained the
  Authorizer.
- **Relates-to, not supersedes, ADR-0064** — link-as-grant preserved as the built-in default; ADR-0064 untouched.

## Findings
None (Blocker/Major/Minor). The ADR's `Realizes: F44` had no feat row (orphan); the implementer added it in-session
(no-orphans invariant) — correct.

## DoD
ADR Review-checklist: **4/4** — schema (`link::invoke` + Function resource) + `EntitiesFor` (principal `links` Set +
Function-resource branch); built-in `permit … caller.links.contains(resource)` (link-as-grant preserved); `NewHandler`
gains the PDP + calls it after `Resolve` (unknown alias ⇒ Forbidden unchanged; deny ⇒ Forbidden; `forbid` revokes);
KV + rbac unchanged, no new dep, no leak.

## Recommendation
**pass** — `Reviewing → Implemented`. fn→fn invoke now joins KV under one Cedar PDP: `spec.links` is naming, the
built-in preserves ADR-0064's link-as-grant, and operators govern via `Policy` — egress/secrets can follow behind
the same schema-extension seam.
