# ADR-0206: Restore and held boot

- **Status**: Reviewing (2026-10-10; accepted 2026-10-10 by an `adr-batch` run after a clean `adr-judge` gate; the defaults below were not confirmed one by one)
- **Superseded in part by**: [ADR-0212](0212-app-self-heal-and-pause.md) (2026-10-10) — the App row of Decision 6: the deadline also counts from `resumedAt` and the last `preApply` `endTime`.
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: disaster-recovery, restore, hold, workflow, eventing, backup
- **Realizes**: [FEAT-0009/F109](../feat/0009-feat-disaster-recovery.md) (platform backup and restore; DR plan item DR-5)
- **Supersedes in part** (back-links at acceptance), for a held platform: ADR-0199 Decisions 4, 6, 7 (boot purge);
  ADR-0200 Decisions 3, 6 (and its deadline after a release, Decision 6 here), 7, 8.
- **Relates to**: ADR-0094, 0108, 0109, 0118, 0119, 0157 (runners) · ADR-0026, 0107, 0195 · ADR-0201–0205, 0207–0209

## Context & Need

Nothing reads back ADR-0202 to ADR-0204's sealed generations. A restored platform starts every runner: timers tick, blob
sources poll, and the run reconciler resumes each non-terminal WorkflowRun from its record, or from step 1 without one
(`(*RunReconciler).start`): side effects happen again (report §3). Purpose: an offline restore of a chosen generation
into empty directories, and one platform-wide hold keeping every side-effect runner still until released.

## Scenarios

- **scenario: restore-refuses-non-empty** — Given a metastore directory holding data, When `funcd restore run latest`
  runs, Then it exits with `fault.Conflict` naming the directory, having read and written nothing.
- **scenario: restore-crash-refuses-start** — Given `restore run` killed after the metastore `Load`, When funcd starts,
  Then it refuses with `fault.Conflict` naming the interrupted restore; once emptied, a new `restore run` boots held.
- **scenario: restore-keeps-data-owner** — Given a data directory owned by `funcd` (ADR-0026 §4), When root runs
  `restore run latest`, Then every entry it created is `funcd`'s, and funcd started as `funcd` boots held.
- **scenario: restore-boots-held** — Given G with a 1 s timer, a blob source, a Sensor and a run, When G is restored and
  funcd starts, Then G's objects list, `hold status` reports held, and for 5 s and a restart nothing fires or dispatches.
- **scenario: held-rollout-deadline** — Given G with an AppRevision `Deploying` past `app.upgradeTimeout`, When G is
  restored, released, funcd killed before the App runs, started twice, Then it fails a full timeout after the release.
- **scenario: restore-integrity** — Given G whose metastore part has another `sha256`, or a Secret that does not open
  with the key `secretsKey` names, When `restore run` runs, Then it fails naming the part or the Secret, data dir empty.
- **scenario: version-rule** — Given G written by 0.(m+1).0, When a 0.m.x binary restores it, Then `fault.Invalid` names
  both versions; G written by 0.(m-1).x is restored and the start steps run at its first start.
- **scenario: restore-points** — Given T1/40, T1/41 and T2/43 (T2 restored from T1/40), When `restore list` runs, Then T2
  is under T1/40, T1/41 is `abandoned`, `latest` is T2/43; `T1-<revision of 41>`, `T2-<revision of 43>` are T1/40.
- **scenario: single-secret-restore** — Given Secret a/s deleted after G, When `inspect --object Secret/a/s` runs, Then
  it prints a/s by key, values `REDACTED`; with `--secrets-key --reveal-secrets`, `funcdctl apply -f` brings a/s back.
- **scenario: runs-held-as-evidence** — Given G with non-terminal runs R (record at step 2) and S (no record), When G is
  restored and released, Then both stay `Paused`; `workflow resume R` runs from step 2; `cancel S` runs no step.
- **scenario: dead-letters-held** — Given G with dead letter d1, When restored, Then `funcdctl eventing dlq list` shows
  d1 by its id, its replay is `fault.Unavailable` while held, nothing redelivers it after release, its replay then works.
- **scenario: blob-replay-or-advance** — Given seen list {a, b}, bucket {a, b, c, d}, When released, Then c, d fire once;
  with `--advance` none fires, a to d seen; after a blob restore (new ModTime), `hold status` has 4 pending.
