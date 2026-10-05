# Fix review — issue #721 (DLQ replay takes no claim: concurrent replays double-deliver, discard is undone)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #721 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i721`, one commit `8d6fefe2` `fix(sensor): make a dead-letter replay hold its entry until it
deletes or re-parks it`, based on the current `origin/main` (merge base = `origin/main` tip `a394c6f1`).

The fix has two parts, matching the issue's two cases:

- `Reconciler.Replay` (`internal/sensor/sensor.go`) claims `(namespace, id)` in a `replaying` set under `r.mu` for its
  whole run; a second replay of an id in flight returns `fault.Conflict` without delivering.
- The failure-path re-park uses a new `deadletter.Store.Update` that replaces only an existing record (memory: under
  the store lock; Badger: `Get` + `Set` in one transaction, a concurrent delete surfaces as `ErrConflict`). A
  `NotFound` from the re-park is the expected "discarded or swept meanwhile" outcome and is not logged as a warning.
  The shared store contract (`internal/eventing/deadletter/contract.go`) gains an `update-replaces-only-an-existing-record`
  case that both drivers run; the spec-generation stub in `internal/controlplane/deadletters.go` gains the method.

### Proof first (the person's rule) — the test fails on current `origin/main`

Overlay of the `origin/main` version of all six changed non-test files (`git show origin/main:<file>`, `go test
-overlay`), regression test kept, `-race`:

```text
Error: Not equal: expected: 1 actual: 2   — one dead letter must reach the target once
Error: Should be zero, but was 1          — a dead letter removed during a failing replay must stay removed  (x2)
--- FAIL: TestIssue721_ReplayClaimsDeadLetter/concurrent-replays-deliver-once
--- FAIL: TestIssue721_ReplayClaimsDeadLetter/discard-during-failing-replay-stays-discarded
--- FAIL: TestIssue721_ReplayClaimsDeadLetter/sweep-during-failing-replay-stays-evicted
```

Each case fails for the issue's stated reason (two target calls; the entry is resurrected by the stale re-`Put`),
and each case has its own subtest — including the retention-sweep case the issue only inferred, now proven. With the
fix: all three subtests pass under `-race -count=3`. The worktree was left clean.

### Minor 1 — the dead-letter Store port grew beyond ADR-0118's listed interface  ·  attribution: adr

ADR-0118 (Implemented) Contracts list `Store` as `Put`/`List`/`Get`/`Delete` + the sweep, and its Decision 3 says a
failed replay "re-`Put`s" the entry. The fix adds `Update` and re-parks through it. This is additive and conditional
only on the record still existing, so it keeps every stated guarantee (the entry is never lost while it exists) and is
what makes the ADR's own `discard-removes` scenario hold; the issue's root-cause section reached the same conclusion
and the ADR file was not edited. Recorded so the next ADR that touches the DLQ port lists `Update`; not scored.

### ✅ Verified correct (keep it)

- **Cause, not symptom**: the unguarded read-deliver-write sequence is closed at both points the issue names — the
  in-flight claim serializes replays of one id, the conditional write stops the stale re-park. No timeout, retry or
  swallowed error was added; a real re-park failure other than `NotFound` is still logged and the delivery error returned.
- **Mutants** (overlay, only the relevant tests run), each fails a test:
  1. claim never held (`if false`) → `concurrent-replays-deliver-once` fails;
  2. `Replay` re-parks with `Put` instead of `Update` → both eviction subtests fail;
  3. memory `Update` creates an absent record → `TestMemoryContract/update-replaces-only-an-existing-record` and both
     eviction subtests fail;
  4. Badger `Update` skips the existence `Get` → `TestBadgerContract/update-replaces-only-an-existing-record` fails.
- **Checks** (touched packages): `go build ./...` ok; `go test -race` ok for `internal/sensor/...`,
  `internal/eventing/deadletter/...`, `internal/controlplane/...`; `go vet` ok on those plus `cmd/funcdctl` and
  `pkg/funcd` (the other users of `deadletter.Store` compile, test files included); golangci-lint 0 issues.
- **Scope**: every hunk serves the issue; the controlplane stub change is required by the port change. No test was
  weakened or deleted. The `Put` doc comment drops the now-false "replace is how replay re-parks" clause.
- **Reuse**: the test reuses `dlqHarness`, `createSensor`, `dep`, `reqOf`, `fire`, `dlqList`; the new contract case
  sits in the existing shared suite. The repo has no shared keyed-claim helper (the activator keeps its own inline
  in-flight map), so the small `claimReplay` is not a duplicate. Errors use `api/fault` (`Conflictf`, `NotFoundf`,
  `Wrapf`), consistent with the drivers' existing `op` and messages.
- **Conventions**: ADR-0002 ports/drivers and the shared contract suite, ctx-first, slog, no `any`; comments state the
  why (the `NotFound` case, the transaction's conflict semantics), no narration.
- **Siblings**: the only other DLQ write in the sensor is the initial `Put` on exhaustion (`sensor.go` ~355), which
  creates a new record and has no stale-copy race; `Delete` on replay success is idempotent. The workflow `Replay` is a
  different mechanism (run records), not this construct.
- **Shape**: `fix(sensor):` subject, `Fixes #721`, attribution trailer, one issue in one commit.

### Definition of Done

12 / 12 applicable items hold (the Linux lint, e2e and lanes are left to the group gate, as instructed).

### Model scorecard

claude-opus-5-5 · issue #721 · fix · pass · 0 blockers · 0 majors · 1 minor (adr) · 0 model-attributed · DoD 12/12.

### Recommendation

Pass — hand back to `/fix` Step 8 for integration. Whoever next writes an ADR on the dead-letter queue should list
`Update` in the `Store` port contract.
