## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0221 implementation, model: claude-opus-5-5)

Reviewed: `git log eb15ee38..HEAD` on `feat/adr-0221-degraded-recovers-under-gate` (2f1f6425 fix, 2bda7415 test,
be787af7 status bump). Files: `internal/function/function.go`, `internal/function/degraded_gate_test.go` (new),
`internal/function/ready_test.go`, `pkg/funcd/issue796_internal_test.go`, `pkg/funcd/issue849_internal_test.go` (new),
`docs/adr/0221-degraded-recovers-under-gate.md`, `docs/feat/0000-feat-v1.md`.

### Blockers
None.

### Majors
None.

### Minor
- **No promotion-side test for a timed-out solo probe** · attribution: model · Decision 4 says a solo probe that
  times out counts not ready (row 3). The only timed-out variant is in `TestScenarioReadyFunctionStaysReadyUnderGate`
  (`degraded_gate_test.go`, "timed-out"), which runs the `Ready` path (`servingWorkers`), so it never reaches
  `servingReady`. `TestScenarioUnreadyWorkerNotPromoted` covers only a 503. The risk is low because `servingReady`
  reuses `probeReady` and the shared `httpx.NodeClient(probeTimeout)`, but a `Degraded` + `serveStalling` subtest in
  the guard would pin it. Fix: builder, optional.

### Verified correct (keep it)
- **Build / vet / lint / fmt, darwin and linux**: `go build ./...` exit 0 (darwin and `GOOS=linux`);
  `go vet ./internal/function/ ./pkg/funcd/` exit 0 on both; `golangci-lint run ./internal/function/... ./pkg/funcd/...`
  "0 issues." on darwin and with `GOOS=linux`; `gofmt -l` empty.
- **Tests with -race**: `go test -race -count=1 ./internal/function/` ok (11.1 s); `go test -race -count=1 ./pkg/funcd/`
  ok (16.4 s), so every unchanged test the plan lists (`TestIssue838_*`, `TestIssue309_*`,
  `TestRevisionMissingCountsServingWorkers`, `TestFailedGateKeepsOldServing`, `TestScenarioRegistryOutageNotReady`,
  `TestScenarioRuntimeOutageKeepsRoutes`, `TestIssue769_*`, `TestIssue796`, `TestScenarioDependencyReturnsStaysAsleep`,
  `TestScenarioMinReplicasOneUnaffected`) passes. The eleven scenario tests at `-race -count=3`: ok, no flake.
- **Against the base** (`-overlay` mapping `internal/function/function.go` to `git show eb15ee38:...`): all eight
  promotion tests fail — the seven `internal/function` ones at `degraded_gate_test.go:72` (`requireServesUnderGate`'s
  phase check, i.e. after the pre-state assertions passed, for the right reason) and
  `TestScenarioCallAnsweredWhileGateFails` with "the held call is answered" (the 2 s hold expires). The three guards
  (`UnreadyWorkerNotPromoted`, `ReadyFunctionStaysReadyUnderGate`, `AsleepGateNotPromoted`) pass on the base.
