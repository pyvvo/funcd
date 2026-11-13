## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1441 batch 4, model: claude-opus-5-5)

This is round 2 of this review. Round 1 reviewed commit 189557b6 and found 0 blockers, 2 majors and 11 minors. Round 2
reviews commit 8bd101ba `docs(config): every key of funcdconfig.yaml has a description`, which replaces 189557b6 on the
same parent, 57c87b4f (the third batch, reviewed separately). The commit has 25 files, 652 insertions and 532 deletions.
`git diff 189557b6 8bd101ba` touches four files and no other: 20 tags in `internal/platform/config/config.go`, the 20
rows that they give on `docs/guide/develop/running-funcd/spec.md`, two pins in `cmd/funcd/doc_test.go`, and one check in
`internal/testkit/doctag/doctag.go`. Every line of the tags stays on its line, so the line numbers of round 1 still hold.

The commit gives a `doc` tag to the 141 keys of `funcdconfig.yaml` that `internal/reference/testdata/undescribed-config.txt`
listed at 57c87b4f and regenerates the funcd Spec page. It pins the tags that copy a named constant, in 16 packages,
through the new `doctag.NamesAt`. It ends the temporary workaround of ADR-0323 and ADR-0324: the list goes with its
exemption in `TestEveryConfigKeyIsDescribed`, and `scripts/check-undescribed.sh` goes with its test and its CI step. In
round 1, each description was checked against the code that reads its key: `Validate` and `Defaults()` in
`internal/platform/config`, `cmd/funcd/main.go`, the packages that apply the value, and the `docs/behavior/` rows that cite
the key. In round 2, the 20 changed tags were checked again in the same way. This review ran in an agent separate from the
session that wrote the work. Every check below was run, not read.

### Blockers

None.

### Majors

None. Both majors of round 1 are fixed (see the next section).

### Minors

None. The eleven minors of round 1 are fixed. For Minor 1, the five defaults that need a code change are routed, as round
1 proposed.

### Round-1 findings in 8bd101ba

- **Major 1, `storage.quarantineRetention` (round 1: it promised a return that a SQL database does not have). Fixed.**
  The tag now says "Once the object exists again and holds no data, funcdctl quarantine return brings a KVStore's, a
  Bucket's or a volume's entry back into it; a SQL database's directory goes back by hand, with funcd stopped"
  (`config.go:132`). This matches the verb's kinds and its guard (`cmd/funcdctl/quarantine.go:28-30`, `:74`), the
  server's refusal (`pkg/funcd/quarantine.go:413`), and ADR-0288's choice and steps by hand
  (`docs/adr/0288-a-boot-reclaim-quarantines-data-no-object-names.md:75`, `:81`, `:127`). The pin on 1h and 168h passes.
- **Major 2, `runtime.containerd.socket` (round 1: it said "supervises", but nothing restarts the private containerd).
  Fixed.** The tag now says "Empty means funcd starts a private containerd of its own in runtime.containerd.root, which
  needs funcd to run as root, and stops it at shutdown; funcd does not start it again if it dies" (`config.go:281`). This
  matches `docs/behavior/workers.md:55` and `internal/runtime/ctrmanager/manager_linux.go:60-82`, `:160-172`.
