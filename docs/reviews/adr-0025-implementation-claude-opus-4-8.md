# ADR-0025 Implementation Review — Testing strategy & e2e harness (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors (model-attributed); 1 `adr`-attributed follow-up recorded

The four-tier taxonomy is realized with runnable artifacts, not just prose: the `tests/e2e` embed harness
(public-surface-only), the L2 contract-suite drift guard, the `e2e-boundary` lint fixture, and the gated L4
Linux skeleton. All judge fixes landed. Notably, writing the M1 fixture **discovered the `e2e-boundary`
depguard rule was silently dead** (its `files` glob never matched golangci's absolute paths, and its deny
used a `/*` glob where depguard does prefix matching) — exactly the failure the judge predicted. The fix
makes the public-surface boundary genuinely enforced; the fixture is now the regression guard. `just ci`
stays green; no new dependency.

**Reviewed against**: ADR-0025 Contracts/Scenarios/Review-checklist/DoD · blueprint "tests/e2e, zero infra" +
the synced layout · ADR-0014 (`InMemory()`), ADR-0024 (L3a), ADR-0011 (`linux && integration` tag), ADR-0002
(no-mocks, depguard) · FEAT-0000/F20.
**Date**: 2026-06-15

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` | exit 0 |
| `go test ./...` | PASS (full suite, incl. the lint-fixture shell-outs) |
| `go test -race ./tests/e2e/...` | PASS |
| scenarios | 5/5 runnable PASS (`e2e-embed-inmemory-boots`, `…-run-shutdown-crash-only`, `…-multiple-platforms`, `e2e-boundary-rule-fires`, `contract-suite-coverage-guard`) + L4 `linux-integration-deploy-invoke` correctly **deferred** (build-tagged `t.Skip`, excluded from `just ci`) |
| `golangci-lint run ./...` | **0 issues** (the e2e-boundary fix adds no false positives on the real `tests/e2e`) |
| public-surface discipline | `tests/e2e` imports only `pkg/funcd` + `api/fault` + stdlib/testify — **enforced**: the fixture proves a stray `internal/` import now trips depguard (2 hits) |
| L4 gating | `go test -list` shows L4 absent without the tag; `-tags integration` on darwin → "no tests to run" (the `linux` constraint), on a Linux runner it compiles+skips |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (no new dep) |
| identity | clean |

## 🔴 Blockers / 🟡 Major / Minor
None (model-attributed).

## ✅ Verified correct — keep it
- **The `e2e-boundary` rule is now real, and proven so** (`.golangci.yml` + `tests/e2e/boundary-fixture` +
  the `e2e-boundary-rule-fires` case in `tests/lint-fixtures/lintrules_test.go`): the implementer found the
  rule was silently dead and fixed both halves — `files: "**/tests/e2e/**"` (the `**/` prefix is required;
  depguard matches the **absolute** path) and `deny pkg: ".../internal"` (depguard deny is a **prefix** match;
  the prior `/*` glob never matched). The fixture (mirroring the any-leak/mock-framework precedent) is the
  regression guard. This is the headline value of ADR-0025 — keep it.
- **L3b embed harness** (`tests/e2e/e2e_test.go`, package `e2e_test`): drives `funcd.New(funcd.InMemory())`
  through the **public** surface only (boot + missing-dep → `fault.Invalid`, crash-only Run/Shutdown,
  multi-instance) — the distinct fact (vs the internal `funcd_test.go`) is *zero `internal/` reach-in*, now
  mechanically enforced.
- **L2 drift guard** (`contractcoverage_test.go`): walks `internal/` on disk (no internal import → stays
  inside the boundary), asserts every `<port>contract` has a `contract.go` + the 8 known ports are present.
- **L4 honestly deferred** (`linux_integration_test.go`, `//go:build linux && integration`): documents the
  full walk, `t.Skip`s with its prerequisite reason, excluded from `just ci`, wired to `just test-integration`
  — the containerd-tag precedent, no fake pass.
- **No new dependency; blueprint synced** (the `tests/` layout reconciled to `internal/<port>contract` +
  the co-located Linux lane).

## Definition of Done
ADR Review-checklist: **6/6** hold (taxonomy real · public-surface enforced *and proven* · L4 defined+gated ·
L2/L3a recorded not regressed · `just ci` green + no dep + follow-up recorded + no leak). Scenarios: 5/5
runnable named+passing, L4 deferred. ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## 🟠 `adr`-attributed follow-up (does NOT count against the model)
- **The same depguard latent bug affects `api-boundary` and `platform-leaf`** (`.golangci.yml`): both use the
  dead `files` glob (no `**/` prefix) + `deny .../internal/*` (broken `/*`), so they have **never fired**.
  Verified safe today — no `api/` file imports `internal/`/`pkg/`, and no `internal/platform/` file imports
  another internal package — so nothing is actively hiding. This is an **ADR-0002** config defect (its import
  graph isn't enforced as written); the clean fix (esp. `platform-leaf`, which must deny `internal` but *allow*
  `internal/platform/*` — an allow-list nuance) belongs in a **corrective/superseding ADR for ADR-0002's
  depguard config**, with fixtures mirroring this one. ADR-0025 fixed only its own rule (`e2e-boundary`),
  staying in scope. **Recorded as the next decision to take.**

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0025 (implementation) → pass, 0/0/0 model-attributed, 1 adr-attributed
follow-up, DoD 6/6. See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0025 `Reviewing → Implemented`, feat F20 → `implemented`. Real edges ADR-0014/0024/0011.
Open the **ADR-0002 depguard-config correction** follow-up (api-boundary + platform-leaf) recorded above.
Next: P-T (packaging) — the last item.
