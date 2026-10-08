## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0197 implementation, model: claude-opus-5-5)

Scope: the four commits after 88d50ba4 on `feat/adr-0197-log-durations-in-ms` (`6937cd0a`, `f9c07189`, `894ef401`,
`a28f1267`). ADR-0196's commits below 88d50ba4 were reviewed separately.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The embedder scenario drives the handler, not a funcd log call** · attribution: model ·
  `pkg/funcd/normalizelog_internal_test.go:24-26` builds a `slog.Record` and calls `p.logger.Handler().Handle`
  instead of logging through `p.logger` as the Implementation plan step 7 says ("logs through `p.logger`"). It still
  tests the wrapped handler that funcd's components receive, and it pins a UTC+2 record time, which a `Logger.Warn` call
  could not do, so the assertions hold. Optional: add one `p.logger.Warn("probe", "timeout", 60*time.Second)` line
  checking only `"timeout_ms":60000`, so the slog front end is covered too.

### ✅ Verified correct (keep it)

- **Build, vet, lint (darwin and linux), all exit 0.** `go build ./...` = 0; `go vet` on the five touched packages
  = 0 (darwin and `GOOS=linux`); `golangci-lint run` on the five packages = `0 issues.` exit 0 (darwin and
  `GOOS=linux`). `cmd/funcdctl`'s changed files carry `//go:build dev`, so they were checked again with the tag
  (`justfile:37`, `_dev-packages`): `go build -tags dev ./cmd/funcdctl` = 0, `go vet -tags dev` = 0 (darwin and
  linux), `golangci-lint run --build-tags dev ./cmd/funcdctl` = `0 issues.` (darwin and linux).
- **Tests with `-race -count=1`, all ok**: `internal/platform/observability` 3.6 s, `internal/function` 11.3 s,
  `internal/edge/observ` 1.9 s, `pkg/funcd` 15.6 s, `cmd/funcdctl` (`-tags dev`) 22.5 s.
  `TestIssue428_DevReloadsEditedWorkflow` reports `--- PASS` under `-tags dev` with node on PATH (not skipped).
- **All seven Scenarios have a named, passing test**:
  configured-timeout-whole-ms → `TestScenarioConfiguredTimeoutWholeMs` (`internal/function`, a 60 s
  `newBootBackoff` over a JSON `NewLogger`, then `timedOut`); measured-latency-keeps-fraction →
  `TestScenarioMeasuredLatencyKeepsFraction` (also decodes to `float64`); text-format-same-key →
  `TestScenarioTextFormatSameKey`; edge-access-log-keeps-fraction → `TestScenarioEdgeAccessLogKeepsFraction`
  (100 µs handler, own 900 µs bound, host-load failure after 100 tries, `0.1 ≤ v < 1`, at most three decimals);
  no-nanoseconds-in-logs → `TestScenarioNoNanosecondsInLogs` (all nine paths × JSON, text, and a
  `NormalizeHandler` over plain JSON and text handlers); funcdctl-dev-logs-like-daemon →
  `TestIssue428_DevReloadsEditedWorkflow` (anchored regex on the exact `NewLogger` text line, read from the cli's
  `out`, no `slog.SetDefault`); embedder-opts-in → `TestScenarioEmbedderOptsIn` (with and without the option). Plus
  `TestMillis` (the Decision table, `math.MinInt64`, extra cases) and `TestDurationKeyAlreadyMs`.
- **Mutants, each killed by a test** (run with `go test -overlay`, the work not edited):
  1. `millis` without `Round(time.Microsecond)` → `TestMillis`, `TestScenarioTextFormatSameKey`,
     `TestScenarioNoNanosecondsInLogs` fail.
  2. `durationField` without the `v1alpha1.Duration` case → `TestScenarioNoNanosecondsInLogs` fails.
  3. `normalizeAttr` without the group rebuild → `TestScenarioNoNanosecondsInLogs` fails (`"g":{"wait":1234567891}`).
  4. `observ.go:120` back to `"duration_ms", dur.Milliseconds()` → `TestScenarioEdgeAccessLogKeepsFraction` fails
     (`0` is not ≥ `0.1`).
- **Contracts**: `durationField`, `durationAttr`, `millis` in `internal/platform/observability/logger.go` match the
  ADR's Contracts block line for line; the duration branch sits after ADR-0196's time branch in the one exported
  `ReplaceAttr`, which `NewLogger` (`logger.go:89`) and the audit recorder (`audit.go:53`) both set. `normalize.go`
  holds `NormalizeHandler`/`NewNormalizeHandler` as contracted: Handle rebuilds the record with UTC time truncated to
  the millisecond (zero time kept), attributes resolved and passed through `durationField`, groups rebuilt; WithAttrs
  normalizes; Enabled and WithGroup forward.
- **Decision 5**: `observ.go:120` logs `"duration", dur`; `TestScenarioAccessLogCorrelated` now uses a JSON
  `NewLogger`, so its `"duration_ms"` assertion still holds.
- **Decision 6**: `startDev` builds a text `NewLogger` over `a.out`, kept on `devInstance.logger` and passed to the six
  former `slog.Default()` sites; no `slog.Default()`/`slog.SetDefault` remains in `cmd/funcdctl` production code
  (grep empty).
- **Decision 7 / Contracts**: `WithNormalizedLogFields` is off by default, wraps only a `WithLogger` logger
  (`funcd.go:420-422`, before the default-logger branch), and `pkg/funcd` exports no hook. The `WithLogger` doc comment
  matches the Contracts.
- **No other handler rewrites durations**: the only non-test `slog.New{JSON,Text}Handler` sites are `logger.go` and
  `audit.go`, both with `ReplaceAttr`.
- **Tracking**: the ADR diff is only `Accepted (2026-10-07)` → `Reviewing (2026-10-08)`, substance unchanged; the
  F17a row reads `log durations: reviewing`; the blueprint already carries Decision 8's "Field encodings" bullet
  (`blueprint.md:450`); no config key added; working tree clean; commits authored as the project identity.

### Definition of Done

5 / 5 hold (Review checklist 4 + the ADR's Definition of done 1). The "failed on main" half of the DoD is shown by
construction (the tests use the new `NewNormalizeHandler`/`WithNormalizedLogFields`, and the four mutants above show
the assertions fail without the change). Generic DoD: build/vet/lint/race tests green on the touched packages, no
stubs, no skipped scenario.

### Model scorecard

To record: claude-opus-5-5 on ADR-0197 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 5/5. Not written to
`docs/reviews/` by this gate run (the orchestrator records the batch's ledger rows in one PR).

### Recommendation

Sign off: stamp ADR-0197 `Reviewing → Implemented` and the F17a "log durations" part → `implemented`. The Minor is
optional polish for the builder; nothing loops back to a superseding ADR.

```json
{"adr":"0197","phase":"implementation","model":"claude-opus-5-5","verdict":"pass","blockers":0,"majors":0,"minors":1,"model_attributed":1,"dod_passed":5,"dod_total":5,"report":"docs/reviews/adr-0197-implementation-claude-opus-5-5.md","notes":"pass; build/vet/lint darwin+linux (incl. -tags dev) and -race tests on 5 pkgs green; 7/7 scenarios; 4 mutants killed; minor(model): embedder scenario calls Handler().Handle instead of logging through p.logger"}
```
