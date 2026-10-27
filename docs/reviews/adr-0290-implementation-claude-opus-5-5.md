## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0290 implementation, model: claude-opus-5-5)

This review covers 2efbb72a on `feat/adr-0290-held-namespace-calls` (the implementation commit on top of the accepted
ADR, 9c6304e5) against ADR-0290's Decisions, Contracts, Scenarios, Review checklist and done-when line. The checks
below ran in a worktree detached at 2efbb72a. `scripts/agent/gate.sh`, with its e2e suite, runs once on the branch
after this review and did not run here.

### Checks run
- `go build ./...`: ok.
- `go test -race -count=1` on `internal/dataplane` and `internal/platform/hold`: ok. On `pkg/funcd`: every test passes
  except `TestScenarioFinishedRunKept`, which fails 4 of 4 on origin/main 5fe2549f as well (Routed, below); the
  branch's own `pkg/funcd` tests, `TestDataPlaneAsksTheNamespaceHold` and `TestEveryRunnerConsultsHoldInNamespace`,
  pass.
- `go vet` on the three packages, plain, `-tags dev`, `-tags e2e` and `GOOS=linux`: ok.
- `golangci-lint run` on the three packages: 0 issues. `gofmt -l`: nothing listed.
- `just check-hygiene check-specs`: ok.
- Five mutants, run with `go test -overlay`, each failing the test that should see it:
  - the hold check moved before `enforce` in `serveFunction` fails `TestScenarioAuthBeforeHold`;
  - the upstream refusal answering the hold's detail fails `TestScenarioLakeUpstreamRefused`;
  - `NamespaceMarker` preferring the `data` marker fails `TestNamespaceMarker`;
  - `NamespaceMarker` counting the platform's hold fails `TestNamespaceMarker` and `TestScenarioPlatformHoldUnchanged`;
  - `c.callGate()` always returning `hold.NeverCall` fails `TestDataPlaneAsksTheNamespaceHold`.

  The builder's two mutants (each check removed) fail four scenario tests and the Lake test.

### Minor
- **`TestNamespaceMarker` lives in `hold_test.go`, not the `namespace_test.go` the plan names** (`model`): the hold
  package keeps all its tests in `internal/platform/hold/hold_test.go`, and the test covers every case the plan lists
  (`restore` preferred, `data`, none, the platform hold alone, `NeverCall`). No change needed.

### Routed (not this work's)
- **`TestScenarioFinishedRunKept` fails on main** · home: elsewhere · destination: a fix on main, before any PR can
  pass CI. The test dates its Backups from a fixed `time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)` with an
  `ExpireTime` an hour later (`pkg/funcd/target_sync_internal_test.go:235-248`), and the expiry runner reads the real
  clock, so `queued` is now deleted as expired before it can succeed. It fails on main's CI (ci and e2e jobs of the
  2026-10-27 run) and on the 2026-10-26 Stress run, listed on #1142. Nothing in this branch touches it.
- The S3 gateway's writes into a held namespace are the ADR's Out item, tracked by #1302.

### ✅ Verified correct (keep)
- **The gate's place** (`internal/dataplane/dataplane.go`, `serveFunction`): after `enforce`, the Function lookup and
  the observability fill, before the limiter and the activator. Internal calls skip only the PEP. A 401 says nothing
  of the hold, a missing Function still answers 404, and a refused call wakes nothing.
- **The upstream refusal** (`serveUpstream`): the existing `edge upstream unavailable` with `Retry-After`, so a caller
  that never authenticated cannot tell a hold from an engine that is down. `serveStatic` is untouched, so a Site keeps
  serving.
- **`heldError`** writes Decision 2's two details word for word. `refuseHeld` sets `Retry-After` before the problem
  body, as the limiter's `writeTooMany` does.
- **`(*Hold).NamespaceMarker`** reads the in-memory markers under the read lock, `restore` first. `hold.NeverCall` is
  the shared `never{}` value, with its `gochecknoglobals` reason, as `NeverNamespace` is.
- **The wiring**: `Option` and `WithCallGate` leave `Handler`'s 25 callers as they were. The Server defaults to
  `hold.NeverCall`, and the daemon passes `c.callGate()` beside `c.gate()` and `c.nsGate()`.
- **The tests**: nine `TestScenario*` tests, one per scenario. Each refused case asserts the 503, `Retry-After: 30`, the
  problem type and detail, and that neither the scaler nor the worker was reached. `TestDataPlaneAsksTheNamespaceHold`
  assembles the daemon `WithHold` over a marker written by `hold.MarkNamespaces`, as `restore run` writes it. It sees
  the refusal, an unheld namespace reaching the activator, and the release. It uses a short `os.MkdirTemp` data dir.
- **Docs**: the runbook's step 8 and `examples/workload-backup.md` say what a held namespace's callers get, that Sites
  keep serving, and that S3 gateway writes are not refused yet (#1302).
- **Tracking**: the ADR at `Reviewing` with its substance unchanged since acceptance (only the status line moved),
  F205 at `reviewing`, and ADR-0222's back-link added at acceptance.

### Definition of done
8 of 8: the seven Review checklist items and the done-when line's checks that run per change (the tests with
`-race`, build, vet plain, dev, e2e and Linux, lint, and `just check-hygiene check-specs`). The line's
`scripts/agent/gate.sh` runs once on the branch after this review.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0290 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 8/8. See
docs/reviews/model-scorecard.md.

### Recommendation
Stamp ADR-0290 `Implemented` and F205 `implemented`, then run the gate on the branch. The Minor needs no change.
