## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0329 rollout, the disaster recovery feature, model: claude-opus-5-5)

The work is the disaster recovery item of ADR-0329's rollout, commit `e5ffcf51` on `6ac8d855`. It describes the 19
operations of the disaster recovery Spec tab: the CRUD of BackupSchedule, Backup and Restore, `getHold`,
`releaseHold`, `returnQuarantined` and `getPlatformBackup`. `TestDisasterRecoveryAnswersMatrix`
(`pkg/funcd/disaster_recovery_answers_test.go`) proves each case in a row of its own. Every check below ran on branch
`feat/rollout-0329-backup` at `e5ffcf51`. ADR-0329 is already `Implemented`, so this review stamps nothing.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

- **Minor 1: five field descriptions leave out a condition that the answer follows** · attribution: model.
  - **The platform backup's status.** With no backup stream (no `backup.target` and no `kvstore.backup.enabled`, or
    `storage.mode` memory), the route answers the zero status (`internal/controlplane/backup.go:46-48`).
    - A probe on the matrix's held platform got `{"enabled":false,"held":false,"rpoRisk":false,"interval":"0s","rpo":"0s"}`
      from `getPlatformBackup`, while `getHold` answered `"held":true`. `6ac8d855` answers the same.
    - `rpoRisk` is set only while the platform backup is on (`internal/backup/runner/runner.go:430-433`), as ADR-0205
      Decision 4 says: "backup on, and `lastVerifiedTime` absent or older than `objectives.rpo`".
    - The new descriptions (`runner.go:52-55`) say that `held` is "True while the platform is held", that `rpoRisk` is
      "True when no verified generation is newer than backup.objectives.rpo", and that `interval` and `rpo` are
      `backup.interval` and `backup.objectives.rpo`. This answer contradicts each of the four.
  - **`QuarantineReturn.since`** (`internal/platform/hold/hold.go:111`): "the object's newest entry when left out".
    `quarantine.Pick` takes a `returning` entry first, so that a cut return resumes
    (`internal/platform/quarantine/quarantine.go:101-120`, ADR-0288 Decision 6).
  - Fix: name the condition. For example, `rpoRisk` applies "with the platform backup on". `held`, `interval` and `rpo`
    read false and 0s when no backup stream runs. A `returning` entry is taken first. Then run `just generate`.
- **Minor 2: four case texts miss part of their case** · attribution: model.
  - **`returnQuarantined`'s 404** (`internal/controlplane/hold.go:379`): "The quarantine holds no entry of the object".
    - `Pick` also skips a `moving` entry and answers this 404 (`quarantine.go:106`).
    - ADR-0288 Decision 3: a move whose store fails leaves its entry `moving`, and `hold status` lists it.
    - In that case the quarantine does hold an entry, so the When is false. It should name that kind of entry.
  - **`returnQuarantined`'s 409 for a missing object** (`hold.go:381-382`). Its Fix, "Apply the object again, then
    return its data.", does not fit the second variant: "the Function declares no local volume of that name"
    (`internal/function/quarantine.go:126`). That Function exists, and an apply of it as it stands changes nothing.
    The Fix should say to declare the local volume again.
  - **`releaseHold`'s 409 for a Restore that is Pending or Running** (`hold.go:367-368`). Its Fix names only the refused
    namespace. `ReleaseNamespaces` stops at the first refusal (`hold.go:283-297`), so the namespaces after it stay held.
    The sister case's Fix says so: "Release the namespaces after it again, without it."
  - **The two waits for an in-place Restore**: `deleteRestore`'s 409 (`internal/controlplane/routes_rest.go:355`) and
    `createRestore`'s one-at-a-time 409 (`:387`).
    - Both Fixes wait until a Restore is Succeeded or Failed, but a Restore stuck in `Pending` never ends by itself.
    - The decided way out is to delete the Backup that the Restore waits for, which fails the Restore with
      `BackupNotReady`. The behavior row "An in-place Restore at apply" (`docs/behavior/disaster-recovery.md:47`) and
      ADR-0228 Decision 2.4 and its Open questions record it.
    - The Fixes should name it.

### Routed (not this work's)

- **The shared 422 body case names a KVStore field** · home: elsewhere · destination: the integrator.
  - `bodyCases()` (`internal/controlplane/describe.go:194`), from the pilot, ends with "such as body.spec.tables".
  - It appears on the 8 operations of this tab that read a body (on `releaseHold` and `returnQuarantined` too), on 4
    operations of the Function tab, and on 3 of the KV tab. Only the create and the replace of a KVStore have the field.
  - Direction: one change in `describe.go` for the whole batch. Either drop the example, since the detail names the
    location, or let `bodyCases` take each operation's own example field. Then regenerate every Spec tab once.