- **Minor 1, seven pins that guarded a constant that does not set the daemon's value. Fixed as proposed.**
  `cmd/funcd/doc_test.go` now pins `runtime.bootBackoffMax` from the limit that `bootBackoff(config.Defaults())` returns,
  and `runtime.readinessTimeout` from `pacing(config.Defaults())`. Run: an overlay that changes the default in
  `bootBackoff` to `6*time.Minute` fails with `the doc tag of runtime.bootBackoffMax does not name "6m"`, and one that
  changes `runtime.readinessTimeout`'s default in `pacing` to `6 * time.Second` fails with `the doc tag of
  runtime.readinessTimeout does not name "6s"`. The other five literals (`kvstore.backup.interval`,
  `kvstore.backup.rebaseline`, `kvstore.backup.rebaselineRetry`, `kvstore.cdc.retention` and `sql.callTimeout`) need a
  code change and are routed: an overlay that changes `main.go:806` to `60*time.Second` still passes, as expected.
- **Minor 2, `backup.target` and the "KV export" wording. Fixed.** `backup.target` now says "and so does the KV backup
  when kvstore.backup.enabled is true and kvstore.backup.target is empty" (`config.go:187`). Both encryption keys now say
  "The KV backup takes this group too while kvstore.backup.encryption sets neither of its keys" (`config.go:201-202`),
  which is the rule of `KVEncryptionKeyPrefix` (`internal/platform/config/kvbackup.go:48-53`). No tag says "KV export"
  any more (grepped).
- **Minor 3, `storage.dataDir` and the unit's variable. Fixed.** The tag now ends "The systemd unit that funcd install
  writes sets it through the variable, which wins over the file" (`config.go:125`; `cmd/funcd/install.go:55`,
  `internal/platform/config/config.go:644`).
- **Minor 4, the negative value of `backup.retention.daily` and `backup.retention.weekly`. Fixed.** Each tag now ends "A
  negative value refuses the start while backup.target is set" (`config.go:192-193`;
  `internal/platform/config/backup.go:147-148`).
- **Minor 5, `app.upgradeTimeout`. Fixed.** The tag now says "counted from the latest of its start, the App's resume, a
  hold's release and the end of its last pre-hook call" (`config.go:463`), which is the deadline of
  `internal/app/revision.go:230-249`. The pin on 5m passes.
- **Minor 6, `volumes.capacityBytes`. Fixed.** The tag now says "an apply of a Function with a volume is then refused, and
  one stored before turns Failed with the reason VolumesOff" (`config.go:455`;
  `internal/controlplane/admission/volumes.go:39`, `internal/function/volumes.go:52-54`). The pin on VolumesOff passes.
- **Minor 7, "the Lakes' query proxies" in `runtime.containerd.bridgeAddress`. Fixed.** The tag now says "the Lake
  gateways" (`config.go:289`), the guide's name (`docs/guide/platform/services/lake.md:6,13`).
- **Minor 8, the elliptical clause of `runtime.handOutSettle`. Fixed.** The tag now says "stops no sooner than this after
  the switch and after the last call handed to it" (`config.go:263`; `internal/function/function.go:2117-2119`).
- **Minor 9, "keeps" in `backup.retention.preUpgrade`. Fixed.** The tag now says "counts as kept" and "funcd upgrade lists
  the older ones past that number for the operator to prune" (`config.go:196`; `internal/upgrade/upgrade.go:155-157`).
- **Minor 10, examples that repeated the printed default. Fixed.** `backup.interval`, `backup.retryInterval`,
  `backup.objectives.rpo`, `recovery.safeMode.stableAfter`, `lake.enginePollInterval` and `lake.engineProbeTimeout` no
  longer give an example. `workloadBackup.maxListedKeySize` now gives 64MiB, which is not its default of 256MiB
  (`internal/platform/config/workloadbackup.go`) and is a valid size in the grammar.
- **Minor 11, `doctag.NamesAt` panicked on a path through a leaf key. Fixed.** Before each lookup, `NamesAt` now fails
  with `t.Fatalf` when the owner is not a struct (`internal/testkit/doctag/doctag.go:29-31`). Run: a probe test that calls
  `NamesAt` with `storage.mode.x`, added through an overlay, fails with `Config has no key storage.mode.x: storage.mode
  is not a group of keys`, and with no panic.

### Routed (not this work's)

- **Name the daemon's defaults in `cmd/funcd/main.go`.** Five literals still repeat named constants of other packages:
  `kvstore.backup.interval`, `kvstore.backup.rebaseline` and `kvstore.backup.rebaselineRetry` (`:806`, `:810`, `:814`),
  `kvstore.cdc.retention` (`:901`) and `sql.callTimeout` (`:560`). A named constant, or the other package's own
  constant, would let a pin guard the value that funcd applies. This is a code change, and it goes to the decider.
- **Three values that the tags copy have no name.** `7075` (`sql.changeFeedPort`; `cmd/funcd/main.go:1166`,
  `pkg/funcd/binding_providers.go:505`), `ScratchSpaceLacking` (`lake.scratchSize`;
  `internal/services/lake/reconcile.go:162`) and `PayloadLimitExceeded` (`workflow.payloadLimit`;
  `internal/workflow/engine.go:299`, `internal/workflow/reconcile_run.go:612`) are literals, so no
  `TestDocNamesTheConstant` can pin them. Named constants would let these tags be pinned.
- **Does funcd export its own logs over OTLP? Surprising, not checked against the ADRs.**
  `docs/guide/platform/observability.md:7` and `:10-11` say that funcd exports its traces, metrics and logs.
  `NewTelemetry` builds an OTLP log handler (`internal/platform/observability/telemetry.go:111-118`) that nothing calls
  (`LogHandler`, `:146`), and `buildLogger` writes only to standard output (`cmd/funcd/main.go:673-679`). The tag of
  `telemetry.endpoint` says metrics and traces, which is what the code does. The guide page is for its owner.
- **The private containerd's restart.** The divergence that the fixed Major 2 keeps out of the tag stays with
  `docs/behavior/workers.md:55` (`differs`, ADR-0054 Decision 3). The Go comments that say "supervises"
  (`internal/runtime/ctrmanager/manager.go:10,40,55`, `manager_linux.go:30,56`) and the unit's comment
  (`cmd/funcd/install.go:35`) go with that row.

### Verified correct (keep it)

- **Generated files, round 2.** On commit 8bd101ba exported with `git archive`, these three steps leave the tree
  identical to the commit (`diff -r` against a second export finds no difference): `go run
  ./internal/controlplane/cmd/specgen/ -out api/openapi/funcd.v1alpha1.yaml -guide docs/guide`, `go test ./cmd/funcd -run
  '^TestScenarioConfigSpecGenerated$' -update`, and `go test -tags dev ./cmd/funcdctl -run
  '^TestScenarioCommandSpecGenerated$' -update`.
- **Tests and checks, round 2.** `go test -count=1` passes for the packages that changed since round 1 (`cmd/funcd`,
  `internal/platform/config`, `internal/reference`; `internal/testkit/doctag` has no test files). Under `-race`,
  `TestDocNamesTheConstant` passes in all 16 packages, and so do `TestEveryConfigKeyIsDescribed`,
  `TestScenarioUndescribedConfigKeyRefused` and `TestScenarioConfigSpecGenerated`. `go vet` is clean, and
  `golangci-lint run` reports 0 issues on the changed packages. In round 1, the 19 touched packages passed `go test
  -count=1` (`scripts` in a git-initialized export, because its guard tests need a repository); no file of those packages
  other than the four above changed since.
- **The refusals still work (overlay mutants).** Round 2: without the tag of `storage.mode`,
  `TestEveryConfigKeyIsDescribed` fails with `keys with no description, which need a doc tag written for users:
  storage.mode`. Round 1: with an ADR number in the tag of `apiVersion`, it fails with `apiVersion: a description is
  written for users and names no ADR`; without the tag of `FunctionSpec.handler`, `TestEveryFieldIsDescribed` fails and
  names `AppSpec.functions[].handler, FunctionSpec.handler`; with `DefaultKVSweepInterval` changed to 2s, the config
  package's `TestDocNamesTheConstant` fails and names `kvstore.sweepInterval`.
- **The removal.** The list file, its `-update` flag, `readList` and `checkList`, `scripts/check-undescribed.sh`,
  `TestScenarioUndescribedListNeverGrows` and the CI step are gone, as the exits in the Temporary workarounds of
  ADR-0323 and ADR-0324 say. No tracked file names them except those two ADRs, which are frozen as Implemented, and
  earlier review reports. `scripts/generated_test.go` keeps the helpers that `TestScenarioStaleGeneratedFileFailsCI`
  still uses, the next step of `.github/workflows/ci.yml` is intact, and `.claude/rules/config-and-cli.md:20-21` now
  says that the test refuses a key with no tag.
- **The text of the tags.** There are 141 tags for the 141 listed keys. None holds a backtick, an ADR or issue number, a
  Go identifier, a package or file name, or a path of the development machine (grepped in both rounds). Every config key
  that a tag names exists under that name: in round 2 too, a script matched each dotted name against the 212 keys of the
  page. The words that could read as internal are the guide's own: shim, metastore, owner collector, segment, event
  store, run state, change feed, egress gateway, DNS forwarder, catalog store, master secret and Lake gateway.
- **The pins are built from the constants, or from the value that funcd computes.** Each `NamesAt` call derives the
  value: durations through `v1.Duration(...).String()`, sizes through a division by the unit, the multipart factor and
  the retention sweep through a word table with a guard, the units of `volumes.capacity` from `v1.ByteSizeGrammar`, the
  Lake engine's stop wait from `STOP_WAIT_SECONDS` in `images/runtime/duckdb/shim.py:95`, and, since round 2,
  `runtime.bootBackoffMax` and `runtime.readinessTimeout` from `bootBackoff` and `pacing` of `cmd/funcd`. Each named
  constant that a tag copies has a pin. The values with no pin are unnamed literals (Routed).
- **The facts, checked against the code, by section** (round 1; the 20 tags that changed were checked again in round 2).
  - storage: the data directory is created at start in every mode (`cmd/funcd/main.go:162`), its relative path is made
    absolute and its store directories are derived under it (`internal/platform/config/config.go:652-685`), and a
    socket path that is too long refuses the start (`internal/workernode/local/manager.go:237-242`). Memory mode skips
    safe mode (`main.go:263-265`), the backups (`main.go:773-780`, `:1196-1199`) and the floor (`main.go:613-615`), and
    still writes the sockets (`main.go:416`) and the master secret (`internal/blob/s3gateway/s3gateway.go:361-372`).
    The floor's 507 and its dropped log records match `docs/behavior/limits.md:50-51`.
  - kvstore: the engine choice, the memory override and the badger requirement (`main.go:766-792`); the store cap's 400,
    its default and a negative value (`pkg/funcd/binding_providers.go:105`, `docs/behavior/limits.md:58`); the sweep's
    floor and default (`internal/platform/config/kvsweep.go:9-12`), and its delete, which reaches the change feed and the
    backup (`internal/kvstore/badger/badger.go:291-317`); the groups, the refusals, the chunk cap, the ranges and the
    rebaseline rule (`internal/platform/config/kvbackup.go:52-141`); the idle tick, the lifecycle rules and the timing of
    the bases (`internal/kvstore/badger/backup.go:126-155`, `:165`, `:636-692`); the change feed's outbox and the bus
    with no listener (`internal/kvstore/badger/cdc.go:20-24`, `internal/bus/nats/nats.go:62-65`), which no EventSource
    reads (`api/types/v1alpha1/eventsource.go:24-30`).
  - backup and workloadBackup: the target schemes and the prefix parameter (`internal/backup/backup.go:167-195`), the
    single-writer probe (`:283-315`), the classes of the ladder and the lifecycle rules (`internal/backup/layout.go:175-221`),
    the timing of a run, a retry, a held platform and the rpo warning (`internal/backup/runner/runner.go:173-175`,
    `:275-293`, `:363-396`), the config rules (`internal/platform/config/backup.go:91-201`), the recipients rules
    (`internal/backup/envelope/envelope.go:68-145`), the overlap check (`cmd/funcd/main.go:1450-1479`) and the listing
    limits (`internal/backup/workload/copies.go:23-41`, `:199-213`; `docs/behavior/disaster-recovery.md:46`).
  - blob: the checks (`internal/platform/config/blob.go:46-90`), the refusal of a store with no versions and the Object
    Lock warning (`cmd/funcd/main.go:1414-1444`), the mirror's conditions, retry, epochs and lifecycle rule
    (`main.go:1245`, `internal/backup/blobmirror/mirror.go:135-157`, `:176`, `:230-232`), and the credential and the
    `--at` window of `funcd restore blob` (`cmd/funcd/restore.go:538-552`, `:677-704`;
    `internal/blob/gocloud/versioned.go:119-124`).
  - recovery, auth and secrets: the counts of safe mode, exit 70 and the reset (`cmd/funcd/main.go:262-309`,
    `internal/safemode/safemode.go:126-172`, `cmd/funcd/upgrade.go:70-96`); the token rules, the loopback refusal and the
    token files (`cmd/funcd/credentials.go:24-203`, `internal/platform/config/config.go:783-795`); the developer role of
    the dev credential (`pkg/funcd/options.go:841-849`); the three roles (`internal/auth/rbac/rbac.go:23-56`); and the
    32-byte key, its warning and the decode check at start (`cmd/funcd/main.go:1095-1136`, `:1344-1354`).
  - runtime: the duration bounds and orderings (`cmd/funcd/main.go:913-1059`), the unit's variables
    (`cmd/funcd/install.go:55-56`), the image names and the pull rule (`internal/runtime/ctrmanager/manager.go:67-92`),
    the CNI list written only when it is missing and the gateway as resolver
    (`internal/runtime/containerd/cni_conf.go:46-90`, `containerd_linux.go:302-345`, `:441-463`), the bridge rules
    (`internal/platform/config/bridge.go:13-53`, `docs/behavior/security.md:77-78`), and the worker rows that the tags
    restate (`docs/behavior/workers.md:42-55`, `docs/behavior/functions.md:63`).
  - controller, log, telemetry and funclog: the collector's sweep at start (`internal/gc/gc.go:120-158`), the retry's
    base and cap (`internal/controller/controller.go:55-98`), the re-check of a waiting Function, bounded by the period
    (`internal/function/function.go:553`); the log on standard output (`cmd/funcd/main.go:81`, `:172`, `:673-692`); OTLP
    gRPC traces and metrics (`internal/platform/observability/telemetry.go:30-118`); and the sinks, their defaults and
    the record bounds (`pkg/funcd/executor.go:155-186`, `internal/funclog/sink.go:55-56`, `internal/funclog/reader.go:18-21`,
    `pkg/funcd/funcd.go:424-450`).
  - s3gateway and lake: the upload cap, the multipart budget and each principal's share
    (`internal/blob/s3gateway/multipart.go:31-33`, `:144-181`), the master's path and its creation
    (`internal/blob/s3gateway/s3gateway.go:322-372`), the derived endpoint (`cmd/funcd/main.go:1171-1190`), the rotation
    bounds (`main.go:1033-1049`), the Lake's fence, which takes only an IP address
    (`internal/services/lake/reconcile.go:117-123`), the bounds of the save interval, the catalog cap and the scratch rule
    (`internal/platform/config/lake.go`), Pending with ScratchSpaceLacking (`internal/services/lake/reconcile.go:158-164`,
    `:351-357`), and the poll capped at the period (`internal/services/lake/lake.go:233`), with
    `docs/behavior/services.md:25` and `:43-45`.
  - workflow, eventing and invoke: the step defaults, the attempts, the backoff cap and the payload refusals
    (`internal/workflow/engine.go:151-160`, `:214-230`, `:297-299`, `:1105-1109`, `:1262-1279`, `:1374-1394`;
    `internal/controlplane/admission/workflowrun.go:41`; `api/fault/problem.go:34`), the hourly sweeps
    (`pkg/funcd/funcd.go:1521-1577`), the Workflow's poll for a step image or a declared Secret
    (`internal/workflow/reconcile_workflow.go:95-136`, `internal/workflow/rootentries.go:88-98`), the delivery defaults,
    the queue cap and the backoff (`internal/sensor/sensor.go:39-46`, `internal/sensor/retry.go:114-171`,
    `cmd/funcd/main.go:978-1002`), the recheck of a Bucket or a SQLDatabase (`internal/eventing/eventing.go:133-239`), the
    dead-letter sweep (`internal/eventing/deadletter/badger/badger.go:190-240`), and the invoke defaults and codes
    (`pkg/funcd/options.go:294-307`, `docs/behavior/calls.md:51-57`, `docs/behavior/functions.md:48`, `:57`).
  - functions, site, sql, volumes, app and health: the CPU floor and the size grammar
    (`api/types/v1alpha1/resources.go:21-37`, `docs/behavior/functions.md:60`), the Site's index gate
    (`internal/site/reconcile.go:329-371`), the change feed's port and the statement timeout
    (`pkg/funcd/binding_providers.go:490-505`, `internal/services/sql/facade.go:60-64`, `:141-155`), the volume image's
    root, its growth and its refusal to shrink (`internal/volume/quotafs/quotafs_linux.go:68-97`,
    `cmd/funcd/main.go:1619-1650`), the volumes-off refusal and phase (`internal/controlplane/admission/volumes.go:39`,
    `internal/function/volumes.go:52-54`), the App's deadline and the health orderings
    (`internal/app/revision.go:230-249`, `cmd/funcd/main.go:982-1008`), and the probes and their reason
    (`internal/health/prober.go:190-196`, `docs/behavior/health.md:29-31`).

### Recommendation

Pass. Every finding of round 1 is fixed in 8bd101ba. The routed items go to their owners: the five unnamed defaults in
`cmd/funcd/main.go` to the decider, the three unnamed literals with them, and the question about OTLP logs to the guide's
owner.
