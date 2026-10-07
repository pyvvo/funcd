## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0196 implementation, model: claude-opus-5-5)

Branch `feat/adr-0196-utc-millisecond-timestamps`, 4 commits on merge base 77ab7f9c (merges cleanly onto the
current origin/main fc6d5379, checked with `git merge-tree`). 52 files, +915/-101.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The sensor stamp sites have no site-level test** · attribution: `adr` (not scored) ·
  `internal/sensor/sensor.go:353`, `:411` (DLQ `failedAt`) and `:491-492` (Invocation `startTime`/`endTime`) call
  `v1.NewTimestamp` as the Contracts table requires (verified by reading), but no test pins them: a mutant that
  writes `v1.Timestamp(time.Now())` there would leave the wire form unchanged, because `MarshalJSON` re-applies UTC
  and truncation, and only the in-memory value would differ from the decoded one. The ADR's test plan does not list
  `internal/sensor` for the scenario, so the gap is in the plan, not in the work. No action needed for sign-off.

### ✅ Verified correct (keep it)

- **Build, vet, lint, darwin and linux**: `go build ./...` exit 0, `go vet ./...` exit 0, `GOOS=linux go build ./...`
  exit 0, `GOOS=linux go vet ./...` exit 0, `golangci-lint run ./...` "0 issues." exit 0, and the same lint with
  `GOOS=linux` "0 issues." exit 0. `gofmt -l` on the touched Go files is empty. The tree is clean.
- **Tests, touched packages, `-race -count=1`**: all 16 packages `ok` (v1alpha1, cmd/funcdctl, s3gateway,
  controlplane including the OpenAPI drift test, eventing, deadletter and its badger and memory drivers with the
  shared contract suite, logread, function, kvstore/badger, observability, sensor, store, workflow, pkg/sdk); exit 0.
- **Every scenario has a same-name, un-skipped, passing test**:
  step-times-are-timestamps → `TestStepTimesAreTimestamps` (`internal/workflow`, mirror over the fake dispatcher;
  `cmd/funcdctl`, `get -o json` plus `workflow describe` "duration: 500ms", Pending step without keys);
  every-timestamp-utc-millisecond → `TestEveryTimestampUTCMillisecond` in v1alpha1, deadletter, logread, eventing
  (timer and blob envelope plus blob `data.time`), kvstore/badger (manifest) and workflow (`stepTime`, `COMPLETED_AT`);
  local-clock-stamps-utc → `TestLocalClockStampsUTC` (`Conditions.Set` with `time.Local` at UTC+2; the drain with a
  UTC+2 manual clock); zero-time-omitted → `TestZeroTimeOmitted` (v1alpha1 zero objects; the `pkg/sdk` apply body);
  log-line-time-fixed-form → `TestLogLineTimeFixedForm` (JSON, text and audit on a UTC+2 host);
  non-fixed-input-refused → `TestNonFixedInputRefused` (humatest 422 at `body.metadata.creationTimestamp` naming the
  form, nothing stored, fixed form accepted; `sdk.DecodeManifest` refuses and accepts quoted or unquoted).
- **Contract tests**: truncation and zone, `String` re-applying the form, years 0000/9999 and the 10000 and -1 errors,
  the `UnmarshalJSON` table (no fraction, 2 and 6 digits, offset, lowercase `z`, month 13, second 60, space, number,
  bool, object, empty string; `null` leaves the value; a refused value leaves the field), pattern and parser agree,
  `Schema` returns a new value per call. Decision 10: `TestManifestReadsRFC3339Nano` loads RFC3339Nano, an offset and
  whole seconds and rewrites them in the form. Decisions 7 and 11: `TestLinesWithinOneMillisecondKeepTimeOrder`,
  `TestRenderLogLinesTimeFixedForm`.