- **`getPlatformBackup` reads `held: false` on a held platform with no backup stream** · home: above ·
  destination: the decider · verdict: uncovered.
  - It happens: the probe above, on the branch and on `6ac8d855`.
  - Nothing decides it. ADR-0205 (Decision 4 and the scenario `backup-off-by-default`) and the behavior row "A platform
    run" speak only of `enabled: false`. `TestPlatformBackupRoute` and `TestScenarioBackupOffByDefault` pin only
    `Enabled`. A search of the issues and of the board's cards finds nothing.
  - The question: should `held` follow the hold whether or not a backup stream runs? Minor 1's fix is due either way,
    since the description must say what the code does today.
- **`releaseHold` answers 400 for `advance` beside `namespaces` before it authorizes** · verdict: accepted, nothing
  to route.
  - ADR-0228 Decision 5.1 makes that body `fault.Invalid` and cites the check, which sits before the authorization.
  - `funcdctl` makes the two flags exclusive (`cmd/funcdctl/hold.go:57`).
  - The probe shows that a developer gets this 400, not the 403. The texts list both answers.
  - ADR-0228's Consequences say that no test pinned 5.1's `advance` beside `namespaces`. The matrix row now does.

### ✅ Verified correct (keep it)

- **TestMain's produced-status check ran on this item.**
  - `go test -race -count=1 ./pkg/funcd`, with no filter, passed on the first try in 96.9 s.
  - With exit 0 and no `-run`, `-skip` or `-list` flag, `checksAll` holds. So `compareAnswers` checked both halves for
    the 19 operations: no answer was unlisted, and every listed status was got.
- **The matrix.**
  - It has 180 rows: the 161 cases of the 19 operations and their 19 successes. Each case has a problem type, a When
    and a Fix, and each ends with a period (a script over the spec).
  - `runMatrix(t, rows)` runs with no exemption.
  - The three platforms keep the rows independent:
    - the held one keeps its Backups `Pending`, so its Restores never end;
    - the one that starts free quarantines three KVStores;
    - the one whose target cannot be listed is built on `drUnlistable`.
  - The objects' resourceVersions are opaque strings, such as `04ceb0cb979b03aa-6`, so the shared stale
    `If-Match: "1"` rows cannot match by chance.
  - Its helpers carry a `dr` prefix, so they cannot collide with the other items' files. The age recipients are the
    public ones of `api/types/v1alpha1/examples/disaster-recovery/`.
- **The four folds that this review was asked to judge.**
  - **`releaseHold`'s three `advance` refusals** (`blobSource`, `hold.go:313-331`): one status (400) and one type
    (`invalid`). The When names the form `<namespace>/<name>`, the EventSource and its blob event, and the Fix fits
    each. The row provokes the form.
  - **`returnQuarantined`'s 409 for a missing object**: the status, the type and the When hold for both variants. The
    Fix does not fit the volume variant (Minor 2).
  - **Its 409 for an object that holds data**: it holds for a KVStore (`holds N keys`), a Bucket (`holds objects`) and
    a volume (`is not empty`). The Fix fits each.
  - **Its 503**: one status and one type. The When names volumes off. Its first clause covers the two other variants:
    - a KV driver without `DropPrefix` keeps no KV quarantine, because its `List` reads nothing;
    - a blob store that cannot move a prefix moves nothing into the quarantine.
    - Both shipped KV drivers have `DropPrefix`, and the gocloud bucket moves prefixes, so only an embedder's driver
      reaches these two. The Fix fits each.
- **The 400s left out are unreachable.** Probes got 422 from the schema for each bad name:
  - `body.namespaces[0]`, `body.namespace`, `body.name` and `body.volume`;
  - an empty `volume`: `expected length >= 1`.
  - `NamespaceName` and `ObjectName` use the same `DNSLabel` pattern and lengths in their schema as in `Validate`
    (`api/types/v1alpha1/ids.go:27-30`, `:67-75`, `:129-137`). A Function with no `volume` still answers the listed
    400.
- **The texts match their raise sites** (admission `internal/controlplane/admission/backup.go`, `envelope.Parse`,
  `HoldService`, `holdPlatform.ReturnQuarantined`, `runner.Status`).
  - `funcdctl hold status`, `funcdctl hold release --namespace`, `funcdctl backup verify`, `funcdctl workflow resume`
    and `funcd restore run` all exist.
  - The 403s hold: Namespace and WorkerNode are cluster-scoped (`api/types/v1alpha1`, `Kind.Namespaced`), so only an
    admin passes (`internal/auth/rbac/rbac.go`).
  - A Backup with no ttl takes its schedule's, as the Fix of the ttl case says.
  - The hold texts use the Function feature's words: `funcdctl hold release --namespace` and "once the Restore has
    ended".
  - The cases agree with the decided rows of `docs/behavior/disaster-recovery.md`: the in-place Restore at apply, and
    `funcdctl hold release --namespace`.
  - No added text names an ADR.
