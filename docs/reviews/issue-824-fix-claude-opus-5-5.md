# Fix review — issue #824 (strict logs `since`), claude-opus-5-5

- **Issue**: #824 (kind/task; decided on the issue 2026-10-10: API strict on both forms, funcdctl lenient for RFC3339 times)
- **Branch**: `fix/824-strict-logs-since`, one commit `feat(api)!: the logs since parameter takes only the duration and timestamp forms`
- **Producing model**: claude-opus-5-5
- **Reviewer**: fix-review gate, loop 1
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (1 model, 1 issue)
- **Definition of Done**: 12/12

## What changed

| File | Change |
|---|---|
| `internal/controlplane/logs.go` | `parseSince` takes `v1.ParseDuration` (ADR-0194 grammar) or the `v1.Timestamp` decoder (ADR-0196 form); anything else is `fault.Invalid` naming both forms. Both `since` doc tags updated. |
| `cmd/funcdctl/logs.go`, `cmd/funcdctl/workflow.go` | new `sinceParam`: a grammar duration is sent as typed, any RFC3339 time is sent as `v1.NewTimestamp(t).String()`, anything else is refused before the request. Both help texts name the two forms with an example. |
| `internal/funclog/logread/logread.go` | `SinceForms`, the shared refusal wording. |
| `pkg/sdk/logs.go` | `LogsOptions.Since` comment. |
| `api/openapi/funcd.v1alpha1.yaml` | regenerated `since` descriptions for both routes (the `api/openapi` sync test passes). |
| `internal/controlplane/since_test.go`, `cmd/funcdctl/since_test.go` | `TestIssue824_SinceTakesOnlyDurationAndTimestamp`, `TestIssue824_LogsSinceConvertsToWireForm`. |

## Verification run

1. **Fails without the change.** The `origin/main` versions of the five changed non-test files were laid over the branch with `go test -overlay` (no stash):
   - `TestIssue824_SinceTakesOnlyDurationAndTimestamp` fails: `expected: 400 actual: 200` for `since "500us"` on the function route — the issue's reason.
   - `TestIssue824_LogsSinceConvertsToWireForm` fails: `logs --since 2026-10-08T00:00:00+02:00` is sent as typed instead of `2026-10-07T22:00:00.000Z`.
2. **Passes with the change**, un-skipped, under `-race`; also `-race -count=10` on both tests, no race reported.
3. **User-visible behaviour**: the built `funcdctl logs --help` and `funcdctl workflow logs --help` show the new `--since` text; `funcdctl logs fn --since 1.5h` exits 1 before any request with the message naming both forms.
4. **Cause, not symptom**: the stdlib `time.Parse(RFC3339)` / `time.ParseDuration` pair in `parseSince` — the cause the issue names — is replaced by the two ADR parsers. Nothing is masked.
5. **Mutants** (overlay, each killed):
   - `parseSince` back to `time.ParseDuration` → killed (`500us` returns 200).
   - `parseSince` timestamp branch back to `time.Parse(time.RFC3339)` → killed (`2026-10-07T22:00:00Z` returns 200).
   - duration sign flipped (`now + d`) → killed (`WithinRange` bound check).
   - `sinceParam` sends the RFC3339 time unconverted → killed.
   - `sinceParam` drops the client refusal → killed (`1.5h` returns no error).
6. **Scope**: every hunk serves #824. No test weakened or deleted. No other route or field changed; the 400 mapping is the existing `fault.Invalid` path.
7. **Reuse**: `v1alpha1.ParseDuration`, the `v1alpha1.Timestamp` decoder (`UnmarshalJSON`; no exported string parser exists) and `v1alpha1.NewTimestamp` for the client conversion. The tests reuse the existing harnesses (`newLogsServer`, `newRunLogsServer`, `seedRun`, `do`, `execCLI`, `devToken`). See Minor 1 for the wording constant.
8. **Conventions**: `api/fault` errors, ctx-first unchanged, no `any`, top-level imports, no comment bloat (each new comment names #824 or the ADR). OpenAPI regenerated, not hand-edited.
9. **ADRs**: ADR-0194 and ADR-0196 (both Implemented) list the logs `since` parameter as out of their scope and name #824; the change brings it to their wire forms and contradicts neither. No ADR file edited.
10. **Checks** (touched packages: `internal/controlplane/...`, `cmd/funcdctl/...`, `internal/funclog/logread/...`, `pkg/sdk/...`, `api/...`): build darwin and linux OK; `go test -race` all ok; `go vet` darwin and linux OK; golangci-lint darwin and linux `0 issues`. No e2e, no repo-wide test, no Lima lane (the gate runs once per PR).
11. **Shape**: one commit, `Fixes #824`, `BREAKING CHANGE:` footer, attribution trailer. `feat(api)!:` rather than `fix(...)`: the issue is a kind/task with a breaking API change, matching the ADR-0194/0196 precedent (#832, #840) under `bump-minor-pre-major`.
12. **Dev-machine references**: none.
13. **Siblings and coverage**: `parseSince` has exactly two callers, the function-logs and the run-logs route; both are driven by the test. No other API query parameter takes a duration or a timestamp. Every case in "Done when" and in the decision comment has a test: `500us`, `1.5h`, `-15m`, an offset timestamp and a non-3-decimal timestamp get 400 naming both forms; the CLI converts `Z` and `+02:00` times and refuses `1.5h` before any request; help texts updated.

## Findings

### Blocker
None.

### Major
None.

### Minor

1. **`logread.SinceForms` restates the v1alpha1 form wording** (attribution: model). `internal/funclog/logread/logread.go` writes the duration grammar and the timestamp form in words a third time, beside the unexported `durationGrammar` (`api/types/v1alpha1/duration.go`) and `timestampForm` (`api/types/v1alpha1/timestamp.go`), with a different timestamp example. If either ADR form's description changes, the `since` message can drift. Exporting the two v1alpha1 descriptions and composing `SinceForms` from them would keep one source. Low impact; not blocking.
2. **The funcdctl refusal names the API's strict timestamp form** (attribution: issue — the decision asked for "the same message"). `funcdctl logs --since yesterday` answers with "an RFC3339 UTC timestamp with exactly 3 fractional digits", while the CLI accepts any RFC3339 time (its help text says so). This follows the decision literally; a CLI-specific wording would be a follow-up the decider can ask for.

## Verified correct (keep)

- One parser for both routes; the API takes exactly one wire form per kind, as ADR-0194/0196 decided for every other field.
- The client converts with `NewTimestamp` (UTC, truncated to the millisecond) — the test covers truncation of `.1239Z` to `.123Z` and an offset time.
- The client refuses an invalid value before any request (the test asserts no request was sent).
- The bound test checks the resolved instant, not only the status code, so a sign error is caught.

## Recommendation

Pass. Hand back to `/fix` Step 8. The two Minors can stay as they are or be folded into a later cleanup.
