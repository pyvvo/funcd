## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #134 fix, model: claude-opus-5-5)

Change: branch `fix/i134`, commit `4c466b6 fix(workernode): wrap a context.invoke input in a CloudEvent envelope`
(`git diff origin/main...HEAD`: `internal/dataplane/dataplane.go`, `internal/dataplane/normalize.go`,
`internal/workernode/local/invoker.go`, `internal/workernode/local/local_test.go`).

### 🟡 Major / Minor
- **Minor — dense call on one line** · attribution: model · `internal/workernode/local/invoker.go:36` now nests
  `dataplane.InvokeEnvelope(target.Namespace, target.Function, input)` inside `bytes.NewReader(...)` inside
  `httptest.NewRequest(...)` on one long line. Lint passes; it is only harder to read. Fix: bind the envelope to a
  local (`body := dataplane.InvokeEnvelope(...)`) first. Non-blocking.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 4c466b6`, keeping the
  new test file, then `go test -race -run TestIssue134 ./internal/workernode/local/` → `--- FAIL:
  TestIssue134_InvokeWrapsInputInCloudEventEnvelope` with `input {"name":"plain"} must arrive as a CloudEvent
  envelope, got {"name":"plain"}` — the exact raw-forwarding symptom the issue reports. `git reset --hard 4c466b6`
  → worktree clean.
- **Passes with the fix under `-race`**: `--- PASS: TestIssue134_InvokeWrapsInputInCloudEventEnvelope`,
  `ok internal/workernode/local`. It covers the three issue cases: a plain object (`event.data` = input, `source`
  = `funcd://team-a/function/echo`), a non-object `[1,2,3]` (previously a 400, now wrapped), and an
  envelope-shaped `{"data":…}` input that passes through unchanged (back-compat for the example shape).
- **Mutants — 3/3 killed** (each applied, run with `-run 'TestIssue134|TestScenario'`, restored):
  1. `InvokeEnvelope` ignores the envelope (`ok && false`) → fails `TestIssue134_…`, `TestScenarioInternalInvokeUntouched`,
     `TestScenarioEmptyBodyForwardsNullData`.
  2. Broker passes `"default"` instead of `target.Namespace` → fails `TestIssue134_…` (source assertion).
  3. Drop the `"data"` passthrough branch in `normalizeInvokeBody` → fails `TestIssue134_…` and
     `TestScenarioCloudEventPassthrough`.
- **Root cause, not symptom.** The issue's cause is that the fn-to-fn broker forwards the raw input as internal
  traffic and the edge skips normalization for internal traffic on the premise that internal producers already
  emit an envelope. The fix makes that premise true at the producer (`proxyInvoker.Invoke`), so the shim gets an
  envelope for both Node and Python callers without a shim change. No timeout, retry or swallowed error.
- **ADRs conform.** ADR-0134 (Accepted/Implemented) requires that the edge never normalizes internal traffic and
  that internal producers emit a v1.0 envelope; the edge's `!internal` gate is unchanged, and `TestScenarioInternalInvokeUntouched`
  still passes. ADR-0064's `invoke(alias, input)` contract now holds (`event.data` is the input). No ADR file is
  in the diff.
- **Reuse, no duplication.** The new `dataplane.InvokeEnvelope` is a thin exported wrapper over the existing
  ADR-0134 `normalizeInvokeBody` + `newInvokeID`, and the edge (`serveFunction`) was switched to call the same
  helper, so there is one envelope rule for both paths. `internal/eventing/cloudevent.go` builds sensor events
  with a different source/type and no passthrough rule, so it is not the right helper here. The
  `workernode/local → dataplane` import already existed (`dataplane.WithInternal`).
- **Scope.** Every hunk serves the issue: the broker wrap, the helper, the edge's switch to the shared helper
  (behavior identical), the doc-comment update and the test. No test weakened or deleted.
- **Conventions.** Typed `v1.NamespaceName`/`v1.ObjectName` parameters, no `any`, top-level imports, comments
  explain the why (ADR-0134/0064 references) without narration.
- **Checks (touched packages).** `gofmt -l` clean; `go build ./...` ok; `go test -race -count=1
  ./internal/dataplane/... ./internal/workernode/local/...` → both `ok`; `go vet` ok; `golangci-lint run` on
  both packages → `0 issues.` (e2e, Linux lint and lanes are deferred to the group gate by design.)
- **Shape.** Subject `fix(workernode): …`, body has `Fixes #134`, names the regression test, and carries the
  attribution trailer; one issue in one commit.
- **Observation (not a finding).** An input object whose top-level keys include `data` or `specversion` is
  treated as an envelope and passed through, so the callee sees `event.data = input.data`. This is the
  ADR-0134 rule the edge already applies to external invokes, kept on purpose for back-compat; any change
  belongs in an ADR, not this fix.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 was verified for the touched packages on the host; e2e, Linux lint and
the Lima lane run in the group gate. Misses: none.

### Model scorecard
Ledger fields (not recorded by this gate): claude-opus-5-5 on issue #134 (fix) → pass, 0/0/1, 1 model-attributed,
DoD 11/11.

### Recommendation
Pass. Optionally split the long line in `invoker.go:36`; otherwise hand back to `/fix` Step 8 to open the PR.