- **No wire change.**
  - **The spec.** A Python diff of `6ac8d855` against `e5ffcf51` finds 168 operations on both sides, and 19 changed:
    the operations of this tab.
    - Unchanged: the paths, the methods, the parameters, the request bodies and the success responses, apart from
      their descriptions.
    - `security` is new. It is documentation only.
    - No schema is new or gone. Nine change their descriptions only: `Evidence`, `Marker`, `NamespaceHold`,
      `QuarantineEntry`, `QuarantineReturn`, `HoldReleaseInputBody`, `PlatformBackupStatus`, `Failure` and `Stream`.
  - **The live behavior.**
    - A scratch copy of the matrix sent its 180 requests, then 15 probes, one at a time, to `6ac8d855`, built from a
      `git archive`, and to `e5ffcf51`, through `go test -overlay`.
    - All 195 answers are identical: the status, the `Content-Type`, the `Allow` header and the body, with timestamps,
      UIDs, resourceVersions and object statuses removed.
    - On the branch, TestMain found none of the 195 answers unlisted.
- **Mutants**: three overlays each fail the matrix.
  - Without the restore-marker check in `restoreInPlace.Admit`, the createRestore row expected 409 and got 200.
  - Without `!rs.Ended()` in `ReleaseNamespaces`, the releaseHold row expected 409 and got 204.
  - With `fault.Internal` in place of `fault.Unavailable` in `runner.Status`, the row expected 503 and got 500. TestMain
    then failed too: `getPlatformBackup answered 500 Internal Server Error, which it does not list`.
- **The page and the list.**
  - The disaster recovery Spec tab shows the 19 operations and the fields of their bodies and answers.
  - The live `/openapi.json` carries their cases, and `/docs` answers 200.
  - The list of operations not yet described loses exactly these 19 lines, and `TestEveryOperationIsDescribed` passes.
- **Conventions.**
  - The bloat audit passes with no hard and no soft flag. Comment ratios: 10 to 135 lines in production, 13 to 280 in
    tests.
  - `drForbidden` serves the four admin-only routes. The three helpers of `registerWorkloadBackup` keep its call short.
  - The matrix is in a file of its own, and `api_answers_test.go` is unchanged.

### Checks run

| Check | Result |
| --- | --- |
| `go test -race -count=1 ./pkg/funcd`, no filter | ok, 96.9 s, on the first try |
| The matrix: `-race -count=3`, then `-race -count=10`, then `-v` | ok, 3.8 s and 8.2 s; 180 subtests pass |
| `go test -race` on `internal/controlplane/...`, `internal/platform/hold/...` and `internal/backup/runner/...` | ok, 20 s |
| `go vet` on the touched packages, on the host and with `GOOS=linux`; `GOOS=linux go build ./...`; `go vet -tags e2e ./pkg/funcd`; `gofmt -l` | exit 0; clean |
| `golangci-lint` on the touched packages, on the host and with `GOOS=linux` (the host's binary) | 0 issues each |
| `scripts/agent/d just generate` | exit 0; the tree stays clean |
| `scripts/agent/audit.py --base 6ac8d855 --head HEAD --report-only` | PASS, 0 hard flags |
| The spec diff and the probe of 195 requests on both commits | identical where it must be |
| Three overlay mutants | each fails the matrix |
| `just ci`, the e2e suite, `go test ./...` | not run, as asked: the batch gate runs them |

### Definition of Done

18 of the 18 items that apply hold:

- 5 of ADR-0329's Definition-of-done items, applied to this feature:
  - a second `just generate` changes nothing;
  - the Spec tab shows the section;
  - the list holds the other operations;
  - `/docs` shows the cases;
  - a run of one test passes.
- 8 of its checklist items, applied to this feature.
- The decider's 5 rulings.

Three items are not counted:

- the two items of `invokeFunction`, which belong to the pilot;
- `just ci`, which the batch gate runs; its parts above pass.

### Model scorecard

To record by the integrator: claude-opus-5-5 on ADR-0329 (implementation, the disaster recovery rollout) → pass,
0/0/2, 2 model-attributed, DoD 18/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Pass. The builder can fix the two Minors in this pull request: they change texts only, then `just generate`. The 422
example goes to the integrator, and `held: false` goes to the decider. Nothing is stamped: ADR-0329 is already
`Implemented`.
