## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0329 rollout, the disaster recovery feature, round 2, model: claude-opus-5-5)

Round 2 reviews two commits on top of [round 1](adr-0329-rollout-backup-claude-opus-5-5.md). Both change texts only:

- `ad10f2f8` rewords the five field descriptions and four case texts of round 1's two Minors;
- `c81336a0` drops the KVStore example from the shared 422 body case, which round 1 routed to the batch.

Round 1's two Minors are closed, and so is the routed 422 text. No finding is open. The question of `held: false`
with no backup stream stays with the decider, and the descriptions now tell the truth either way. ADR-0329 is already
`Implemented`, so this review stamps nothing.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### Routed (not this work's)

- **`getPlatformBackup` reads `held: false` on a held platform with no backup stream** · home: above · destination:
  the decider · verdict: uncovered. It is unchanged since round 1.

### ✅ Verified correct (keep it)

- **Round 1's Minor 1 is closed.** Each new description matches its code.
  - `held`, `interval` and `rpo` read false and 0s "when funcd runs no backup: under storage.mode memory, or with
    neither backup.target nor kvstore.backup.enabled set".
    - That is the condition under which `backupRunner` (`cmd/funcd/main.go`) returns no runner.
    - The daemon then passes no `WithPlatformBackup`, and `pkg/funcd` sets the route's statuser only from a runner
      that is not nil. So the route answers the zero status.
    - With a runner, `held` reads the hold that the daemon gives both the runner and the platform.
  - `rpoRisk` is true "when the platform backup is on and …". `runner.status` sets it only when `Enabled` is true.
  - `QuarantineReturn.since`: "Left out, a return that was cut short resumes first, else the object's newest kept entry
    returns". That is `quarantine.Pick`: with no since, a `returning` entry wins, and otherwise the newest of the
    entries left is taken, all of them `kept`.
- **Round 1's Minor 2 is closed.** Each new case text matches its raise site.
  - **`returnQuarantined`'s 404.** The When now speaks only of entries that are kept or returning, and names the
    `moving` entry that `Pick` skips.
    - The Fix says that a moving entry returns once a later start has finished its move. That is ADR-0288 Decision 3.
    - Only a start that is not held moves data (`pkg/funcd/funcd.go`, ADR-0307), and the Fix waits for the start that
      finishes the move.
    - If the object exists again by that start, the start writes the entry `kept` as it stands. The keys not yet moved
      stay with the object, so the return meets the listed 409 for an object that holds data.
  - **Its 409 for a missing object.** "Apply the KVStore or the Bucket again, or the Function with the volume in its
    spec.volumes" now fits both variants. The When still says that the volume is a local one.
  - **`releaseHold`'s 409 for a Restore that is Pending or Running.** "Release the namespaces after it again; release
    this one once the Restore has ended." This matches `ReleaseNamespaces`, which stops at the first refusal, and the
    sister case's wording.
  - **`restoreStuck`, the Fix of the two waits for an in-place Restore.** "A Restore stuck in Pending fails once the
    Backup that it waits for is deleted." "Fails" is the right word, not a reason:
    - a deleted `spec.backup` fails the Restore `BackupNotReady` (`internal/services/backup/restore.go:65`);
    - a deleted export fails it `ExportFailed` (`:155`);
    - `MapBackup` reconciles both Restores when their Backup goes;
    - no admission of `internal/controlplane/admission` handles a Backup's delete, and the matrix's `deleteBackup` row
      deletes one with a developer's credential.
  - The second sentence of these Fixes follows a precedent on main: `createFunction`'s 400 Fix has two sentences.
- **The routed 422 text is closed.** `bodyCases()` now ends "Correct the field at the location that the detail
  gives."
  - huma's details name the location, such as `(body.namespaces[0])` in round 1's probes.
  - The 15 operations that read a body change: 8 of the disaster recovery tab, 3 of the KV tab and 4 of the Function
    tab.
  - The one `body.spec.tables` left outside the reviews is a KV row's expected detail (`pkg/funcd/api_answers_test.go:262`).
    It is huma's answer, not the text.
- **Only texts changed.** A Python diff of the spec at `e5ffcf51` against `c81336a0`:
  - It finds 168 operations on both sides, and 16 changed: 9 of this tab, 3 of the KV tab and 4 of the Function tab.
  - The changed leaves are 25 descriptions, 20 Fixes and 1 When, and nothing else.
  - Two schemas, `PlatformBackupStatus` and `QuarantineReturn`, change their descriptions only.
  - Statuses, types, parameters and bodies are as in round 1. So round 1's probe of 195 answers and its full guard
    run still hold.
- **Every matrix row still names one case.** The 404's row (`holds no entry of the object`) still matches the new When.

### Checks run in round 2

| Check | Result |
| --- | --- |
| `go test -race -count=3 -run '^TestDisasterRecoveryAnswersMatrix$' ./pkg/funcd` | ok, 3.9 s |
| `TestFunctionAnswersMatrix`, `TestAnswersOfKVStoreOperations` and `TestAnswersOfKVStoreQuota`, once | PASS; 148 Function subtests |
| `go test -race -count=1 ./internal/controlplane/...` | ok, kindref included |
| `scripts/agent/d just generate` | exit 0; the tree stays clean |
| `go vet`, `golangci-lint` and `gofmt -l` on `internal/controlplane`, `internal/backup/runner` and `internal/platform/hold` | exit 0; 0 issues; clean |
| The spec diff, round 1 against round 2 | texts only |
| The full `pkg/funcd` run, `just ci`, e2e | not run, as asked: texts only, and the batch gate runs them |

### Definition of Done

18 of the 18 items that apply hold, as in round 1. Round 1 counted three items out: the two `invokeFunction` items,
which belong to the pilot, and `just ci`, which the batch gate runs.

### Model scorecard

To record by the integrator: claude-opus-5-5 on ADR-0329 (implementation, the disaster recovery rollout, round 2) →
pass, 0/0/0, 0 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The batch gate runs next. The decider's answer on `held: false` would change behavior, not these texts.
Nothing is stamped: ADR-0329 is already `Implemented`.
