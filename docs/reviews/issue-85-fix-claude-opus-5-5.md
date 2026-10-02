## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #85 fix, model: claude-opus-5-5)

Change: branch `fix/i85`, commit 3e0a97d `fix(edge): emit the edge SERVER span that the injected traceparent names`.
Touched: `internal/edge/observ/observ.go`, `internal/edge/observ/observ_test.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **ADR-0114 wording versus the SDK's id source** · attribution: `adr` (not scored).
  ADR-0114 Decision (Trace) says to mint `trace-id` + `edge-span-id` with `crypto/rand`, "independent of
  the OTel tracer", and that the real-telemetry SERVER span reuses "that same `edge-span-id`". The OTel
  SDK gives no way to start a span with a chosen span-id short of a custom `IDGenerator`, so both sentences
  cannot hold literally. The fix keeps `crypto/rand` minting for the no-op (non-recording) path, which is
  the ADR's stated reason (judge M2), and uses the recording span's own ids when a span is emitted
  (`observ.go` `startEdgeSpan`). This honors the ADR's intent; a future ADR touching edge tracing could
  restate the rule as "crypto/rand when the tracer does not record".

Observation, not a finding (pre-existing, out of scope): the injected header always carries flags `-01`,
even when a parent-based sampler declines to record the edge span.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 3e0a97d`
  with the new test file restored: all four `TestIssue85_EdgeServerSpanEmittedWithInjectedSpanID`
  subtests fail at `"[]" should have 1 item(s), but has 0 — one edge span is emitted under real
  telemetry`, i.e. no SERVER span is recorded, exactly the issue's "OTel spans recorded = 0".
  Restored with `git reset --hard 3e0a97d`; worktree clean at that HEAD.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/edge/observ/` → `ok`.
- **Root cause fixed**: the middleware now calls `telemetry.TracerProvider()` (the issue's root cause:
  no non-test caller), starts a `SpanKindServer` span under the extracted inbound context, and injects
  that span's own trace-id and span-id, so the invocation's ParentSpanID names an emitted span. The
  second defect the issue names (trace-id adopted without a hex/lowercase check) is fixed by using the
  OTel W3C `propagation.TraceContext` extractor, which rejects non-hex and uppercase ids; both cases are
  subtests.
- **Mutants, all killed** (each restored afterwards):
  1. keep the no-op provider instead of `telemetry.TracerProvider()` → `TestIssue85_…` FAIL;
  2. always mint a random span-id (drop the `IsRecording` guard) → `TestIssue85_…` FAIL (injected
     span-id differs from the emitted span's);
  3. drop the `crypto/rand` trace-id fallback for an invalid context → `TestScenarioEdgeSpanInjectsTraceparent`
     FAIL (the no-op path still needs a valid header, ADR-0114 M2).
- **Scope**: every hunk serves the issue; the RED-metric attribute slice was hoisted only so the span
  reuses the same `function`/`namespace`/`status_class` attributes. No test weakened or deleted; the
  existing ADR-0114 scenario tests still pass.
- **Reuse**: the hand-rolled `strings.Split` traceparent parse was replaced by the OTel propagator
  already in the module (`go.opentelemetry.io/otel/propagation`) rather than a new parser; the test uses
  the existing `observability.NewFromProviders` and the `tracetest.SpanRecorder` that ADR-0114's test
  plan names. No other in-repo traceparent parser exists to reuse (`internal/workflow/dispatch.go` only
  formats one). No new dependency.
- **Conventions**: imports at top level, no `any` in signatures, no comment bloat (one doc comment on
  `startEdgeSpan` stating the why), `gofmt -l` clean.
- **ADRs**: no ADR file edited; consistent with ADR-0114 (edge SERVER span emitted under real telemetry,
  valid header under no-op) and ADR-0101 (invocation parents under the injected span).
- **Checks (touched package)**: `go vet ./internal/edge/observ/` clean; `golangci-lint run
  ./internal/edge/observ/...` → `0 issues.`; tests green with `-race`. Linux lint, e2e and lanes are
  left to the group gate per this run's scope; the issue's e2e probe was not rerun for the same reason.
- **Shape**: `fix(edge):` subject, `Fixes #85`, attribution trailer, one issue in one commit.

### Recommendation
Pass. Hand back to `/fix` Step 8.
