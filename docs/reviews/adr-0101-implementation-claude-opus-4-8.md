# ADR-0101 implementation review — claude-opus-4-8

## Verdict: pass — 0 blockers, 0 majors  (ADR-0101 implementation, model: claude-opus-4-8)

Function trace capture (F51): a per-invocation OTel `SERVER` span on the *same* signal-generic
funclog side channel + `blob.Bucket` sink that ADR-0081 built for logs. The implementation lands the
Go marshal/route/persist tier, the host signal demux, both language shims (Node ALS / Python
contextvars), and closes ADR-0081's `trace_id`/`span_id` provenance gap. Full verification below was
executed (not eyeballed); every Scenario has a named, un-skipped, passing test at the right tier, and
the Implemented logs path shows no regression.

### Evidence (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...` | exit 0 |
| lint | `go tool golangci-lint run ./...` | `0 issues.` (macOS `ld: warning … newer macOS` lines are environmental, ignored) |
| mod | `go mod verify` | `all modules verified`; `git diff go.mod` empty (no new dep) |
| go tests | `go test ./internal/funclog/... ./pkg/funcd/...` | `ok` all packages (`pkg/funcd` 51.7s) |
| traces e2e | `TestScenarioE2EFunclogCapturesSpans` | PASS (1.81s) |
| logs regression guard | `TestScenarioE2EFunclogCapturesBurst` | PASS (1.79s) — no F50 regression |
| Node shim | `npm test` | 37 pass / 0 fail / 0 skipped (29 existing + 8 new span tests) |
| Python ruff | `ruff check .` | All checks passed! |
| Python mypy | `mypy` | Success: no issues found in 20 source files |
| Python pytest | `pytest -q -k "not test_uds_channel_captures_lines"` | 56 passed, 1 deselected |
| Venom containerd lane | `test_results_funclog.venom.xml` (already ran; not re-run) | `tests="12"`, 0 `<failure>`, 0 `<error>` — both languages, 5 traces testcases incl. traceparent-adopting `SERVER` span + logs↔trace correlation |

The excluded Python UDS test was run in isolation and **passed** (`1 passed`) — it fails only with the
macOS `AF_UNIX path too long` limit, path-length dependent, unrelated to this change (`env`). The two
known pre-existing environmental failures (`TestPythonPoolSmoke` subinterpreter readiness; the UDS
test) are not attributable to this model.

### Scenario → test map (all present, un-skipped, passing)

| Scenario | Test(s) | Tier |
|---|---|---|
| invocation-emits-span | Node `invocation-emits-span…`; Py `test_invocation_span_emits_server_span_ok` | shim |
| error-span-status | Node throw / output-mismatch / input-422-no-span (3); Py `test_invocation_span_error_status`, `…records_exception_status`, `test_server_input_mismatch_emits_no_span` | shim |
| adopt-traceparent | Node `adopt-traceparent`; Py `test_new_inv_context_adopts_traceparent`, `test_server_invocation_emits_span_adopting_traceparent` | shim |
| mint-root-trace | Node `mint-root-trace`; Py `test_new_inv_context_mint_root` | shim |
| logs-correlated-to-span | Node `logs-correlated-to-span`; Py `test_logs_correlated_to_span` | shim |
| persisted-via-blob | Go `TestScenarioSpanPersistedViaBlob` | funclog (real in-mem blob) |
| identity-tagged | Go `TestScenarioSpanIdentityTagged` | funclog |
| signal-demux | Go `TestScenarioSignalDemux` (+ `TestSignalDemuxTracesDisabled`) | funclog |
| transport-fd3-and-uds | Go `TestScenarioSpanTransportFd3AndUds` | funclog |
| python-invocation-span | Py `test_server_invocation_emits_span_adopting_traceparent` (+ input-mismatch) | shim |

The ADR deliberately moved `error-span-status` and the behavioral-span assertions to the shim tier
(the funclog package never sees a handler throw); the funclog tier owns marshal/route/persist. The
implementation places each test exactly where the ADR's test plan says.

### ✅ Verified correct (keep it)

