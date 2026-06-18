# ADR-0034 Implementation Review — End-user journey acceptance e2e

**Verdict**: **pass** — `tests/e2e/journey_test.go` realizes all four scenarios through the public
surface only (real `funcdcli` binary + data-plane HTTP); node-gated, `e2e-boundary` clean, no new
dependency.
**Producing model**: claude-opus-4-8 · **ADR status at review**: Reviewing

## Evidence

- `go test ./tests/e2e/ -run TestE2EUserJourney -v` → `--- PASS (0.93s)`. It `go build`s
  `./cmd/funcdcli` and drives `push` → `apply` → `get`(Ready) → `http.Post` invoke, then the
  scale-to-zero cold-wake.
- `go tool golangci-lint run ./tests/e2e/...` → `0 issues` (the `e2e-boundary` depguard passes →
  imports only `pkg/**` + `api/**`, no `internal/`).
- `go.mod`/`go.sum` unchanged → no new dependency.
- Identity grep clean.

## Scenario → test

| Scenario | Covered by |
|---|---|
| user-pushes-and-deploys | the `push` + `apply` + `requireCLIPhase(Ready)` steps |
| user-invokes-over-http | the warm `http.Post` → 200 + handler body |
| user-wakes-scaled-to-zero | the `minReplicas:0` block (Idle → cold POST → 200) |
| public-surface-only | enforced by the `e2e-boundary` lint pass |

## Findings

🔴/🟡 None. **Minor (env):** node-gated — skips where `node` is absent (by design, ADR-0025 node tier).

## DoD

| Item | Status |
|---|---|
| Passes node-gated; skips without node | ✅ |
| Imports only `pkg/**` + `api/**` (real CLI binary + HTTP) | ✅ |
| Lint clean (e2e-boundary holds) · no new dependency · no leak | ✅ |

**Recommendation**: stamp **Implemented**. The embedded-server vs daemon-execution gap is the
recorded open question (a follow-up ADR), not a defect in this lane.
