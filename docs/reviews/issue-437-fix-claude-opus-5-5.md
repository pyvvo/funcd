# Fix review — issue #437: buildOptions leaves the store, bus and KV driver open when a later step fails

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #437 fix, model: claude-opus-5-5)

Change: branch `fix/i437`, commit b844bf0 `fix(funcd): close the drivers buildOptions opened when a later step fails`
(`cmd/funcd/main.go`, `cmd/funcd/main_test.go`).

`buildOptions` now has a named `err` result and a deferred cleanup that closes, in reverse order, every driver it
recorded in `opened` (store, bucket, bus, KV driver when it is an `io.Closer`). `substrateOptions` also returns the
bucket so `buildOptions` can close it. On success the platform still owns the drivers. This removes the cause the
issue names: the error returns after the substrate is opened no longer drop the drivers.

### 🟡 Minor 1 — `substrateOptions` still drops the bucket when the bus fails to open  ·  attribution: model

`cmd/funcd/main.go` `substrateOptions`: in both the memory and the file branch, `gocloud.Open` succeeds and then
`nats.Open` fails, and the function returns the error without closing the bucket. The deferred cleanup in
`buildOptions` cannot close it, because `substrateOptions` returns a nil bucket on error. The issue's expected
behavior names the bucket among the drivers a failed call must close. The fix edited every return in this
function, so closing the bucket on that one path (as `buildKVStore` already does for its backup bucket after
`OpenWithSeamsFor` fails) was in reach. The impact is small: a file bucket holds no lock and a `mem://` bucket
holds only memory.

### Observation (not scored)

The OTLP telemetry pipeline that `observability.NewTelemetry` builds is not shut down when a later step fails.
The issue does not list it among the drivers to close, so it is out of scope for this fix; a follow-up issue
may be worth filing if it holds a connection.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With `cmd/funcd/main.go` reverted to `origin/main` (and the
  three `substrateOptions` call sites in the test file returned to the old arity so the package compiles),
  `TestIssue437_FailedBuildOptionsClosesDrivers` fails in both subtests: `memory` reports "the in-memory NATS
  server is still running" (the `funcd-bus-mem-*` JetStream dir that `internal/bus/nats` creates is still
  present), and `file` reports "the metastore is still open" (Badger directory lock still held).
- **Passes with the fix** under `-race` (both subtests). The worktree was reset to the starting HEAD and is clean.
- **Mutants** (overlay, no file edits), each killed:
  1. drop `opened = append(opened, st)` → `file` fails, "the metastore is still open";
  2. drop the KV `io.Closer` append → `file` fails, "the KV driver is still open";
  3. record only the bucket, not the bus → `memory` fails, "the in-memory NATS server is still running".
- **Cause, not symptom**: the drivers are closed where they are dropped; no timeout, retry or skipped test.
- **Close order is right**: reverse order closes the KV driver (which may publish CDC to the bus) before the bus,
  and the store last.
- **Scope**: every hunk serves the issue. The three test-call arity changes follow from the new
  `substrateOptions` result; no test was weakened or deleted.
- **Reuse**: the deferred close-on-error idiom matches the one `cmd/funcdctl/dev.go` `buildPersistDrivers`
  already uses; the two live in different `main` packages, so there is no shared helper to reuse. The test reuses
  `shortDataDir`, `config.Load`, the real Badger drivers and `fault.KindOf`; no new harness or dependency.
- **Conventions (ADR-0002, CLAUDE.md)**: `api/fault` kind asserted; slog only; imports at top level; YAML in the
  test is block style; comments are short and say why (the `TMPDIR` note, the issue reference on the doc comment).
  The test uses `shortDataDir`, not `t.TempDir()`, for the data dir.
- **ADRs**: no ADR file touched; ADR-0061 (config to options) is unchanged in substance — ownership on success
  still passes to the platform.
- **Checks** (touched package): `go build ./...` ok; `go vet ./cmd/funcd/` ok; `golangci-lint run ./cmd/funcd/`
  0 issues; `go test -race ./cmd/funcd/` ok.
- **Shape**: `fix(funcd):` subject, Cause/Fix/Test body, `Fixes #437`, attribution trailer, one issue in one commit.

### Definition of Done

11 of 11 applicable items hold (regression test, fails pre-fix, passes with `-race`, mutants killed, root cause,
scope, ADRs, touched-package checks green — repo-wide, Linux lint, e2e and lanes are left to the group gate —,
conventions, reuse, commit shape).

### Model scorecard

| Model | Verdict | Blockers | Majors | Minors | Model-attributed | DoD |
|---|---|---|---|---|---|---|
| claude-opus-5-5 | pass | 0 | 0 | 1 | 1 | 11/11 |

### Recommendation

Ship. Optionally, close the bucket in `substrateOptions` when `nats.Open` fails (Minor 1) in a later touch of the
same code.
