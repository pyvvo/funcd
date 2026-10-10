## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0205 implementation, model: claude-opus-5-5)

Reviewed commit `0790aa12` (`feat(backup)!: implement ADR-0205`) against `origin/main`: 26 files, +2825/−30.

### Verification run

| Check | Command | Result |
|---|---|---|
| build | `scripts/agent/d go build ./...` | exit 0 |
| vet | `go vet` on the touched packages (`internal/backup/{runner,verify}`, `internal/controlplane/...`, `internal/platform/config`, `pkg/funcd`, `pkg/sdk`, `cmd/funcd`, `cmd/funcdctl`) | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.` |
| tests | `go test -race -count=1` on the same packages | exit 0, all 9 packages `ok` |
| ADR diff | `git diff origin/main...HEAD -- docs/adr/` | only the status line `Accepted → Reviewing` |
| deps | `git diff --stat -- go.mod go.sum` | no change (no new module, as the ADR requires) |

The e2e suite, repo-wide tests, Linux lint and `just ci` are left to the PR gate, as this review's brief requires.

### 🟡 Major

None.

### Minor

- **The generation layout is duplicated in `verify`** · attribution: model · `internal/backup/verify/verify.go`
  `dir` and `partKey` rebuild `gen/<class>/%010d-<timeline>/` and `<store>/part-%05d`, which
  `internal/backup/layout.go` (`genDir`) and `internal/backup/write.go:134` already own (unexported). If ADR-0203's
  layout changes, the verifier and the writer can drift apart without a compile error. Fix: export one layout helper
  from `internal/backup` (for example `GenDir`, `PartKey`) and use it in both places. This is not blocking: the
  verifier's tests write through the real `Target.Write` and read the result back, so today's layout is pinned.

### ✅ Verified correct (keep it)

- **All 12 scenarios have a named test, un-skipped and passing under `-race`.** `TestScenarioRunsOnInterval`,
  `…RestartKeepsCadence`, `…FailedRunRetries`, `…OverrunNoOverlap` and `…RPORiskFollowsVerified` are in `runner`;
  `…VerifyDetectsDamage` is in `verify`; `…BackupOffByDefault`, `…ImpossibleSettingsRefused` and
  `…TightSettingsWarn` are in `cmd/funcd`; `…VerifyPinsGeneration`, `…StatusShowsRestorePoints` and
  `…KVStreamWithoutPlatformBackup` are in `cmd/funcdctl`. The plan's unit tests are all present and pass:
  `TestCheckBackupRules`, `TestBackupTimesDefaults`, `TestDueTime`, `TestStatusFromEntries`, `TestRecorderStreams`,
  `TestRPOWarnOncePerRPO` and `TestVerifyNoGeneration`. The tests drive file targets with an injected `Clock` and
  `After`, and read the metrics through a `ManualReader`, as the test plan specifies.
- **The runner follows Decision 2.** The first due time is the newest complete ladder entry plus `interval`, and pins
  are never counted. A failed first listing counts as a failed run and does not stop the start. A successful run sets
  the next due time to `start + interval`. A failed run sets it to `min(end + retryInterval, start + interval)`. An
  overrun starts the next run at once and logs one Warn with `duration_ms` and `interval_ms`. A held due time writes
  nothing, counts nothing, and is checked again after `retryInterval`. A run that shutdown cancels (`ctx.Err()`) is
  not a failure. Runs execute one at a time on a single goroutine.
- **Status (Decision 4) is built from the listing, and nothing is stored.** `viewOf` takes `lastSuccessTime` from
  ladder entries and `earliestRestorePoint` from ladder and `pre-upgrade` entries. It takes `lastVerifiedTime` from
  the highest-numbered complete `verified` copy, at its original's `At`, and leaves the field absent when the
  original has expired. The `rpoRisk` re-list at `lastVerifiedTime + rpo` is implemented by `nextCheck` and
  `waitUntil`, and that loop always terminates. A failed listing returns the last status with `fault.Unavailable`,
  which the route maps to 503 (`internal/controlplane/backup_test.go:66`).
- **Metrics and the alert follow Decision 5.** The meter is `funcd.backup`. The instruments are
  `funcd.backup.runs`, `funcd.backup.duration_ms` and `funcd.backup.rpo_risk`, with `stream` and `result`
  attributes. The Warn fires when `rpoRisk` turns on and then once per rpo, and an Info line is logged when it clears.
  The meter is a no-op without `telemetry.endpoint`, because telemetry is now built before the runner in
  `cmd/funcd/main.go`.
- **The settings check follows Decision 3.** `config.CheckBackup` reads only the config, returns errors first, and
  `Validate` refuses the start on the first error. Its rules cover malformed or out-of-bounds times, an rpo below the
  interval, a ladder that expires before the next run, the scheme and `credentialsFile` rules, retention minimums,
  and ADR-0204's encryption rules. Its warnings cover an interval above half the rpo, a too-short
  `retention.verified`, keys set off their defaults without a target, and memory mode with a target. In
  `cmd/funcd`, errors from `envelope.New` and `backup.Open` refuse the start. The `preUpgrade` retention row waits for
  ADR-0207's key, which does not exist yet; that is sequencing, not a gap in this implementation.
- **Verify follows Decision 6.** It checks every part against the manifest's `parts`, `bytes` and `sha256`, and
  refuses missing parts and parts beyond the manifest's count. It decrypts each store file to its end through
  `envelope.Opener`, parses `backup.Records` to the end, and requires keys in strictly ascending order. With
  `--escrow` it also runs `escrow.Find`. Only after every check passes does it pin the parts and then the manifest
  under `gen/verified/`, with `IfNotExist`; a part that is already present must match byte for byte. A rerun writes
  nothing (`Pinned: false`) and exits 0. The daemon holds no read credential and no identity: verify exists only in
  `funcdctl`.
- **The route matches the contract.** `GET /apis/funcd.io/v1alpha1/platformbackup` authorizes `get` on
  `WorkerNode`, so a developer gets 403; both `controlplane` and `pkg/funcd` (`TestPlatformBackupWired`) check this.
  The route is mounted next to the dead-letter routes, and its stub is registered in `specgen`, so the OpenAPI
  document lists it. `WithPlatformBackup` makes `New` call `Bind` with the event store, the metastore and the run
  state; `Run` starts the runner; and `TestPlatformBackupWired` checks that a real generation holds the stores
  `events`, `metastore` and `runs`.
- **The KV stream.** The scenario is exercised at the `Recorder("kv")` seam. Feeding the KV export into `Record` is
  ADR-0209's work: its Contracts put `Record` on its `BackupConfig` and its plan wires `Recorder("kv")`. The runner is
  still built when only the KV export is on, which Decision 2 requires, so that wiring has something to attach to.
- **Conventions.** No `any` in the exported APIs. Errors use `api/fault` kinds. `slog` is the only logger. Every
  function takes `ctx` first. No `panic` or `fmt.Print*`. Imports are at the top level. `examples/funcdconfig.yaml`
  is in block style. The OpenAPI, `examples/backup-lifecycle.md` (verify every `(rpo − interval)/2`) and the
  example config are updated as the plan lists. The ADR text was not changed except the status bump.

### Definition of Done

15 / 15 items hold: the ADR's 5 Review-checklist items and the 10 generic implement-gate items. The ADR's own DoD
(`-race` tests and no new module) holds. `just ci` was not run here: by this review's brief, it runs once at the PR
gate. Its component checks (build, vet, lint, tests on the touched packages) are green.

### Model scorecard

To record: claude-opus-5-5 on ADR-0205 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 15/15.
The batch's tracking PR records the ledger row in `docs/reviews/model-ledger.json` and `model-scorecard.md`.

### Recommendation

Sign off: the ADR moves to `Implemented`, and the FEAT-0009/F109 row moves to `implemented` in the tracking PR. The
duplicated layout helper is a small cleanup that can follow; it does not block.