- **Mutants (overlay, the work untouched), all killed**: (1) `NewTimestamp` without `.UTC()` fails
  `TestNewTimestampTruncatesToUTCMillisecond`, `TestTimestampStringReappliesTheForm` and both `TestLocalClockStampsUTC`;
  (2) the audit handler without `ReplaceAttr` fails `TestLogLineTimeFixedForm`; (3) the log read sorting on the
  millisecond instead of the nanosecond fails `TestLinesWithinOneMillisecondKeepTimeOrder`; (4) `stepTime` without the
  zero check fails `TestStepTimesAreTimestamps` and `TestEveryTimestampUTCMillisecond` in `internal/workflow`.
- **Contracts**: `api/types/v1alpha1/timestamp.go` matches the Contracts block exactly (`TimestampLayout`,
  `TimestampPattern`, `type Timestamp time.Time`, `NewTimestamp`, `IsZero`, `String`, `MarshalJSON`, `UnmarshalJSON`,
  `Schema`); `TimestampPattern` is the only copy of the form, used by `timestampRe` and `Schema`; decode errors are
  `fault.Invalid` and name the form. `stepTime` (`reconcile_run.go`), `manifestTime` (`backup.go`) and the hook
  match; the hook is the exported `observability.ReplaceAttr`, as ADR-0197's partial supersession names it, set on
  the text and JSON handlers (`logger.go:84`) and the audit handler (`audit.go:53`).
- **Every Contracts-table field and stamp site** changed as listed: `creationTimestamp` (`omitzero`, `store.go:331`),
  `deletionTimestamp` (`*Timestamp`), `lastTransitionTime` (`status.go:39`, `:47`), `drainingSince`
  (`function.go:1491`), Invocation `startTime`/`endTime`, step `startedAt`/`endedAt` via `stepTime`, DLQ `failedAt`,
  log-read `time` (built after the nanosecond sort), CloudEvent `time` (both constructors), blob `data.time`
  (`blobwatch.go:283`), manifest `at`, `COMPLETED_AT`. Runstate keeps int64. The backup prefix keeps
  `started.UnixNano()`.
- **ADR Definition of done greps**: no `time.Time`, `*time.Time` or int64-instant field with a JSON tag remains in
  `api/types/v1alpha1`, deadletter, logread, `eventing/cloudevent.go` or the backup `segment` (none in the whole
  non-test tree); `time.RFC3339` outside tests is only `internal/artifact/artifact.go:117` and
  `internal/controlplane/logs.go:308`; the regenerated spec has 10 `format: date-time` fields, each followed by the
  pattern, and no integer instant; the window assertion in `internal/store/store_test.go` truncates its lower bound.
- **funcdctl**: `workflow describe` computes `endedAt − startedAt` with the ADR-0194 formatter; `logs` text prints
  `l.Time.String()`; `dev` is untouched.
- **Tracking**: the ADR diff is only the status line `Accepted (2026-10-07)` → `Reviewing (2026-10-08)`; the
  FEAT-0000/F02 row reads "timestamps: reviewing". The first commit carries `!`, `Fixes #821` and
  `BREAKING CHANGE:`. No new dependency; `api/**` imports no `internal/**`.

### Definition of Done

21 / 21 items hold (ADR Review checklist 6, ADR Definition of done 5, generic implement-gate DoD 10). `just ci`
was not run as one command here: its constituents (build, vet, lint on both OSes, gofmt, the touched packages'
tests with `-race`, the OpenAPI drift test) are green, and the repo-wide `just ci-full` and e2e run once in the PR
gate, as the task directs.

### Model scorecard

Not recorded by this run (the task forbids editing `docs/reviews` and the ledger). Row for the batch ledger PR below.

### Recommendation

Pass. The work is ready for the gate; the orchestrator stamps ADR-0196 `Reviewing → Implemented` and the F02 row
"timestamps: implemented" in the ledger/status step. The single Minor is an ADR test-plan gap and needs no rework.

```json
{"adr": "0196", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 1, "model_attributed": 0, "dod_passed": 21, "dod_total": 21, "report": "docs/reviews/adr-0196-implementation-claude-opus-5-5.md", "notes": "pass; 1 minor [adr]: the test plan pins no sensor stamp site (DLQ failedAt, Invocation start/end); all 6 scenarios same-name tests green with -race; 4/4 mutants killed; darwin+linux build/vet/lint clean"}
```