- **Mutants (all killed)**:
  1. `servingReady` without the probe (`if r.listening(in) {`) → `TestScenarioUnreadyWorkerNotPromoted` fails (#309 guard).
  2. `gateFailed` counting with `servingReady` for every read phase → `TestScenarioReadyFunctionStaysReadyUnderGate`
     fails (no new demotion of a `Ready` Function).
  3. `failPass` without the `started == Degraded && n >= 1` case → `TestScenarioFailedPassPromotesDegraded` and
     `TestScenarioFailedPassDuringPoolRebuildPromotes` fail.
- **Contract `servingReady`** (`function.go:1263-1291`): signature exact; S only, no fallback (`s == ""` → 0, 0, nil);
  pooled → `countWorkers` (entry read, unanswered host an error, #838); solo → one `namedInstances` List, running S
  replicas counted, ready = `listening && (materializer == nil || probeReady(..., readinessPath))`, which is
  `readyReplicas`' test including the legacy placeholder mode (`readyReplicas` returns `running` when the materializer
  is nil). Doc comment carries the ADR-0215 alignment note.
- **Decision 1 table** (`gateFailed`, `function.go:877-919`): asleep branch first and unchanged; `servingReady` only when
  the read phase is `Degraded`, else `servingWorkers`; rows in order: Ready+n≥1 keeps `Ready` with `n`; Degraded+n≥1 →
  `Ready`, `replicas: n`, `Ready=True` with no reason; running≥1 → `Degraded`, 0, `Restarting` with the new message;
  else the gate's phase and reason. C ≠ S stop and the period requeue unchanged. `RevisionReady` keeps
  `ObservedGeneration: gen` (`:863`). Count errors return as before (through `failPass`).
- **Decision 2** (`failPass`, `function.go:766-791`): `servingReady` only when `started` is `Degraded`, else
  `listeningCount`; a count error keeps the read status (#353, #838); `Degraded`+n≥1 → `Ready`, `replicas: n`,
  `Ready=True` with no reason; never writes `Failed`, never wakes; `RevisionReady` stamped with `fn.Generation`; writes
  only on change (`sameIgnoringMessages`).
- **Decision 3 / 5**: the secret-gate comment (`function.go:665-669`) states a running S worker keeps its start-time
  env; no worker is created (the call test asserts `poolCreates()` unchanged). The only new vocabulary is the message
  "no worker of the serving revision is ready"; no new phase, reason, condition, field or key.
- **Unchanged surfaces**: `programAllRoutes`, `endpoints.Upstream`, `finish`, `readyReplicas`, `countWorkers`,
  `servingWorkers`, `listeningCount` and the activator are untouched by the diff.
- **Scenarios**: eleven tests, each with a `// scenario: <name>` comment, named as the Implementation plan says; the five
  variant tests assert the pre-state (Degraded, `RevisionReady=False` with the gate's reason at the current generation,
  no route, upstream not ready; `ReconcileFailed` for the unanswered pool host), then phase, `replicas: 1`, `Ready` with
  reason "", `RevisionReady` reason and `ObservedGeneration == fn.Generation`, the route, upstream ready, and
  `RevisionReady=True` once the gate clears. `TestDegradedWithListeningReplicaStaysDegraded` was renamed to
  `TestScenarioUnreadyWorkerNotPromoted` and only gained assertions (no route, upstream not ready); the
  `running-not-listening` subtest changed only its expected message, as plan step 4 states. No assertion weakened.
- **`poolHost` hook** (`issue796_internal_test.go`): per-member state override under the existing mutex, cloned before
  the response is built; the default stays `ready`, so `TestIssue796` and neighbours are unaffected.
- **Tracking**: ADR diff is only the `Accepted → Reviewing` status line; feat F13 row reads `degraded recovery:
  reviewing`. The acceptance back-links (ADR-0161, ADR-0057, ADR-0093) and the blueprint edge (`blueprint.md:706`)
  are present on the base. Conventions: no `panic`, no new exported API, ctx-first, imports at top level.

### Definition of Done
9 / 9 ADR items hold (Review checklist 5, Definition of done 4). `just ci` was not run here by instruction; its
components for the touched packages are green (build, vet, lint on darwin and linux, fmt, `-race` tests), and the
repo-wide run belongs to `scripts/agent/gate.sh`. Generic DoD: holds (no stubs, contracts exact, no new deps, no scope
creep).

### Model scorecard
Not recorded here (the orchestrator records the batch's ledger rows). Row to record below.

### Recommendation
Pass. The minor (a timed-out probe on the promotion side) is optional hardening; it does not block. The review gate's
stamping (ADR `Reviewing → Implemented`, feat row, board card) is left to the orchestrator as instructed.

```json
{"adr": "0221", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 1, "model_attributed": 1, "dod_passed": 9, "dod_total": 9, "report": "docs/reviews/adr-0221-implementation-claude-opus-5-5.md", "notes": "pass; 8 promotion tests fail on base and pass, 3 guards pass on both, 3 mutants killed; minor(model): no Degraded-side timed-out probe test"}
```
