## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0156 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0156-sensor-delivery-isolation`, one commit (`e92bd4ae`), 13 files, +1367/−113
(`git diff origin/main...HEAD`). Reviewed against ADR-0156 Decisions 1–10, Contracts, Scenarios, Implementation
plan and Review checklist. All commands ran in the worktree through `scripts/agent/d`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| Build (host) | `go build ./...` | exit 0 |
| Build (Linux) | `GOOS=linux go build ./...` | exit 0 |
| Vet (host, Linux) | `go vet` on `internal/sensor`, `internal/platform/config`, `pkg/funcd`, `cmd/funcd`, `internal/eventing/deadletter` | exit 0, exit 0 |
| Lint (host) | `go tool golangci-lint run` on the same packages | `0 issues.` exit 0 |
| Lint (Linux) | the host-built linter binary run with `GOOS=linux` on the same packages | `0 issues.` exit 0 |
| gofmt | `gofmt -l` on the changed `.go` files | no output |
| Tests, touched packages | `go test -race -count=1 ./internal/sensor/... ./internal/platform/config/... ./internal/eventing/deadletter/... ./pkg/funcd/ ./cmd/funcd/` | all `ok` |
| Scenario and named tests ×3 | `go test -race -count=3 -run 'TestScenario\|TestRetryQueue\|TestIssue347\|TestIssue145\|TestActionFails\|TestTransient\|TestIssue174\|TestWithSensorDelivery\|TestIssue341'` on the 4 packages | all `ok` (sensor 16.3 s) |

The first Linux lint attempt (`GOOS=linux go tool golangci-lint`) failed with `exec format error`, because `go tool`
then builds the linter itself for Linux. This is a tooling quirk (env), not a finding; the rerun with the host binary is
the result above.

**Overlay mutants (`go test -overlay`, all 3 killed):**

| # | Mutated line | Killed by |
|---|---|---|
| m1 | `retry.go` `offer`: per-target cap check `l.inFlight < q.maxInFlightPerTarget` → `l.inFlight >= 0` | `TestScenarioDeliverySizesFromSettings` (`"5" is not less than or equal to "2"`), `TestScenarioStuckTargetHoldsAtMostCap` (`Condition never satisfied`) |
| m2 | `sensor.go` `attemptDelivery`: `if stale {` → `if false && stale {` (a stale in-flight failure is parked with its own error instead of the sensor-changed Reason) | `TestScenarioSensorEditReleasesBacklog` (`expected: 4100, actual: 4097`) |
| m3 | `retry.go` `enqueue`: a late enqueue before the deadline is counted dropped instead of returning the shutdown Reason | `TestScenarioShutdownParksQueued` (`should have 11 item(s), but has 10`) |

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Ragged doc-comment reflow in `internal/eventing/deadletter/deadletter.go`** · attribution: model · evidence:
  `deadletter.go:4` is the lone token `// BUS-DRIVER-INDEPENDENT` between two full lines, and the `DeadLetter` doc
  (`:23-24`) breaks after `It carries the full`. The edit inserted the ADR-0156 clause without refilling the
  paragraph. The content is correct (Implementation plan item 3 asks for these doc updates). Fix: reflow the two
  paragraphs. Cosmetic only.

### ✅ Verified correct (keep it)
- **Contracts match the ADR exactly**: `newRetryQueue(base, maxDelay, maxInFlightPerTarget, maxQueuedPerSensor)`,
  `actionTarget{ns, kind, name}`, `unitState` (4 values), `unit{d, attempts, target, slot, state, stale}`,
  `pending{id, d, attempts, withdrawn}`, `errDropped` text, `enqueue`/`takeDropped`/`get`/`reschedule (ok, stale)`/
  `ready`/`forget`/`withdraw`/`shutDown(deadline) []pending`. The three park Reasons are verbatim
  (`retry.go:140-150`). `retryWorkers` is gone (grep: no hit). `NewReconciler`, `Replay`, `RunRetryWorkers(ctx, drain)`
  are unchanged in signature.
- **Decision 1 (enqueue-first)**: `runAction` (`sensor.go:255-265`) only enqueues when `DeadLetters` is set, parks a
  refused delivery at once with `Attempts` 0, and logs `errDropped`; the `DeadLetters == nil` path keeps its inline
  attempt (Scope Out).
- **Decision 3 (one state per unit)**: one `units` map, every transition under `q.mu`; `ready` acts only on a
  backing-off unit (`retry.go:303-312`); the call that ends an attempt frees the slot in the same hold (`reschedule`,
  `forget` → `release`). `TestRetryQueueOneStatePerUnit` covers all seven plan-item-4 cases.
- **Decision 4 (ring and cap)**: a lane joins the ring only with a queued id and a free slot, at most once (`inRing`);
  `take` skips ids no longer in the wanted state and re-offers the lane; the withdrawn FIFO is the zero `actionTarget`
  with the same cap. A capped target holds no worker. Mutant m1 confirms the cap is tested.