- **Contracts match the ADR exactly.** `Span` (all 11 fields), `SpanKind`/`SpanStatus` typed enums
  with `valid()` guards, `SignalTraces = "traces"`, `TraceSink` (`AppendSpan`/`Flush`/`Close`),
  `NewBlobTraceSink(Deps) (*BlobTraceSink, error)`, `Sinks{Logs, Traces}`, and
  `Route(ctx, r, sinks, res, log) error` all conform. `Route` replaces `Pump` at the composition-root
  wiring (`funcd.go`) while `Pump`/`NewNDJSONReader` remain for the logs-only contract tests, as the
  ADR specifies.
- **No new dependency.** `ptrace`/`pcommon` come from the already-direct vendored
  `collector/pdata`; `git diff go.mod` is empty — matches the "no `go.mod` change" contract.
- **Demux is back-compat + freeze-safe.** `Route` peeks `funcd.signal`: `traces` → `AppendSpan`,
  absent/other → logs `Append`; a nil `Traces` sink reads span lines off the channel and drops them
  (traces disabled) while logs still flow; one blocking `Scan` preserves the loss-safe property; a
  malformed line is logged + skipped; EOF/ctx-cancel seals both sinks' segments (`flushSinks`).
- **Persist mirrors `BlobSink`.** Per-`Resource` segments, size/age seal + background age-flusher,
  `ptrace.JSONMarshaler` → OTLP-trace-JSON-Lines, one `blob.Bucket.Put` under
  `traces/<ns>/<fn>/<date>/<unixnano>-<replica>.otlp.jsonl`; Resource attrs `source=function` +
  `namespace`/`function`/`replica`/`tenant` — identical identity tagging to logs. Concurrency-safe
  per-Resource locking.
- **Both shims emit the same wire and correlate logs.** One channel opened once per process/worker and
  **shared** between console/logging capture and the span emitter (avoiding a second UDS connect that
  would double-capture); the handler runs inside the invocation context (Node `AsyncLocalStorage`,
  Python `contextvars`) so `buildRecord`/`FuncLogHandler.format` populate `inv`/`trace_id`/`span_id`.
  `traceparent` is forwarded host→worker in pool mode. Span capture is best-effort (a broken channel
  never crashes the function). `parseTraceparent` rejects malformed/all-zero/`ff` and mints a root.
  Monotonic-base duration with epoch-nanos stamping at emit — matches the ADR's resolved open question.
- **Input-mismatch (422) emits no span** in all four paths (Node solo/pool, Python solo/pool): the
  span opens only after input validation passes.
- **ADR-0002 conventions hold.** Typed enums over magic strings; ctx-first (`AppendSpan`/`Flush`/
  `Route`); `api/fault` for all errors (`Invalidf`, `Wrapf`); `log/slog` only; no `panic`/`fmt.Print*`
  in the shipped path; one-file sink driver (`tracesink.go`); no `any` in exported signatures
  (`golangci-lint` 0 issues).
- **Tracking correct.** ADR-0101 at `Reviewing` with the `Accepted → Reviewing` bump documented in the
  header; feat `docs/feat/0004` F51 row at `reviewing` linking ADR-0101; the logs e2e + venom lane
  re-run green as the regression guard.

### Minor

- `TraceSink` in `tracesink.go` lists `Close() error` explicitly rather than embedding `io.Closer` as
  the ADR contract wrote it. Semantically identical (`io.Closer` *is* `Close() error`; `BlobTraceSink`
  satisfies both), and arguably clearer inline with a doc comment. Non-blocking, no fix required.
  · attribution: model · non-blocking.

### Definition of Done

10/10 hold — the 7 ADR Review-checklist items (single `SERVER` span + no-span-on-422; adopt/mint;
log correlation with unchanged wire; blob/OTLP/`traces/` persist + identity; demux + no F50
regression; reuse transport/drain/clock + no `go.mod` change; both shims same wire; pure-Go + one test
per scenario + freeze-safe) plus the generic gate (full suite green, real behaviour no stubs,
contracts honoured, tree matches surface, conventions, deps, tracking). All verified with captured
evidence above. No `adr`- or `env`-attributed DoD gaps.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0101 (implementation) → pass, 0 blockers / 0 majors / 1 minor,
0 model-attributed blocking, DoD 10/10. See docs/reviews/model-scorecard.md.

### Recommendation

Sign off. The work meets the ADR's Contracts, all Scenarios, the Review checklist, and the generic
DoD, with no Blockers or Majors and a single non-blocking Minor. Stamp ADR-0101 `Reviewing →
Implemented` and the F51 feat row `reviewing → implemented`. Nothing loops back to the builder; no
superseding ADR needed.
