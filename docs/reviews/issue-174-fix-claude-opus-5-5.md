## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #174 fix, model: claude-opus-5-5)

Commit `20e1fbc` — `fix(sensor): record an Invocation for a DeadLetter replay and keep the function's error answer`
(branch `fix/204-eventing`, reviewed at `39e677e`). Touched: `internal/sensor/sensor.go`,
`internal/sensor/invoker.go`, and the two regression tests in `internal/sensor/deadletter_test.go` and
`internal/sensor/invoker_test.go`.

### 🟡 Minor 1 — the 512-byte cap on the function's answer is untested  ·  attribution: model
Evidence: overlay mutant m3 replaces `capMsg(string(answer))` with `string(answer)` in
`internal/sensor/invoker.go`; `go test ./internal/sensor/` stays `ok`. No test sends an answer longer than the
cap, so a regression that writes up to `maxErrorBody` (1 KiB) unbounded into the DeadLetter `Reason` and the
Invocation `Error` would go unnoticed. Related cosmetic point: `maxErrorBody` (1 KiB) is twice `capMsg`'s
512-byte limit, so half of what is read is always discarded; harmless, but one bound would do.
Fix (builder): add a case to `TestIssue174_InvokeErrorCarriesResponseBody` with an answer over 512 bytes and
assert the error ends with the `…` marker and stays bounded. Not blocking.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 20e1fbc` conflicts with the later
  `39e677e` (#175) in the `Replay` doc comment, so the pre-fix code was overlaid instead: `invoker.go` from
  `20e1fbc~1`, and the HEAD `sensor.go` with only the added `r.recordTerminal(ctx, d, derr)` line in `Replay`
  removed (keeping #175's change). `go test -overlay … -run TestIssue174_` fails both tests:
  `TestIssue174_ReplayRecordsInvocation` — expected 2 Failed Invocations, got 1 ("a failed replay is an action
  delivery"); `TestIssue174_InvokeErrorCarriesResponseBody` — `"sensor: function default/strict returned status
  422" does not contain "name is required"`. These are exactly the two facets of the issue.
- **Passes with the fix**, un-skipped, `go test -race -count=3 -run TestIssue174_ ./internal/sensor/` → 3/3 PASS.
- **Mutants**: m1 (record only on failure) and m2 (always record Ready) both kill
  `TestIssue174_ReplayRecordsInvocation`; m4 (`maxErrorBody = 0`) kills `TestIssue174_InvokeErrorCarriesResponseBody`.
  m3 survives (Minor 1).
- **Root cause, not symptom.** `Replay` called `deliver` directly and bypassed the recording that ADR-0118 moved
  into `attemptDelivery`. The fix records the replay's outcome through the same `recordTerminal` the retry path
  uses, from the same `delivery` unit, so a replay is one action-delivery with one Invocation (Ready or Failed).
  `HTTPInvoker.Invoke` now reads a bounded prefix of a 4xx/5xx answer into the `fault.Unavailable` error, keeping
  the status-only message when the answer is empty. No retry, timeout or error-swallowing was added.
- **ADR conformance.** ADR-0118 Decision §2 ("one per action-delivery", recorded at the terminal outcome) and §3
  (replay is exactly one synchronous attempt, Delete on success, re-Put with Attempts reset on failure) both
  hold: the replay still does not re-enter the async loop, and the Delete / re-Put semantics are unchanged. The
  ADR-0023 "never silently dropped" line gets stronger. The issue's provenance facet (Invocation carrying
  Sensor/action) was correctly left alone, as the issue itself rules it out of scope. No ADR file was edited.
- **Scope.** Every hunk serves #174: the `Replay` recording line plus its doc comment, the bounded answer read
  in `Invoke` plus its doc comment, and the two regression tests. No test was weakened or deleted.
- **Reuse.** Reuses `recordTerminal`, the package's existing `capMsg`, the existing `dlqHarness` /
  `scriptedInvoker` / `invocationsByPhase` / `readyEndpoints` test helpers, and `io.LimitReader` from the
  standard library (the same bounded-read idiom as `internal/runtime/provision`). No new helper or dependency.
- **Conventions.** `api/fault` errors, ctx-first, slog via the reconciler logger, top-level imports, one short
  *why* comment on the new constant, no comment bloat.
- **Checks.** `gofmt -l internal/sensor` clean; `go build ./...` OK; `go vet` on `internal/sensor`,
  `internal/controlplane`, `cmd/funcdctl` OK (host) and `GOOS=linux go vet ./internal/sensor/...` OK;
  `golangci-lint run ./internal/sensor/...` 0 issues (host and `GOOS=linux`); `go test -race` on
  `internal/sensor/...`, `internal/controlplane/...`, `cmd/funcdctl/...` all `ok`;
  `go test -tags e2e ./pkg/funcd/...` `ok` (112 s, covers `deadletter_e2e_test.go`); `just check-hygiene` clean.
  No Lima lane was run (owned by a later stage).
- **Shape.** Conventional `fix(sensor):` subject, `Fixes #174`, the attribution trailer, one issue per commit.

### Definition of Done
11 / 11 items hold (the fix checklist). Item 4 holds for the key lines (revert and 3 of 4 mutants fail a test);
the surviving cap mutant is Minor 1 (model).

### Model scorecard
Ledger fields for claude-opus-5-5 on issue #174 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.
Recording is left to the batch's later stage.

### Recommendation
Sign off. Optionally add the over-cap answer case to the invoker regression test before the group PR; it does
not block.