- **Decision 5 (bound and park)**: the per-Sensor id set is joined at `enqueue` and left once at `forget`, `withdraw`
  or `shutDown`. A single `park` (`sensor.go:285-290`) writes the DLQ entry, the Failed Invocation and the warn log on
  `context.WithoutCancel(ctx)`, so every park path (publisher overflow, worker exhaustion, refused reschedule,
  withdrawn unit, shutdown park) uses it.
- **Decision 7 (shutdown)**: `shutDown(now + drain)` returns every unit that is not in flight; `parkQueued` parks
  until the deadline and counts the rest; `enqueue` returns the shutdown Reason before the deadline and counts a drop
  after it; `takeDropped` reads and marks in one hold; one warn log carries the count (`count=7` asserted); a later
  refusal logs its own line. An in-flight failure at shutdown is parked with its own error, and a stale one gets the
  sensor-changed Reason (stale is checked before shutdown in `reschedule`).
- **Decision 8 (withdraw)**: `Reconcile` calls `withdraw` between the cancels and `r.subscribe` in one `r.mu` hold;
  `cancelAll` calls it under `r.mu` (delete and static defect select all units). A new UID selects all units; a new
  generation keeps only the actions that are unchanged in every field (`keepActions`, `reflect.DeepEqual` over
  `Spec.Do`). Lock order is `r.mu` → `q.mu`; the queue never takes `r.mu`.
- **Decision 10 (settings)**: three fields in the contract order with the exact tags; `defaults()` sets 32/4/4096;
  `WithSensorDelivery` applies the same checks and returns `fault.Invalid`; `cmd/funcd` passes the values after
  `WithDeadLetterQueue`; `pkg/funcd` copies them into `sensor.Deps`; `examples/funcdconfig.yaml` lists the 3 keys
  after `deliveryAttempts` with `default: N` (`TestIssue341_ExampleDocumentsEveryKeyWithDefault` passes).
- **Scenarios, 10/10, each a named, un-skipped, passing test** that asserts what the scenario says:
  `TestScenarioStuckTargetSparesTimer` and `…SparesBlobSource` run real `Source.Run` / `BlobWatcher.Run`, `Fanout`,
  reconciler and `RunRetryWorkers`, and the blob source is registered only through `Source.Reconcile` with
  `NewMemWatermark()`, as plan item 4 requires. `…SensorEditReleasesBacklog` builds the exact 4096-unit state (4 hung,
  one on attempt 3; 3 backing off; 4088 waiting; `b` backing off; S2's 6) and checks every count in the scenario,
  through to the replay reaching G. `TestScenarioInvalidDeliverySettingRefused` (config) checks all four refusals
  by key and value.
- **Existing tests**: `TestActionFailsThenDeadLettered`, `TestTransientThenSucceeds`, `TestIssue145_*`,
  `TestIssue347_ShutdownDrainStopsAtBound` and `sensor_test.go` are unmodified and pass. `TestIssue347_ShutdownStartsNoDueUnit`
  has the new signatures and the same assertion. The only other test edit (`TestIssue174_ReplayRecordsInvocation`,
  `require.Len` → `require.Eventually`) follows from attempt 1 now running asynchronously, and that test is not on the
  plan's "unmodified" list.
- **Scope held**: no change to `go.mod`, OpenAPI or `internal/eventing` code (doc comments in `deadletter.go` only);
  the ADR file and every other doc are untouched. Conventions hold: ctx-first, `fault` kinds, slog only, no `panic`, no
  narration comments, `t.Parallel()` everywhere except the env-setting config test.

### Definition of Done
14 / 14 hold: Review checklist (`retryWorkers` removed; every park uses `WithoutCancel`; `takeDropped` in one hold;
field order; `withdraw` under `r.mu` in `Reconcile` and `cancelAll`; every scenario has its named test; existing tests
as in plan item 4), Contracts signatures, plan item 6 scope (no `go.mod`/OpenAPI/eventing-code change; only the three
config keys), build/vet/lint on both platforms, touched-package tests under `-race`, ADR substance unchanged, and the
overlay mutants (3/3 killed). As this campaign specifies, the status moves (`Accepted → Reviewing → Implemented`, feat
row F85, ADR-0118/0109 back-links) are left to the wave's docs PR. This review neither stamps them nor counts them.

### Model scorecard
Ledger row below: claude-opus-5-5 on ADR-0156 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 14/14.
For the wave's ledger PR to record (it is not written to `docs/reviews/` here).

### Recommendation
Ready to merge. The one Minor (a doc-comment reflow) can be fixed in the same PR or left. The wave's docs PR then
stamps ADR-0156 `Implemented`, moves the F85 row, adds the ADR-0118/0109 back-links, and the PR carries `Fixes #35`.

```json
{
  "date": "2026-10-05",
  "adr": "0156",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 14,
  "dod_total": 14,
  "report": "docs/reviews/adr-0156-implementation-claude-opus-5-5.md",
  "notes": "all contracts verbatim, 10/10 scenario tests pass (-race, count=3), build/vet/lint green on host and Linux, 3/3 overlay mutants killed (per-target cap, stale-park reason, pre-deadline shutdown park); deadletter.go doc-comment reflow left ragged lines (model); Linux lint via go tool needs the host-built binary (env, not scored)"
}
```
