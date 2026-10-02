## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #175 fix, model: claude-opus-5-5)

Commit under review: `39e677e fix(funcdctl): report a dead-letter re-park only when the replay re-parked it`
(on the eventing group branch; the other commits on that branch belong to other issues and were not reviewed).

The issue: `funcdctl eventing dlq replay` wrapped every server error with "(entry re-parked)", including an
unknown id, a deleted Sensor or action, disabled dead-lettering and an invalid payload, where nothing was
re-parked. The fix moves the claim to the only place that knows it is true: `Reconciler.Replay` in
`internal/sensor/sensor.go` wraps the delivery error with "delivery failed (entry re-parked)" only after the
re-Put of the entry succeeded. The CLI (`cmd/funcdctl/eventing.go`) no longer adds its own claim. The
discard half of the issue was correctly left alone; the issue itself shows it is the documented contract.

### 🟡 Major
None.

### Minor
- **Minor 1 — the re-Put-failure branch is untested** · attribution: model · evidence: overlay mutant m2
  (`internal/sensor/sensor.go`, drop the early `return derr` in the `Put` failure branch, so a failed re-Put
  also claims "re-parked") passes all of `./cmd/funcdctl/`, `./internal/sensor/`, `./internal/controlplane/`.
  The commit message names this exact condition ("once the re-Put of the entry succeeded"), so it deserves a
  test: a `deadletter.Store` whose `Put` fails during replay, asserting the error carries no re-park claim.
- **Minor 2 — the error kind of a re-parked delivery failure is not pinned** · attribution: model · evidence:
  overlay mutant m3 (wrap with `fault.Internal` instead of `fault.KindOf(derr)`) passes the same packages. The
  regression test asserts the NotFound kind for the two non-re-park cases but only `require.Error` for the
  delivery failure; asserting `fault.Unavailable` (the stub invoker's kind) would pin that the wrap keeps the
  delivery error's kind, which ADR-0118 §3 ("returns the delivery error") relies on.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 39e677e` (clean, it is the
  branch tip), test file restored, `go test -race -run TestIssue175 ./cmd/funcdctl/` → FAIL at
  `eventing_test.go:77`: `"funcdctl eventing dlq replay: replay 01NOSUCHID failed (entry re-parked): sdk:
  deadletter.memory: dead letter "team-a"/"01NOSUCHID" not found" should not contain "re-parked"` — the
  exact message from the issue's Actual behavior.
- **Passes with the fix** under `-race`, un-skipped: `--- PASS: TestIssue175_ReplayReportsReparkOnlyOnDeliveryFailure`.
- **User-visible behavior.** The test drives the real cobra command → real SDK → real `controlplane.NewServer`
  (with `DeadLetters` + `Replayer`) → real `sensor.Reconciler` over `httptest`, and covers all three of the
  issue's cases: an unknown id (NotFound, no claim), a real delivery failure (claim present, entry re-parked
  with `Attempts` reset to 0), and a deleted Sensor (NotFound, no claim, entry's `FailedAt` unchanged). This
  is the issue's reproduction minus the daemon process, so no separate daemon probe was run.
- **Root cause, not symptom.** The claim now comes from the server, which is the only component that knows
  whether a re-park happened. A client-side guess on the error kind would have been wrong (a delivery error
  can itself be NotFound), so placing the marker in `Replay` is the correct fix. The re-park is reported only
  when `Put` succeeded; a failed re-Put returns the bare delivery error.
- **Mutant m1** (return the bare `derr` instead of the wrap, i.e. drop the marker) fails the regression test.
- **Scope.** Three files, all serving the issue: the `Replay` return path and its doc comment, the CLI's
  wrap message and `Short` help text, and the new test. No test was weakened or deleted.
- **Reuse.** The fix uses `fault.Wrapf` / `fault.KindOf`; the error detail reaches the client through the
  existing `wrapFaultError` → `fault.ToProblem` (`Detail: err.Error()`) path, so no new transport field was
  needed. The test reuses `execCLI` and `devToken` from `cmd/funcdctl/cli_test.go`; its inline server mirrors
  the existing per-test servers in `cli_test.go` and `logs_test.go` (the shared `newClient` cannot take
  `DeadLetters`/`Replayer`). `errors.Is`/`errors.As` still reach the delivery error through `Err`.
- **ADR-0118.** §3 (one synchronous attempt; re-Put with Attempts reset on failure, delivery error returned;
  a missing Sensor/action ⇒ NotFound) holds: the wrap keeps the delivery error and its kind. No ADR file
  changed in the commit.
- **Conventions.** `api/fault` errors, ctx-first, slog unchanged, no `any` in new signatures, imports at the
  top, the test's comment is a two-line why.
- **Checks** (through `nix develop -c`): `gofmt -l` on the touched packages empty; `go build ./...` ok;
  `go vet` host and `GOOS=linux` ok on `./cmd/funcdctl/... ./internal/sensor/... ./internal/controlplane/...`;
  `golangci-lint run` host and Linux target → `0 issues.`; `just check-hygiene` ok; `go test -race` on
  `cmd/funcdctl`, `internal/sensor`, `internal/controlplane/...`, `internal/eventing/...`, `pkg/sdk` → all ok;
  e2e `go test -tags e2e ./pkg/funcd/...` → `ok github.com/pyvvo/funcd/pkg/funcd 110.801s`. Lima lanes not
  run (owned by a later stage).
- **Shape.** `fix(funcdctl):` subject, `Fixes #175`, the attribution trailer, one issue per commit.

### Definition of Done
11 / 11 items hold (fix checklist). Item 4 holds on the key lines (the revert and mutant m1 fail the test);
the two surviving mutants on secondary lines are recorded as Minors 1–2.

### Model scorecard
To record: claude-opus-5-5 on issue #175 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11.

### Recommendation
Ship. Optionally, in a follow-up or rework, add a failing-`Put` replay test and assert `fault.Unavailable`
on the re-parked case so mutants m2 and m3 are caught.
