# ADR-0027 Implementation Review — Import-discipline depguard fix (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0027 implementation, model: claude-opus-4-8)

The two dead path-scoped depguard rules (`api-boundary`, `platform-leaf`) are now enforced and proven. The
fix matches the already-shipped `e2e-boundary` form (`**/` glob prefix + bare-prefix deny), adds the
`!**/*_test.go` production scoping (so `api/`'s legitimate integration test stays legal) and is deny-only
(no `allow`, which would break stdlib). Both judge Blockers were folded pre-accept; the corrected rules report
**0 issues** on the real tree and **fire** on a stray production import (proven by two new `lintfixture`
shell-out cases). No code/graph change, no new dependency.

**Reviewed against**: ADR-0027 Contracts/Scenarios/Review-checklist/DoD · ADR-0002 §7 (the import graph, unchanged) ·
ADR-0025 (the proven `e2e-boundary` form + the `tests/lint-fixtures` pattern) · FEAT-0000/F01.
**Date**: 2026-06-15

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` | exit 0 (the two `lintfixture`-tagged fixtures are excluded) |
| `go test ./...` | PASS (full suite, exit 0, no failures) |
| `golangci-lint run ./...` | **0 issues** — real tree green: `api/openapi/client_test.go`'s `internal/controlplane` import exempt via `!**/*_test.go`; `clock.go`'s stdlib `time` unaffected (no `allow`) |
| scenarios | 2/2 PASS (`api-boundary-rule-fires`, `platform-leaf-rule-fires` — each fires `depguard` on a stray `internal/store` import); `real-tree-stays-green` verified by the 0-issues lint |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (config + tagged fixtures + test cases — **no new dep**) |
| import graph | ADR-0002 §7 unchanged — only the enforcing config fixed (corrective, not superseding) |
| identity | clean (the draft's illustrative `/Users/...` genericized) |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **The two rules now fire and are fixtured** (`.golangci.yml` + `api/lintfixture/bad.go` +
  `internal/platform/lintfixture/bad.go` + two `lintrules_test.go` cases): `**/api/**` / `**/internal/platform/**`
  glob prefix + bare-prefix `deny: internal`/`pkg` — the same proven form as `e2e-boundary`. The fixtures are
  non-test `bad.go` files (so `!**/*_test.go` still matches them) and prove the rule trips a stray import.
- **Production-only scoping is the right call** (`!**/*_test.go`): the boundary guards the *shipped* public
  contract; `api/openapi/client_test.go` mounting the real server to validate the OpenAPI spec is a legitimate
  integration test (the SDK/CLI pattern). Verified: the real tree is **green** with the fix — the judge's B1
  is resolved without relocating the test or banning integration tests.
- **Deny-only, no `allow`** (B2 resolved): the draft's `allow` exception rejected `clock.go`'s stdlib `time`
  (a depguard `allow` list constrains the whole permitted set); deny-only is green today and correct. The
  cohesion concern is a documented, not-yet-real V1 case (only `clock` exists, no cross-imports).
- **Corrective, not superseding** (ADR-0002 frozen): the fix touches `.golangci.yml` (the config implementing
  §7's graph), not the decision — the import graph is unchanged. Correct per ADR-0000's immutability rule.

## Definition of Done
ADR Review-checklist: **4/4** hold (api-boundary fires on production · platform-leaf fires, deny-only · no
regression — 0 issues · fixtures tagged + graph unchanged + no dep + no leak). Scenarios: 2/2 named+passing +
the green-tree guard. ADR substance unchanged beyond the `Accepted→Reviewing` bump (the post-accept edits were
identity + stale-draft-sentence cleanup, not a decision change).

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0027 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 4/4.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0027 `Reviewing → Implemented`. F01 stays `implemented` (this is a CI-hardening increment
under an already-delivered feature; the row links ADR-0001 + ADR-0027). All three path-scoped depguard rules
(`e2e-boundary` from ADR-0025; `api-boundary` + `platform-leaf` here) are now enforced + fixtured. Next: P-U.