- **scenario: first-drill** — Given a seeded platform backed up, When the drill restores it into a scratch directory,
  Then the integrity checks pass, counts per kind equal the source's, a read of each kind works, and the time is logged.

## Scope

**In**: restore commands, points, listing, version rule, single object, master-secret step, recovery order, registry
rule; the hold, its runners and data reclaims, evidence, blob replay or advance, release; conformance test, first drill.
**Out**: snapshot, timeline (ADR-0202); event store (ADR-0201); format (ADR-0203); keys (ADR-0204); operation (ADR-0205);
safe mode (ADR-0207); blob, KV readers and their `restore` subcommands (ADR-0208, ADR-0209); workload restore (DR-10).

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q3 (runs and dead letters held evidence; the operator resumes, replays or cancels), Q4
  (one platform-wide hold at the runners, an explicit release; per-kind flags stay operator tools; not built on the
  WorkflowRun flag until a test shows it blocks a run with no record), Q5 (replay by default, advance per source at
  release), Q9 (registry outside DR), Q10 (same or older minor), Q14. Fail closed; no new module.

## Alternatives considered

| Option | Outcome |
|---|---|
| **One gate the runners ask; a marker file; a live release** ✅ | Chosen: one switch (Q4), survives restarts, edits no object for the hold |
| A paused flag set on every Sensor, EventSource and run | Rejected by Q4: the flags edit desired state and the hold edits none (pausing runs is Q3's evidence rule); a kind without a flag fires |
| Release = delete the marker and restart | Rejected: downtime, and `--advance` needs the running watcher |
| Online restore through the API | Rejected: it needs the read credential and the identities on the running daemon (Q7, Q8), and it would load into a store that is serving |

## Decision

**1. Commands** (names and flags proposed; accepted as default, not confirmed by the decider), all NEW. Offline work is a daemon subcommand
beside `version` (`cmd/funcd/main.go` `newRootCmd`); work on a running daemon is a funcdctl noun group as `workflow`:

| Command | Does |
|---|---|
| `funcd restore list`; `funcd restore inspect <point>` | generations as a lineage tree (Decision 3), then KV points and blob generations (rows ADR-0209 and ADR-0208 add); manifest, keys needed, version verdict, counts per kind, evidence, `--diff <point>`, `--object <Kind>/<ns>/<name>` (Decision 5); with `--kv`, `<point>` is `<chain>/<segment>`; a blob generation is listed, not inspected (ADR-0208 has no manifest reader); both write nothing |
| `funcd restore run <point>`; `funcd restore kv [<chain>/<segment>]`; `funcd restore blob [<generation>]` or `--at <RFC 3339>` | the restore (Decision 2); the KV instance at a point, default the newest restorable one (ADR-0209 `ListPoints`, `RestoreDir` into `kvstore.dataDir`); the blob store at a generation, default the newest complete one of `blobmirror.List`, by `blobmirror.Restore` into the configured store (`blob.target`, else `blob.dir`) opened with `--store-credentials-file`, or with `--at` by `RestoreAt` of a `blob.Versioned` store, else `fault.Invalid` (ADR-0208); Decision 5 |
| restore flags | `--config`, `--from` (default `backup.target`; for `restore kv`, ADR-0209's `ResolveKVBackup`), `--credentials-file` (the operator's restore credential, ADR-0203 Decision 5, never `backup.credentialsFile`), `--store-credentials-file` (`restore blob` only: opens the configured store as the destination; default `blob.credentialsFile`; with `--at` it also needs `s3:ListBucketVersions`, `GetObjectVersion`, ADR-0208 Decision 6), `--identity` (repeatable age identity file), `--escrow` (directory), `--timeline <t>` (Decision 3), `--reveal-secrets`, `--secrets-key` (`--object` only), `-o yaml` or `-o json`; `--new-master-secret` (ADR-0204) |
| `funcdctl hold status`; `funcdctl hold release [--advance <ns>/<source>]…` | the hold's evidence: marker, report, counts now, paused runs, dead letters, `Pending` blob keys, data with no object (`Orphans`, `bucketOrphans`), Functions not Ready; lifting it (Decision 8) |
| recovery order; first drill | escrow keys, `restore run`, `restore kv`, `restore blob`, start held, read `hold status`, release; the drill (test and runbook) is the first-drill scenario with `server.network.egress: true` (ADR-0115; outbound off on Linux), booted held and never released; its integrity checks are steps 2.2 and 2.3, its duration an RTO input |

**2. `restore run`**, in order; an error removes what it created and exits non-zero:
1. `config.Load`; `storage.mode: memory` ⇒ `fault.Invalid`; the metastore, run-state, event-store directories absent or
   empty, else `fault.Conflict` naming one. Resolve the point, `backup.ReadManifest`; Decision 4; a `format` above
   `backup.Format` ⇒ `fault.Invalid`. ADR-0204 Decision 5: `ReadIdentities`; `envelope.Opener(ids)(m.Recipients)`, the
   recipient match, once (else `fault.Invalid`); `CheckSecretsKey`; `escrow.PlanMaster` (`s3gateway.masterSecretFile`,
   `storage.dataDir`, `--escrow`; refuses unless `--new-master-secret`; writes nothing). Then `hold.Begin`, `hold.Write`.
2. Per store in cut order: every part present with the manifest's `sha256`, else `fault.Invalid` naming generation and
   store; step 1's `Unseal`, `backup.Records` into ADR-0202's `snapshot.Loader` (`Load`; on the metastore
   `store.Engine`, which skips the timeline record; ADR-0201 for events).
3. `store.New` on the loaded engine mints a new timeline (ADR-0202 Decision 3).
   Canary: `checkSecretsDecode` (`cmd/funcd/main.go`) on the restored Secrets with the configured key; no Secret warns.
4. Each non-terminal WorkflowRun not already paused gets `spec.paused: true` (`store.Update`, Decision 7). Step 1's
   `MasterPlan`: a non-nil `Install` is written 0600 to `Path`; with `List`, `escrow.ListChanged` on the loaded store
   prints what changes (as "may change" when the manifest has no `masterSecret`).
5. `restore.json` (0600, `fsync`ed); the stores closed, `hold.Own` gives all the restore created `<storage.dataDir>`'s
   owner (ADR-0026 §4); `hold.End`. A kill before it leaves `restore.inprogress`; `hold.Open` then refuses the start
   (`fault.Conflict`: empty, rerun), as `store.New` would mint a timeline over a partial `Load`.

**3. Points and listing** (proposed; accepted as default, not confirmed by the decider). A generation is ADR-0203's (timeline, n):

| `<point>` | Resolves to |
|---|---|
| `latest`; an RFC 3339 time | the newest complete generation by `at` not `Abandoned` (ADR-0203); the same with `at` at or before the time; when the qualifying complete generations come from two timelines neither of which descends from the other by `parent`, `fault.Conflict` naming both timelines; `--timeline <t>` (NEW) keeps only t and the timelines it descends from |
| `<timeline>-<n>` (a resourceVersion) | the newest complete generation of that timeline with `revision` below n: the state before that write; none ⇒ the timeline's `parent` |
| `<timeline>/<n>`; `pre-upgrade` or `verified` | that generation; the newest complete one of that pin class, with the same two-timeline rule and `--timeline` |

`list` prints a row per generation (timeline, n, class, `at`, `funcd`, state `complete`, `incomplete`, `abandoned` or
`newer`), each timeline indented under its `parent`. ADR-0205 takes a generation's `parent` from `restore.Parent`.

**4. Version rule** (Q10). The manifest's `funcd` and `version.Version` compare by `semver.MajorMinor`: the same or older
restores, newer is `fault.Invalid` naming both; when either is not valid semver (`dev`, or a bare hash:
`scripts/build.sh`'s `git describe --tags --always --dirty` in a clone without tags), it restores, warning naming both.
An older generation runs the start steps as at an upgrade (`workflow.MarkKVStoresOnce`, ADR-0201's move, ADR-0202's
legacy versions); until a migration framework exists, generations from ADR-0205's minor on restore.

**5. Single object, KV, blob, registry.** `inspect` reads into memory engines (ADR-0202), never the data directory;
`--object` prints one object without `uid`, `resourceVersion`, creation time and status, so `funcdctl apply -f`
recreates it with a new uid (Q14; v1 applies ConfigMaps and Secrets). Secrets are encrypted whole
(`store.WithEncryptor`): without `--secrets-key` (the escrowed `secretsKey` file, `escrow.CheckSecretsKey`), they print
and count by key (`keyFor`, values `REDACTED`); values need `--reveal-secrets` too. `--diff <point>` lists per kind the
objects added, removed or changed (`Export` forms differ) since it. `restore kv` and `restore blob` take the restore
flags and `envelope.Opener(ids)`, refuse a memory store (`fault.Invalid`) and, but for `--at`, a non-empty destination
(`fault.Conflict`), empty it on error, run between `hold.Begin(storage.dataDir, "kv"|"blob")` and `hold.End`, and pass a
local one to `hold.Own`. Registry (Q9): nothing read or pulled; a Function lacking its artifact is not Ready.

**6. The hold** (Q4). `<storage.dataDir>/.hold` exists iff the platform is held; `restore run` and ADR-0207 write it,
only the release deletes it (a failed restore, its own). `serve` (`cmd/funcd/main.go`) opens it (`hold.Open`) before `buildOptions`, after ADR-0207's `safemode.Begin` and any safe-mode `hold.Write`, and passes it to `funcd.WithHold` (default
`hold.Never`) and to each backup loop's `Hold` (ADR-0205 runner, ADR-0208 `blobmirror.Config`, ADR-0209 `BackupConfig`); API, controllers and data plane serve. Each runner asks `Held()`; per-namespace holds are DR-10's.

| Runner | While held | After the release |
|---|---|---|
| timers, `(*Source).Run` | the tick skips `dueTimers` | at most one fire per timer (`dueTimers` keeps the latest boundary) |
| blob sources, `(*BlobWatcher).Run` | no `sweep`, no `poll` | `Advance` per `--advance`, then sweep and polls replay what seen lists lack |
| Sensors, `(*Reconciler).deliver` | `fault.Unavailable`: retried, then parked (a guard; no publisher runs held) | delivers |
| dead letters, `(*Reconciler).Replay` | `fault.Unavailable` | operator-run as today; nothing redelivers by itself (ADR-0118) |
| runs, `(*RunReconciler).Reconcile` | no start, resume or replay; `RequeueAfter: waitRequeue`; status untouched | a run created while held starts |
| `runWorkflowRetention`, `runDeadLetterRetention` | skipped: they delete evidence | sweep |
| data reclaims: the boot calls in `(*Platform).Run` of `(*kv.Reconciler).ReclaimDeleted` (#708) and, right after it, `reclaimDeletedBuckets` (ADR-0199 Decision 7; `pkg/funcd/funcd.go:1325`, `:1329` at main c35bdf5e); the KV reconciler's `reclaimOrphanTables` | skipped: KV or blob data newer than the restored metastore, or left intact, may have no KVStore, table or Bucket yet; `hold status` lists it: `Orphans`, and `bucketOrphans`, the walk split out of `reclaimDeletedBuckets`: each `<ns>/<bucket>` with objects under `bucketPrefix` (`s3/<ns>/<bucket>/`) and no Bucket, on each of which the boot purge calls `bucketPurger.Purge` | as today, the boot ones at the next start (proposed; accepted as default, not confirmed by the decider); the operator applies, while held, the objects whose data stays |
| backups: ADR-0205 `(*Runner).Run`, ADR-0208 `Mirror.Loop`, ADR-0209 `RunBackup`, each through its config's `Hold` (`interface{ Held() bool }`, nil ⇒ never held), set from the daemon's `*hold.Hold` | skipped (proposed; accepted as default, not confirmed by the decider): a held drill writes no generation that turns the source's later ones `Abandoned` (ADR-0203) | the next due run; the platform runner's `parent` from `restore.Parent`; a copy still naming its source's `backup.target` is refused there and refuses its source (ADR-0203 Decision 4), so a drill moves `prefix` before its release |
| App reconciler and its rollouts (ADR-0199, ADR-0200; F117's hooks, unbuilt, run in it per FEAT-0010), through `app.Deps.Hold` | `(*Reconciler).Reconcile` first returns `RequeueAfter: SupervisionPeriod`, status untouched, as runs: no apply (self-heal included), prune, stamp, switch, history deletion or `Failed`, so the rollout deadline (`startedAt`, set at the stamp, + `app.upgradeTimeout`, ADR-0200 Decision 6) cannot run out while held | `(*Reconciler).deadline` is max(`startedAt`, `ReleasedAt`) + `app.upgradeTimeout`, read again after a restart (proposed; accepted as default, not confirmed by the decider): a `Deploying` AppRevision gets a full timeout after the release, and the App reconciler stays its only writer (ADR-0200 Decision 1); one `Failed` before the hold stays `Failed` |

**7. Evidence** (Q3). A restored non-terminal run is paused (step 2.4); the operator runs `funcdctl workflow resume`
(from its record, or step 1 without one: no step had dispatched, ADR-0202 Decision 2), `cancel`, or `cancel` then
`replay --from` (`(*Engine).replay`: `SeedInvalid` unless terminal). `Reconcile` checks `spec.paused` before it starts a
run without a record; the hold never reads the flag. Records without a WorkflowRun stay inert, listed, never deleted.

**8. Release and status** (proposed; accepted as default, not confirmed by the decider). `POST /apis/funcd.io/v1alpha1/hold/release`
authorizes `update`, `GET /apis/funcd.io/v1alpha1/hold` `get`, on `WorkerNode` as ADR-0205's route (cluster-scoped,
admin-only under the built-in RBAC, `internal/auth/rbac`). Not held ⇒ `fault.Conflict`; an `--advance` naming no blob
event of an existing EventSource ⇒ `fault.Invalid`; both before any change. Then `Advance` per source, rerun-safe: a
failure returns, the marker kept, and a rerun completes; `Release` writes the time (daemon clock) to `.hold-released`,
deletes the marker (`fsync` of the directory), lifts the gate. `hold status` shows Decision 1's evidence.

## Temporary workarounds

None.

## Contracts

```go
package hold // internal/platform/hold (NEW)
const MarkerFile, BusyFile, ReleasedFile = ".hold", "restore.inprogress", ".hold-released" // Busy: Decision 2
type Marker struct{ Reason string `json:"reason"`; Since v1.Timestamp `json:"since"` } // "restore" | "safe-mode"
type Gate interface{ Held() bool; ReleasedAt() time.Time }; var Never Gate = never{} // never: not held, zero time
type Hold struct{ dir string; mu sync.RWMutex; m *Marker; released time.Time } // a Gate; Open reads both files
func Write(dataDir string, m Marker) error; func End(dataDir string) error // 0600, fsync of file and directory; End removes BusyFile
func Begin(dataDir, command string) error  // BusyFile naming the command (run, kv, blob), as Write; another's ⇒ fault.Conflict
func Open(dataDir string) (*Hold, error)   // BusyFile ⇒ fault.Conflict; no marker ⇒ not held; unreadable ⇒ fault.Internal
func Own(ref string, roots ...string) error // os.Lchown each root, if it exists, and all below to ref's uid, gid where they differ
func (h *Hold) Marker() (Marker, bool); func (h *Hold) Release(now time.Time) error // Decision 8: ReleasedFile, no marker
package restore // internal/restore (NEW)
type Point struct{ Latest bool; At time.Time; Version store.Version; Gen *backup.GenRef; Pin backup.Class; Timeline string }
type Generation struct{ Manifest backup.Manifest; Class backup.Class; State string }
type Report struct{ From backup.GenRef; Timeline, Funcd string; At v1.Timestamp; Counts map[v1.Kind]int // restore.json
	PausedByRestore, AlreadyPaused, RecordsWithoutRun []string } // runs as <ns>/<name>
type Options struct{ Config config.Config; Source blob.Bucket; Identities []age.Identity; EscrowDir string
	NewMasterSecret bool; SecretsKey []byte; Out io.Writer } // Source: OpenWith, restore credential; SecretsKey: --secrets-key
type View struct{ Meta store.Store; Runs runstate.Store; Events *eventstore.Store; Secrets []v1.ObjectRef } // memory engines
func ParsePoint(s string) (Point, error)                         // Decision 3; malformed ⇒ fault.Invalid
func List(ctx context.Context, src blob.Bucket) ([]Generation, error)
func Resolve(gens []Generation, p Point) (Generation, error) // none ⇒ fault.NotFound; unrelated timelines ⇒ fault.Conflict
func Run(ctx context.Context, p Point, o Options) (Report, error); func CheckVersion(writer, binary string) error // Decision 4
func Inspect(ctx context.Context, g Generation, o Options) (*View, error) // SecretsKey nil ⇒ View.Secrets by key only
func Diff(from, to *View) (added, removed, changed map[v1.Kind][]v1.ObjectRef, err error)
func Export(obj v1.Object, reveal bool) (v1.Object, error)       // !reveal ⇒ Secret values REDACTED
func Parent(dataDir, timeline string) (*backup.GenRef, error)    // the report's From while its Timeline is current
// internal/eventing, NEW, no publish: Advance marks listed keys seen; Pending counts, per event, keys whose versionOf is unseen
func (w *BlobWatcher) Advance(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) error
func (w *BlobWatcher) Pending(ctx context.Context, ns v1.NamespaceName, source v1.ObjectName) (map[string]int, error)
func (r *Reconciler) Orphans(ctx context.Context) ([]string, error) // NEW, internal/services/kv: what the reclaims would drop
func bucketOrphans(ctx context.Context, shared blob.Bucket, st store.Store) ([]v1.ObjectRef, error) // NEW, pkg/funcd
Hold interface{ Held() bool; ReleasedAt() time.Time } // NEW in app.Deps, nil ⇒ never held; deadline: max(startedAt, ReleasedAt)+timeout
```

| consumes | exposes |
|---|---|
| ADR-0201 `eventstore.Store` (`Load`); ADR-0202 `snapshot.Loader`, `store.Version`; ADR-0203 `List`, `ReadManifest`, `Records`, `Manifest`, `GenRef`, `Class`, `Abandoned`, `Format`; ADR-0204 `envelope.Opener`, `ReadIdentities`, `CheckSecretsKey`, `PlanMaster`, `ListChanged`; ADR-0199, ADR-0200 `app.Deps`, `(*Reconciler).deadline` (`internal/app/revision.go`), AppRevision `status.startedAt`; `internal/platform/clock`; ADR-0196 `v1.Timestamp`; `reclaimDeletedBuckets`, `bucketPurger`, `bucketPrefix` (`pkg/funcd/funcd.go`); ADR-0026 §4 unit (`User=funcd`); `golang.org/x/mod/semver` (indirect → direct) | the Decision 1 commands and flags; the two routes; `<dataDir>/.hold`, `.hold-released`, `restore.inprogress`, `restore.json`; `hold.Gate` (as `app.Deps.Hold`, `ReleasedAt` for the deadline), `funcd.WithHold`; `hold.Begin`/`End` and the restore flags (`--store-credentials-file` for `restore blob`) for ADR-0208 `restore blob` and ADR-0209 `restore kv`; `hold.Own` (`ref`: the data directory; ADR-0207 also passes a `file://` target directory) for ADR-0207 (upgrade, safe-mode reset), ADR-0208 `restore blob`, ADR-0209 `restore kv`; `Orphans`, `bucketOrphans` |

## Implementation plan

**Files**: NEW `internal/platform/hold/hold.go`, `cmd/funcd/restore.go`, `cmd/funcdctl/hold.go`, `pkg/sdk/hold.go`,
`internal/restore/{point,list,run,inspect,version}.go`, `internal/controlplane/hold.go`, `examples/restore-runbook.md`
(recovery order, drill); `cmd/funcd/main.go` (`serve`: `hold.Open` before `buildOptions`); `pkg/funcd/{options.go,funcd.go}`; `internal/app/{reconcile,revision}.go`;
`internal/workflow/reconcile_run.go`; `internal/eventing/{eventing.go,blobwatch.go}`; `internal/sensor/sensor.go`;
`internal/services/kv/reconcile.go`. **Order**: ADR-0207 to ADR-0209 follow; ADR-0208 and ADR-0209 wire their loop's
`Hold`, restore subcommand and `TestEveryRunnerConsultsHold` case; ADR-0205, if later, its runner and case.
**go.mod**: `golang.org/x/mod` direct. **Blueprint** (at acceptance): the DR bullet gains "offline restore, held boot".

**Test plan**: one `TestScenario<Name>` per scenario, platform ones on `shortDataDir`, `TestScenarioFirstDrill` in
`tests/e2e`, the owner one as root on Linux (a second uid owns the data), else skipped. Units: `TestParsePoint`,
`TestResolve` (unrelated timelines, `--timeline`), `TestCheckVersion` (`dev`, `abc1234`, `abc1234-dirty` as writer or
binary: restores, warning; `v0.9.0-9-gabc1234-dirty` under v0.8: `Invalid`), `TestExportRedacts`, `TestInspectDiff`,
`TestHoldMarkerRoundTrip`, `TestOwn`, `TestOrphans`, `TestBucketOrphans`, `TestReleaseRefuses` (developer `Forbidden`,
unknown `--advance` `Invalid`, not held `Conflict`, nothing changed; a failed `Advance` keeps the marker). Conformance:
`TestPausedRunWithoutRecordStaysStill` (10 passes, no record, no dispatch; Q4's precondition) and
`TestEveryRunnerConsultsHold` (each Decision 6 runner built by then, the App's included, held; fails when one acts).
**Definition of done**: `scripts/agent/d go test -race -count=1` and `just ci`, the e2e lane green; no identity leak.

## Review checklist

- [ ] `restore run`: memory mode, directories, recipient match, keys, `Begin`, the marker, before any part is read;
      `sha256` before `Load`; canary on the new timeline; `Own`, then `End`; an error empties them; a kill blocks start.
- [ ] Every Decision 6 runner asks the gate; no hold logic reads `spec.paused`; release refuses before any change.
- [ ] Inspect writes nothing and prints no Secret value without `--reveal-secrets`; each scenario has its named test.

## Consequences

**Positive**: nothing restored fires before the operator decides; evidence survives; one command reads every point.
**Negative (accepted)**: one operator action per restored run; ingress and `minScale` workers serve while held; no backup
while held; `replay --from` of a held run needs a `cancel` first. **Risk**: a later runner without the gate fires held.

## Open questions

| Item | Recommended default (proposed; accepted as default, not confirmed by the decider) | Why |
|---|---|---|
| Command names, flags; lineage display; blob `inspect` | Decision 1; a tree indented by `parent`, `abandoned` marked (Decision 3); blob generations listed, not inspected | the daemon owns offline work, funcdctl the API verbs; a restore leaves later generations on a dead branch; ADR-0208 has no manifest reader (else it adds a `ReadManifest`) |
| Where the hold lives; runner checks; what release verifies | the marker in `storage.dataDir`; `hold.Gate` per runner (Decision 6); held, valid `--advance`, and `hold status`, with pending blob keys, is the operator's check | travels with the data; one switch; funcd cannot judge the real world |
| Backup runners, data reclaims while held | skipped until the release; `hold status` lists the data with no object; the boot reclaims run at the next start after it | a drill or unverified restore must not branch the source's lineage, nor drop data the restored metastore does not name yet; running backups would protect changes made while held; for reclaims, alternative (ADR-0209): none after a restore until the operator runs them over the listed set, which keeps #708 open on a restored node and needs a reclaim command, offline or racing a live create (`ReclaimDeleted`'s comment, `internal/services/kv/reconcile.go`) |
| Split | one ADR | restore without the hold is unsafe; the hold alone has no caller but ADR-0207 |
| Registry digest list (Q9, not decided) | none in v1; Functions show not Ready | no registry logic |
| One function for which stamps order (Decision 4) | `CheckVersion` calls `semver.IsValid` and `semver.MajorMinor` itself and cites no ADR-0207 symbol; the alternative: this ADR defines `version.Compare(a, b string) (c int, ok bool)` (NEW, `internal/platform/version`, ADR-0207's contract comment), `CheckVersion` uses its `ok`, and ADR-0207 consumes it (a paired edit there) | ADR-0207 builds after this ADR; both accept the same stamps (any from a tagged tree, `-<n>-g<hash>-dirty` included; not `dev` or a bare hash), and two library calls hold no ordering logic to drift |
| Held App rollout deadline (ADR-0200 Decision 6; downtime counts) | restart it at the release, with the App reconciler the only AppRevision writer (ADR-0200 Decision 1): `Release` persists its time (`.hold-released`), the reconciler takes the deadline as max(`startedAt`, `ReleasedAt`) + `app.upgradeTimeout` (Decision 6); alternatives: the release sets each `Deploying` `startedAt` to now (the move F122 makes, ADR-0200 Open question 2), which also supersedes ADR-0200 Decision 1 (a second writer); pause it (subtract held time, a sum ADR-0200 does not store); let it expire, the operator runs F117's `retry` | the release is the operator's decision to continue (Q3, report §4.L row 6); a hold is no failed rollout; the persisted time survives a restart before the first un-held pass, keeps one writer, and the hold edits no object |

## References

- `docs/reports/platform-disaster-recovery-design.md` §3, §4.A, §4.C, §4.I row 5, §4.L rows 5-6, §5 (Q3, Q4, Q5, Q9, Q10,
  Q14); FEAT-0009. Licence checked 2026-10-08 in the module cache: `golang.org/x/mod` v0.36.0 BSD-3-Clause.
