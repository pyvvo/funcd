# ADR-0027: Import-discipline depguard fix — make the path-scoped rules actually fire

- **Status**: Implemented
- **Date**: 2026-06-15 (**Implemented 2026-06-15** · **Accepted 2026-06-15** after judge pass — no Blockers left open. The judge
  *empirically* found two Blockers in the draft and both are folded: B1 — the real `api/` tree is **not**
  clean (`api/openapi/client_test.go` integration-imports `internal/controlplane`), so the rules scope to
  `!**/*_test.go` (production-only; api/ tests may integration-test against internal — a documented decision);
  B2 — a depguard `allow` list constrains the *whole* permitted set (it rejected `clock.go`'s stdlib `time`),
  so `platform-leaf` is **deny-only** (the cohesion `allow` + its scenario/fixture dropped; cohesion is a
  documented not-yet-real V1 case). The corrected rules verified **0 issues** on the real tree. Decision
  unchanged in substance: fix `api-boundary` + `platform-leaf` (`**/` glob prefix + prefix-deny) to the proven
  `e2e-boundary` form + a proving fixture each. No code/graph change, no new dep.)
- **Deciders**: green-0-rabbit
- **Tags**: ci, lint, depguard, import-discipline, guardrails, testing
- **Realizes**: [FEAT-0000/F01](../feat/0000-feat-v1.md) (project setup, CI, lint graph — the import-discipline enforcement)
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (§7 the import graph these rules
  *enforce* — this ADR fixes the **config that implements** it, not the decision itself),
  [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (found the latent bug while writing the `e2e-boundary`
  fixture, fixed `e2e-boundary`, and recorded `api-boundary`/`platform-leaf` as this follow-up — the lint-fixture
  pattern this ADR reuses), [ADR-0001](0001-project-setup-and-structure.md) (the `.golangci.yml` + `tests/lint-fixtures`)

## Context & Need

ADR-0002 §7 decides the import graph: `api/**` imports nothing from `internal/`/`pkg/`; `internal/platform/**`
is a leaf (no other-internal imports); `tests/e2e/**` imports only `pkg/**`+`api/**`. The `.golangci.yml`
`depguard` rules `api-boundary` / `platform-leaf` / `e2e-boundary` are how that decision is *enforced*.

While building ADR-0025 (testing), writing the `e2e-boundary` proving fixture exposed that the rule was
**silently dead** — and so were the other two path-scoped rules. Two config bugs, both verified empirically:

1. **`files` glob never matched.** depguard matches a rule's `files` patterns against the **absolute** file
   path; a bare `"api/**"` / `"internal/platform/**"` / `"tests/e2e/**"` (no `**/` prefix) never matches
   (the absolute path has a leading prefix before `api/`/`internal/`). The `**/` prefix is required.
2. **`deny` `pkg` is a PREFIX match, not a glob.** A trailing `/*` (e.g. `".../internal/*"`) silently never
   matches — depguard does prefix matching on the import path. The bare prefix `".../internal"` matches
   `internal/store`, `internal/store/memory`, … (proven: `deny ".../internal/store"` fired 2 hits;
   `".../internal/**"` fired 0; bare `".../internal"` fired 2).

ADR-0025 fixed `e2e-boundary` (its fixture needed it) and recorded `api-boundary` + `platform-leaf` as this
ADR. They have **never fired**: the `api/**`-imports-nothing and `internal/platform`-is-a-leaf guardrails
the blueprint and ADR-0002 promise are currently decorative.

The **production** trees are clean today (no `api/` *production* file imports `internal/`/`pkg/`;
`internal/platform/clock` imports no other internal package), but turning the rules on naively reddens CI on
two legitimate cases that force a real decision: (1) `api/openapi/client_test.go` (an `openapi_test` package)
imports `internal/controlplane` to mount the real huma server and prove the generated spec is *client-consumable*
— an integration **test**, exactly the cross-boundary pattern the SDK/CLI tests use; (2) a naive `deny:
internal` on `internal/platform` flags `clock.go`'s **stdlib** `time` import, because adding a depguard `allow`
list constrains the *whole* permitted set rather than carving an exception. So the fix must scope the boundary
rules to **production code** (`!**/*_test.go`) and use the **deny-only** form (no `allow`).

This ADR decides the **fix + its proof**: correct the two rules (production-scoped, deny-only), and add a
proving lint fixture per rule (the ADR-0025/`tests/lint-fixtures` pattern) so a future config regression
**fails CI**. One topic at one altitude — "make ADR-0002's import-discipline rules enforce what they decide."
It does **not** change the import graph (ADR-0002's decision stands); it fixes the buggy config that
implements it, so it is a corrective ADR, **not** a supersession.

## Scope

- **In**: fix the `api-boundary` + `platform-leaf` depguard `files` globs (`**/` prefix + `!**/*_test.go`
  production scoping) and `deny` patterns (prefix form, drop `/*`, deny-only); add a `lintfixture`-tagged
  proving fixture per rule + a `lintrules_test.go` case each.
- **Out**: changing the import graph itself (ADR-0002's decision — unchanged); the `e2e-boundary` rule
  (already fixed + fixtured by ADR-0025); any non-`depguard` linter; introducing new boundaries (V2).

## Constraints & Decision drivers

- **Enforce the existing decision, don't re-decide it** — ADR-0002 §7's graph is correct; only its
  `.golangci.yml` implementation was buggy. The fix conforms the config to the frozen ADR.
- **The boundary protects the *production* import graph, not test code** — `api/**` being the public contract
  that depends on nothing internal is a property of the *shipped* package; an `api/` **test** that
  integration-tests against `internal/controlplane` (like `client_test.go`, and like every SDK/CLI test) does
  not ship and does not couple the public surface. So `api-boundary`/`platform-leaf` scope to `!**/*_test.go`.
  (`e2e-boundary` is the deliberate exception — it is a *test-only* directory whose whole purpose is proving
  the public embed surface needs zero internal reach-in, so it must apply to its test files.)
- **Deny-only, no `allow`** — verified: a depguard `allow` list constrains the *entire* permitted import set
  (it rejected `clock.go`'s stdlib `time`), so it is the wrong tool. The bare-prefix `deny: internal` is
  correct and green today; if `internal/platform` ever grows a sub-package cross-import, a follow-up narrows
  the deny then (V1 has no such cohesion need).
- **Proven, not asserted** — each path rule gets a runnable fixture (the `tests/lint-fixtures` shell-out
  pattern ADR-0002 established + ADR-0025 extended) so the guardrail can't silently rot again.

## Scenarios

- **scenario: api-boundary-rule-fires** — *Given* a `lintfixture`-tagged **non-test** package under `api/**`
  that imports an `internal/` package, *when* golangci-lint runs the real config over it, *then* the
  `api-boundary` depguard rule **fires** (a `depguard` finding).
- **scenario: platform-leaf-rule-fires** — *Given* a `lintfixture`-tagged **non-test** package under
  `internal/platform/**` that imports a non-platform `internal/` package (e.g. `internal/store`), *when*
  golangci-lint runs, *then* the `platform-leaf` rule **fires**.
- **scenario: real-tree-stays-green** — *Given* the actual `api/` + `internal/platform/` trees (including
  `api/openapi/client_test.go`'s legitimate `internal/controlplane` integration import and `clock.go`'s
  stdlib `time`), *when* `golangci-lint run ./...` runs with the fixed rules, *then* it reports **0 issues**
  — the `!**/*_test.go` scoping exempts the integration test and deny-only leaves stdlib alone, so the
  tightening breaks no existing code. (Empirically confirmed: 0 issues with the fixed rules.)

## Decision

Fix the two rules in `.golangci.yml` to the proven-correct form (the same shape ADR-0025 applied to
`e2e-boundary`), and prove each with a fixture.

### 1. `api-boundary` — `api/**` *production* code imports neither `internal` nor `pkg`
```yaml
api-boundary:
  list-mode: original
  files:
    - "**/api/**"            # **/ prefix required (depguard matches the absolute path)
    - "!**/*_test.go"        # production-only: api/ tests may integration-test against internal
  deny:
    - pkg: "github.com/green-0-rabbit/funcd/internal"   # prefix match (no trailing /*)
      desc: "api/** production code must not import internal/*"
    - pkg: "github.com/green-0-rabbit/funcd/pkg"
      desc: "api/** production code must not import pkg/*"
```
The `!**/*_test.go` scoping is a **decision**: ADR-0002 §7's "api/** imports nothing from internal" is a
property of the *shipped public contract* (production code). An `api/` **test** that mounts the real server
to validate the OpenAPI spec (`api/openapi/client_test.go` → `internal/controlplane`) does not ship and does
not couple the public surface — it is the same cross-boundary integration pattern every SDK/CLI test uses, so
it is permitted.

### 2. `platform-leaf` — `internal/platform/**` *production* code imports no *other* internal package
```yaml
platform-leaf:
  list-mode: original
  files:
    - "**/internal/platform/**"
    - "!**/*_test.go"
  deny:
    - pkg: "github.com/green-0-rabbit/funcd/internal"
      desc: "internal/platform is a leaf; must not import other internal packages"
```
**Deny-only, no `allow`** — verified: a depguard `allow` list constrains the *whole* permitted set (it
rejected `clock.go`'s stdlib `time`), so it's the wrong tool. The bare-prefix deny is green today
(`internal/platform/clock` imports only stdlib). It *would* flag a future `internal/platform/x` →
`internal/platform/y` import — but V1 has no such cohesion need (only `clock` exists, with no internal
imports); if that ever changes, a follow-up narrows the deny to the specific non-platform roots then.

### 3. Proving fixtures (the `tests/lint-fixtures` pattern)
A path-scoped rule can only be fixture-proven by a file **under its matched path** (the `e2e-boundary`
fixture lives at `tests/e2e/boundary-fixture`). The fixtures are **non-test** `bad.go` files (so the
`!**/*_test.go` scoping still matches them):
- `api/lintfixture/bad.go` (`//go:build lintfixture`) imports `internal/store` → `api-boundary-rule-fires`.
- `internal/platform/lintfixture/bad.go` (`//go:build lintfixture`) imports `internal/store` →
  `platform-leaf-rule-fires`.

Each is `lintfixture`-tagged (invisible to normal `go build`/`just ci`/`just lint`); `lintrules_test.go`
runs golangci over each with `--build-tags lintfixture` and asserts the finding, `-short`-skippable.

## Contracts

### `.golangci.yml` (the two fixed rules — above)
### `tests/lint-fixtures/lintrules_test.go` (new cases, reusing the existing `lintFixture` helper)
```go
func TestScenario_APIBoundaryBlocksInternalImport(t *testing.T)        // scenario: api-boundary-rule-fires
func TestScenario_PlatformLeafBlocksOtherInternalImport(t *testing.T)  // scenario: platform-leaf-rule-fires
```
### Fixtures (non-test `bad.go`, `//go:build lintfixture`)
```
api/lintfixture/{bad.go,doc.go}                # bad.go imports internal/store
internal/platform/lintfixture/{bad.go,doc.go}  # bad.go imports internal/store
```
The `real-tree-stays-green` scenario is verified by `golangci-lint run ./...` (0 issues) in the build, not a
separate fixture.
### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `.golangci.yml`, the `tests/lint-fixtures/lintrules_test.go` shell-out helper, `go tool golangci-lint` | no Go-code dependency change |
| Adds (lib) | none | config + tagged fixtures + test cases |
| Exposes | two enforcing depguard rules + three fixtures | `real-tree-stays-green` is the no-regression guard |

## Implementation plan

1. **`.golangci.yml`** — apply the `api-boundary` + `platform-leaf` fixes (above): `**/` glob prefix,
   `!**/*_test.go` production-only scoping, bare-prefix deny, **no `allow`**.
2. **`api/lintfixture/bad.go` + `doc.go`** — tagged (non-test) fixture importing `internal/store`.
3. **`internal/platform/lintfixture/bad.go` + `doc.go`** — tagged (non-test) fixture importing `internal/store`.
4. **`tests/lint-fixtures/lintrules_test.go`** — two new cases via the existing `lintFixture` helper.
5. **Verify**: each fixture fires (`go test ./tests/lint-fixtures/...`); the real tree stays green
   (`golangci-lint run ./...` → 0 issues — exempting `client_test.go` + leaving stdlib alone); four sub-checks pass.
6. **Definition of done**: `just ci` green; `api-boundary` + `platform-leaf` fire on a stray *production* import;
   no false positives on the real tree (tests + stdlib unaffected); no new dependency; no identity/path leak.

## Review checklist

- [ ] **`api-boundary` fires on production** (`api-boundary-rule-fires`): a stray `internal/` import in a
      non-test `api/**` file trips `depguard`; rule uses `**/api/**` + `!**/*_test.go` + prefix deny on
      `internal` **and** `pkg`.
- [ ] **`platform-leaf` fires** (`platform-leaf-rule-fires`): a non-platform internal import in a non-test
      `internal/platform/**` file trips it; **deny-only** (no `allow`).
- [ ] **No regression** (`real-tree-stays-green`): `golangci-lint run ./...` → **0 issues** on the real code
      (`client_test.go`'s integration import exempt via `!_test.go`; `clock.go`'s stdlib `time` unaffected);
      `just ci` green.
- [ ] Fixtures are `lintfixture`-tagged (excluded from `just ci`/`just lint`/`go build`); the import graph
      (ADR-0002 §7) is **unchanged** — only the enforcing config is fixed; **no new dependency**; no leak;
      every Scenario a named passing test.

## Consequences

- (+) **The import-discipline guardrails are real**: `api/**`-imports-nothing and `internal/platform`-is-a-leaf
  now actually fail CI on violation, with a proving fixture each (the latent bug can't silently return).
- (+) **No code change, no new dependency** — a config fix + tagged fixtures; the real tree is already clean,
  so it's a pure tightening.
- (+) **Completes the depguard-fix set** ADR-0025 started (`e2e-boundary` done there; `api-boundary` +
  `platform-leaf` here) — all three path-scoped rules now enforced + fixtured.
- (−) **Fixtures live under `api/` and `internal/platform/`** (the only paths their rules match) — mitigated
  by the `lintfixture` build tag (invisible to every normal build/lint/test).
- (note) **Roadmap**: P-Y depends only on `ADR-0002` (the graph it enforces); it's an independent leaf.

## Temporary workarounds

None. The fix is the permanent correct form; the fixtures are the permanent regression guard.

## Alternatives considered

- **Treat it as a pure bug fix, no ADR** — defensible (it's a config typo correcting a frozen ADR's
  implementation), but ADR-0025 explicitly recorded it as a tracked follow-up item (P-Y), and the
  fixture-convention decision (a proving fixture per path rule) is worth recording. A one-line `.golangci.yml`
  diff with no proof is exactly how the bug survived this long.
- **A superseding ADR for ADR-0002** — rejected: ADR-0002's *decision* (the import graph) is correct and
  unchanged; only the config implementing it was buggy. Superseding would wrongly imply the decision changed.
  This is a corrective config ADR that *relates to* ADR-0002.
- **`platform-leaf` *with* an `allow` exception for `internal/platform`** — the original draft's approach, to
  permit platform-internal cohesion. **Rejected (empirically broken)**: a depguard `allow` list constrains the
  *entire* permitted import set, so it rejected `clock.go`'s stdlib `time` — reddening the real tree. The
  deny-only form is correct + green; the cohesion concern is a documented, not-yet-real V1 case (only `clock`
  exists). This is why the proven `e2e-boundary` rule also has no `allow`.
- **Apply `api-boundary` to test files too (move `client_test.go` out of `api/`)** — keeps `api/` *and its
  tests* free of `internal/`. Rejected for V1: the boundary's real guarantee is about the *shipped public
  contract* (production code), and `client_test.go` is a legitimate spec-consumption **integration** test
  (mounting the real server) — the same pattern every SDK/CLI test uses. Scoping to `!**/*_test.go` enforces
  the production guarantee without banning integration tests or relocating a test that belongs beside the spec.
- **`list-mode: strict` (allow-list only)** — rejected: it would require enumerating every permitted import
  (stdlib, third-party, api/**) — far more brittle than a targeted deny for a leaf/boundary rule.

## Open questions

| Question | Where it gets answered |
|---|---|
| Should *every* internal package boundary get a depguard rule (not just `platform`)? | a broader import-graph-hardening pass (V2) — F01's V1 scope is the three boundaries ADR-0002 named |
| A generic "each path rule must have a fixture" meta-check | a lint-of-the-lint follow-up; V1 ships the three fixtures explicitly |

## References

- [ADR-0002](0002-source-code-conventions-and-patterns.md) §7 — the import graph these rules enforce.
- [ADR-0025](0025-testing-strategy-and-e2e-harness.md) — found the bug, fixed `e2e-boundary`, established the
  `tests/lint-fixtures` proving-fixture pattern, and recorded this follow-up.
- [.golangci.yml] — the `depguard` rules; [tests/lint-fixtures/lintrules_test.go] — the shell-out fixtures.
