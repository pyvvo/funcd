# ADR-0227: Disaster recovery implementation addenda: contracts settled while implementing ADR-0204 to ADR-0210

- **Status**: Accepted (2026-10-11, on the disaster-recovery session's request under its FEAT-0009 `adr-batch` run, after a clean `adr-judge` gate: three lenses with a skeptic per finding, the kept findings folded, a confirm judge's accept; it records only decisions already on main, each cited to the code)
- **Date**: 2026-10-11
- **Deciders**: green-0-rabbit
- **Tags**: disaster-recovery, backup, restore, contracts
- **Realizes**: [FEAT-0009/F109, F111](../feat/0009-feat-disaster-recovery.md) (platform backup and restore; blob
  backend and KV common format). No feat row changes here: the disaster-recovery session owns FEAT-0009's rows and
  records them in its tracking PRs. A process (consolidation) ADR that adds no capability, so no board card.
- **Supersedes (in part)**, each clause getting a `Superseded in part by: ADR-0227` back-link at acceptance (lines at
  origin/main 74f8e604; the quotes identify each clause once back-links shift them):
  - [ADR-0204](0204-backup-encryption-and-key-escrow.md) (Implemented): the Contracts' `Opener` comment "nil
    recipients ⇒ nil (as is)" (175), by Decision 1.4; Decision 7's migration "on a gateway-off node" … "both present
    and different ⇒ `fault.Invalid`" (149–152) and the Open questions' "gateway off: copy to Decision 7's path with a
    warning, refuse when both exist and differ" (241), by Decision 1.7; the Implementation plan's go.mod list "adds
    `filippo.io/edwards25519` v1.2.0, … `filippo.io/nistec` v0.0.4, `golang.org/x/term` v0.45.0" (204–206) and the
    Consequences' "four new modules" (227), by Decision 1.8.
  - [ADR-0206](0206-restore-and-held-boot.md) (Implemented): Decision 6's "`restore run` and ADR-0207 write it"
    (130), by Decision 3.2.
  - [ADR-0207](0207-pre-upgrade-snapshot-and-safe-mode.md) (Implemented): the scenario's "`--no-snapshot` swaps and
    warns once" (36) and "`--no-snapshot` skips 2 and 4 and warns once" (99), by Decision 4.1; step 2's order
    "`envelope.New`; the metastore directory present and not empty" and its On-failure "nothing changed" (93), by
    Decisions 4.2 and 4.3.
  - [ADR-0208](0208-blob-store-backend-and-backup-target.md) (Implemented): "the mirror is due when the newest
    complete manifest is this old" (91), by Decision 5.1; Decision 6's "one `backup.Unseal` for the index and every
    object" (144–145), by Decision 5.3.
  - [ADR-0210](0210-api-optimistic-concurrency-if-match.md) (Implemented): "at most 5 attempts (the `devApplyAttempts`
    bound" (129), by Decision 7.2; "every PUT and DELETE operation gains an optional `If-Match`" (135), "`IfMatchParams`
    (NEW) is embedded in every PUT and DELETE input" (147), "optional `If-Match` on every PUT and DELETE operation"
    (180) and the checklist's "Every PUT and DELETE operation in the regenerated spec has an optional `If-Match`" (215)
    and the Open questions' "optional `If-Match` on every PUT and DELETE" (240), by Decision 7.1.
- **Extends only** (no back-link): every other item below adds a name or a rule the ADR's Contracts do not state and
  contradicts no clause; [ADR-0205](0205-backup-operation.md) and [ADR-0209](0209-kv-backup-on-the-common-format.md)
  get no supersede entry (ADR-0209's `hold.Begin`/`hold.End` bracket, lines 150–151, still holds, as does ADR-0206's
  line 127).
- **Relates to** (unchanged): ADR-0203 (format, `GenRef`) · ADR-0212 (already supersedes ADR-0206's App deadline) ·
  ADR-0214 (the `preApply` deadline term) · ADR-0000 (frozen Implemented ADRs).

## Context & Need

The implementations of ADR-0204 to ADR-0210 (#891, #909, #910, #920, #941, #943, #878) settled contract details those
ADRs leave out or state otherwise. Their reviews record several as Minors attributed to the ADR and ask a later ADR
to state them (`docs/reviews/adr-0204-implementation-claude-opus-5-5.md` Minor(adr);
`adr-0207-implementation-claude-opus-5-5.md:22-26`; `adr-0208-implementation-claude-opus-5-5.md`, three minors;
`adr-0209-implementation-claude-opus-5-5.md:48-52`; `adr-0210-implementation-claude-opus-5-5-2.md:25-27`). The ADRs are
`Implemented`, so frozen (ADR-0000): only a new ADR can record them. Purpose: the seven ADRs plus this one describe
what main does, each item cited to the landed code, so a reader or an implementer never has to diff the code to learn a
contract. It records decisions the decider took during implementation (the DR session of 2026-10-10; its question
numbers are cited where the reviews record them) and adds no code.

## Scenarios

Only the user-visible items; each names the existing test that proves it on main.

- `scenario: master-secret-file-never-migrates` (1.7) — a gateway-off node with `s3gateway.masterSecretFile` set and a
  working-directory `s3gateway/master.key` starts: no copy, no refusal, one warning naming the ignored file.
  `TestIssue850_MasterSecretFileNeverMigrates` (`internal/blob/s3gateway/issue850_test.go:145`).
- `scenario: restore-blob-or-kv-leaves-next-start-held` (3.2) — `funcd restore blob` or `restore kv` succeeds ⇒
  `<storage.dataDir>/.hold` exists. `TestRestoreBlobCommand` (`cmd/funcd/blobstore_test.go:270`, asserted at `:306`),
  `TestRestoreBlobAtCommand` (`:316`), `TestScenarioRestoreAnyPoint` (`cmd/funcd/kvbackup_scenarios_test.go:299`, `:315`).
- `scenario: upgrade-while-held-refuses` (4.1) — `funcd upgrade` with `.hold` present ⇒ `fault.Conflict` naming the
  marker and its reason, binary not swapped; with `--no-snapshot` it swaps and prints the held warning once.
  `TestUpgradeWhileHeld` (`internal/upgrade/upgrade_test.go:412`).
- `scenario: restore-blob-store-credentials` (5.2) — `funcd restore blob --store-credentials-file <f> --at <t>`
  restores with the given store credentials. `TestRestoreBlobAtCommand` (`cmd/funcd/blobstore_test.go:316`, flag at `:340`).
- `scenario: backup-records-restore-parent` (2.1) — after a restore, the daemon's generations name the restored one
  as parent. `TestBackupParentWired` (`cmd/funcd/upgrade_test.go:536`).
- `scenario: mirror-retries-after-failure` (5.1) — a failed mirror run is retried after `backup.retryInterval`, not
  `backup.interval`. `TestLoopRetriesAfterFailure` (`internal/backup/blobmirror/loop_test.go:97`).
- `scenario: unsealed-mirror-object-restored-as-is` (5.3) — an object whose bytes start with `age-encryption.org/v1`
  restores unchanged from an unsealed generation. `TestScenarioMirrorRestore` (`internal/backup/blobmirror/restore_test.go:30`, `:34`).
- `scenario: kv-only-config-lists-kv-points` (6.2) — `funcd restore list` with only `kvstore.backup.target` lists the
  KV points. `TestRestoreListKVOnlyConfig` (`cmd/funcd/kvbackup_scenarios_test.go:364`).
- `scenario: kv-source-failure-own-line` (6.2) — with a platform `backup.target`, a failing KV source prints
  `KV POINTS: cannot list …` after the platform rows and the command succeeds; with no platform source the KV failure
  is the command's error. `TestRestoreListKVSourceFailureOwnLine` (`:380`); `cmd/funcd/restore.go:146-149`.
- `scenario: discard-dead-letter-without-if-match` (7.1) — `DELETE …/deadletters/{id}` takes no `If-Match`: the
  Review checklist's spec check; `DiscardDeadLetter(ctx, ns, id)` sends it (`pkg/sdk/sdk_test.go:384`, `:410`).

## Scope

**In**: the items below, each already on main at 74f8e604. **Out**: any behaviour change; ADR-0206's App deadline and
boot reclaims (already decided there and in ADR-0212/0214); ADR-0209's legacy-chain rule (its Decision 3, line 122);
the unconfirmed claim that a bad-config crash loop is left to systemd's rate limit (not in code or docs: dropped).

## Constraints & Decision drivers

- ADR-0000: an `Implemented` ADR takes only a `Superseded by` back-link; a contract fact is restated in a new ADR.
- An item that contradicts a stated clause is a supersede entry; one that only adds a name or a rule is an extension.
- Every item is grounded in a `file:line` at 74f8e604; nothing unbuilt is recorded.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **One addenda ADR, cited to the code** | One place; the frozen ADRs stay untouched but for back-links | Readers must read two ADRs per topic | **Chosen** |
| Edit the Implemented ADRs in place | One document per topic | Violates ADR-0000's freeze | Rejected |
| One superseding ADR per source ADR | Narrow | Seven tiny ADRs for no decision | Rejected |
| Leave the gaps to the reviews | No work | Contracts invisible to a reader of `docs/adr/` | Rejected |

## Decision

Each item states what main does. **[changes]** marks a superseded clause (header); every other item extends.

### 1. ADR-0204 (backup encryption and key escrow; #891)

1. `envelope.Config` gains `KeyPrefix` and `Logger` (`internal/backup/envelope/envelope.go:37-38`); `""` keeps
   `backup.` (`:51-53`), nil keeps `slog.Default()` (`:56-58`). The KV export passes `"kvstore.backup."` and
   `cfg.KVEncryptionKeyPrefix()` (`cmd/funcd/main.go:720`, `:755`).
2. `funcd.WithMasterSecret` (`pkg/funcd/options.go:164`; empty refused, `:167`): `cmd/funcd` loads the master once
   (`main.go:284`), shares it with the sealer (`:289`) and the platform (`:458`); without it pkg/funcd still loads its
   own (`pkg/funcd/funcd.go:683-689`).
3. `s3gateway.MasterPath` computes Decision 7's path (`internal/blob/s3gateway/s3gateway.go:297-304`), used by
   `escrow.PlanMaster` (`internal/backup/escrow/escrow.go:148`) and `MigrateMaster` (`s3gateway.go:352`).
4. **[changes]** `Opener` with no recipients returns an `Unseal` that reads as is and refuses a sealed file
   (`envelope.go:232`, `:267-268`); with recipients it refuses an unsealed file (`:249-250`). A store file whose seal
   disagrees with its manifest is `fault.Invalid` (`:218`).
5. `Sealer.Keys()` recipients are sorted and distinct (de-duplicated by `String()`, `envelope.go:122-124`, `:91`) and
   returned as a copy (`:165-167`).
6. Neither `storage.dataDir` nor `s3gateway.masterSecretFile` set ⇒ the master is random, in memory, never written
   (`s3gateway.go:323-328`); `MasterPath` is `""` (`:301-302`) and `MigrateMaster` does nothing (`:353-355`).
7. **[changes]** `s3gateway.masterSecretFile` set ⇒ no migration and no differ-refusal, gateway on or off: a
   working-directory master only warns (`s3gateway.go:363-367`). Only a gateway-off node without it migrates.
8. **[changes]** go.mod: `filippo.io/age` v1.3.2 adds two modules, itself and `filippo.io/hpke` v0.4.0 (`go.mod:8`,
   `:84`); raises `golang.org/x/crypto` to v0.55.0 and `golang.org/x/sys` to v0.47.0 (`:395`, `:67`); `edwards25519`,
   `nistec` and `golang.org/x/term` are not required. Other MVS raises are those of the review (x/net, x/sync, x/text,
   x/mod, x/tools, rogpeppe/go-internal).
9. `envelope.LoadMaster`, `ReadSecretsKey`, `FromConfig` (`internal/backup/envelope/config.go:14`, `:23`, `:36`) load
   the master and build the sealer from config, for the daemon and `funcd upgrade` alike; `FromConfig` is nil
   without `backup.target`. `s3gateway.MigrateMaster` (`s3gateway.go:350`) realizes Decision 7's migration.
10. `escrow.Find` follows symlinks (`escrow.go:66`, `:89`). `PlanMaster` refuses a set but unreadable
    `s3gateway.masterSecretFile` (`escrow.go:161-164`). `ReadIdentities` refuses an `age -p` identity file as
    `fault.Invalid` naming it (`envelope.go:193`).

### 2. ADR-0205 (backup operation; wired by #920)

1. ADR-0207's PR wired what line 211 deferred: `Config.Hold` is the daemon's hold (`cmd/funcd/main.go:1025-1028`) and
   the runner is in `TestEveryRunnerConsultsHold` (`pkg/funcd/hold_internal_test.go:59`, `:73`); `Inputs.Parent` comes
   from `backupParent` (`main.go:1136-1141`: `restore.Timeline`, then `restore.Parent`) through the new option
   `funcd.WithBackupParent` (`pkg/funcd/options.go:276`; `main.go:383`) into `Bind` (`pkg/funcd/funcd.go:1173`).

### 3. ADR-0206 (restore and held boot; #910)

1. The daemon opens one hold (`cmd/funcd/main.go:150`, `:181`) and passes it to `WithHold`, the runner, the blob
   mirror and the KV backup (`:385-386`, `:1027`, `:1098`, `:774`); a nil hold never holds (`:276`).
2. **[changes]** `restore kv` and `restore blob` also write the marker, `Reason: "restore"`, after `hold.Begin`
   (`cmd/funcd/restore.go:584-596`; callers `:651`, `:681`, `restore_kv.go:153`; DR Q15), so the next start is held. On
   success `hold.Own` includes the marker file (`restore.go:611`); on failure `undo` removes it only if this run wrote
   it (`:599-606`). Marker writers: `restore run`, `restore kv`, `restore blob`, ADR-0207.
3. The hold package also exports `ReportFile`, `Evidence` (the `GET …/hold` body), `Report`, `WriteFile`,
   `WriteReport` (`internal/platform/hold/hold.go:24`, `:66`, `:178`, `:269`, `:300`); `restore run` writes
   `restore.json` through `WriteReport` (`internal/restore/run.go:201`).

### 4. ADR-0207 (pre-upgrade snapshot and safe mode; #920)

1. **[changes]** `funcd upgrade` refuses while `.hold` exists, before step 3: `fault.Conflict` naming the marker and
   its reason (`internal/upgrade/upgrade.go:100`, `:224-227`; DR Q13). With `--no-snapshot` on a held node it proceeds
   and prints two warnings, the `--no-snapshot` one (`:106`) and the held one (`:229`).
2. **[changes]** Step 2 checks the metastore directory before loading the master or the sealer (`upgrade.go:335-338`
   before `:345`, `:352`), so a wrong `--config` creates no master secret.
3. **[changes]** Step 2 loads the master (`envelope.LoadMaster`, `upgrade.go:345`) before `backup.Open` and the probe
   (`:357`): a later step-2 failure leaves a master it created under `<storage.dataDir>` or `s3gateway/` in place,
   re-owned to `<storage.dataDir>`'s owner (`:341`, `:346`, `masterDirs` `:507`), as on success.
4. `safemode.MarkerReason` (`internal/safemode/safemode.go:31`; `cmd/funcd/main.go:233`, `:246`) and `backup.GenDir`
   (`internal/backup/layout.go:35`) are exported. `funcd safe-mode reset` adds a held hint (`cmd/funcd/upgrade.go:92-93`);
   the downgrade refusal points to `examples/restore-runbook.md` (`internal/upgrade/upgrade.go:92-93`).

### 5. ADR-0208 (blob store backend and backup target; #941)

1. **[changes]** `blobmirror.Config.RetryInterval` (DR Q9), from `backup.retryInterval` (`internal/backup/blobmirror/mirror.go:56`;
   `cmd/funcd/main.go:1093`; default 5m, `internal/platform/config/backup.go:17`, `:53`); after a failed run the next
   is due at min(end + `RetryInterval`, start + `Interval`) (`mirror.go:168-173`); `New` refuses one ≤ 0 (`:130`).
2. `--store-credentials-file` is declared on `restore blob` (`cmd/funcd/restore.go:556-557`), default
   `blob.credentialsFile` (DR Q5): Open questions lines 143/242 are closed as written.
3. **[changes]** A generation naming no recipient restores its objects as is, unchecked
   (`internal/backup/blobmirror/restore.go:50-53`, `:150-153`); its index still goes through the `Unseal` (`:54`).

### 6. ADR-0209 (KV backup on the common format; #943)

1. `restore kv` brackets as Decision 6 says and also writes the marker (Decision 3.2; `cmd/funcd/restore_kv.go:153`).
2. `restore list` lists KV rows only when the KV export is on or has a target (`restore_kv.go:29-30`); a KV-only
   config skips the platform open (`restore.go:130`) and lists from `kvstore.backup.target` (`restore.go:141-151`). A
   failing KV source prints its own line after platform rows (`:148-149`, `restore_kv.go:49`); with no platform
   source it is the command's error (`restore.go:146-147`). `restore inspect --kv` reads only the KV points (`restore_kv.go:184-189`).

### 7. ADR-0210 (API optimistic concurrency; #878)

1. **[changes]** `If-Match` is on every PUT and DELETE *object* operation (Scope, line 64); `DELETE
   …/deadletters/{id}` (`discardDeadLetter`) has none, a DeadLetter having no `resourceVersion`
   (`internal/eventing/deadletter/deadletter.go:26-37`; `internal/controlplane/deadletters.go:106-116`; `api/openapi/funcd.v1alpha1.yaml:4639-4640`).
2. **[changes]** The retry bound is `applyAttempts` = 5 in `cmd/funcdctl/workflow.go:255`, not `devApplyAttempts`.
   `withReplaceVersion` and `staleVersion` (`internal/controlplane/precondition.go:79`, `:89`) are internal helpers.

## Temporary workarounds

None.

## Contracts

Copied from the code at 74f8e604; all exist, none is new here.

```go
// internal/backup/envelope (envelope.go:33-39; config.go:14, :23, :36)
type Config struct {
	Recipients         []string
	None, NoSecrets    bool
	SecretsKey, Master []byte
	KeyPrefix          string
	Logger             *slog.Logger
}
func LoadMaster(cfg config.Config, log *slog.Logger) ([]byte, error)
func ReadSecretsKey(cfg config.Config) ([]byte, error)
func FromConfig(cfg config.Config, master []byte, log *slog.Logger) (*Sealer, error)

// internal/blob/s3gateway (s3gateway.go:297, :350)
func MasterPath(masterSecretFile, dataDir string) string
func MigrateMaster(masterSecretFile, dataDir string, gatewayOn bool, logger *slog.Logger) error

// pkg/funcd (options.go:164, :276)
func WithMasterSecret(secret []byte) Option
func WithBackupParent(p *backup.GenRef) Option

// internal/platform/hold (hold.go:24, :66, :178, :269, :300)
const ReportFile = "restore.json"
type Evidence struct { Held bool; Marker *Marker; ReleasedAt *v1.Timestamp; Report json.RawMessage; … } // :66-78
func (h *Hold) Report() (json.RawMessage, error)
func WriteFile(op, dir, name string, data []byte) error
func WriteReport(dataDir string, report json.RawMessage) error

// internal/safemode (safemode.go:31); internal/backup (layout.go:35)
const MarkerReason = "safe-mode"
func GenDir(class Class, n uint64, timeline string) string

// internal/backup/blobmirror Config field (mirror.go:56)
Interval, Rebaseline, Retention, RetryInterval time.Duration
```

| Direction | What |
|---|---|
| Consumes | `backup.retryInterval` (ADR-0205) for the blob mirror; `restore.Timeline`, `restore.Parent` (ADR-0206); `hold.Write` in `restore kv`/`blob` |
| Exposes | `funcd upgrade`'s held `fault.Conflict`; the `restore` marker after `restore kv`/`blob`; `restore blob --store-credentials-file`; `discardDeadLetter` without `If-Match`; the names above |

## Implementation plan

None: every item is already on main. At acceptance: `Status: Accepted` and the back-links of the header's entries.
With no code to write, the citation review below is its review gate. No feat, blueprint, roadmap or board change here:
FEAT-0009's rows belong to the disaster-recovery session's tracking PRs.

## Review checklist

- [ ] Every `file:line` above shows the cited text at 74f8e604 (`git show 74f8e604:<file> | sed -n <line>p`).
- [ ] Every header line shows its quote in the cited ADR at 74f8e604; each **[changes]** item has a header entry and
      no other item does.
- [ ] The Contracts block matches the code verbatim (but the elided `Evidence` fields), and nothing in it is marked new.
- [ ] `scripts/agent/d go test -run 'TestOpenerUnsealed|TestKeysSortedAndDistinct|TestIssue850_|TestUpgradeWhileHeld|TestLoopRetriesAfterFailure|TestScenarioMirrorRestore|TestRestoreBlobCommand|TestRestoreBlobAtCommand|TestScenarioRestoreAnyPoint|TestRestoreListKV|TestBackupParentWired|TestEveryRunnerConsultsHold' ./internal/backup/... ./internal/blob/s3gateway/ ./internal/upgrade/ ./cmd/funcd/ ./pkg/funcd/` passes.
- [ ] In `api/openapi/funcd.v1alpha1.yaml` `discardDeadLetter` is the only PUT or DELETE without `If-Match`; go.mod
      has no require line for `filippo.io/edwards25519`, `filippo.io/nistec`, `golang.org/x/term`.
- [ ] The acceptance diff touches only this file and the back-link line of each superseded ADR; no username or
      absolute path.

## Consequences

- (+) ADR-0204 to ADR-0210 plus this ADR describe main; the review Minors attributed to those ADRs are closed.
- (+) A kv-only or blob-only restore boots held, like `restore run`: the operator inspects before data serves.
- (−) A reader of an affected ADR must follow its `Superseded in part by` link to see the current clause.
- (−) `internal/controlplane/precondition.go:14` still says "every PUT and DELETE input"; a code comment, left to a fix.
- (−) `TestScenarioPreRestoreVersionConflicts` still uses a stand-in engine, not ADR-0202's `Snapshot`
  (`internal/controlplane/concurrency_test.go:331-332`; ADR-0210 line 197); swapping it now that ADR-0202 is
  Implemented is left to a fix.

## Open questions

- A node with neither `storage.dataDir` nor `s3gateway.masterSecretFile` gets a `MasterPlan` whose `Path` is `""`
  (1.6; `escrow.go:148`), and `restore run` writes `Install` to `Path` (`internal/restore/run.go:444-448`). Neither
  ADR-0204 nor this ADR states that outcome; if it is wrong, it is an issue for `/fix`.

## References

- PRs [#891](https://github.com/pyvvo/funcd/pull/891) (ADR-0204), [#909](https://github.com/pyvvo/funcd/pull/909)
  (ADR-0205), [#910](https://github.com/pyvvo/funcd/pull/910) (ADR-0206), [#920](https://github.com/pyvvo/funcd/pull/920)
  (ADR-0207), [#941](https://github.com/pyvvo/funcd/pull/941) (ADR-0208), [#943](https://github.com/pyvvo/funcd/pull/943)
  (ADR-0209), [#878](https://github.com/pyvvo/funcd/pull/878) (ADR-0210); #850 (master migration).
- ADRs [0000](0000-adr-process.md), [0202](0202-platform-store-snapshot-and-timeline.md) and the ADRs in the header; the implementation reviews in
  `docs/reviews/adr-0204-…` to `adr-0210-implementation-claude-opus-5-5*.md`.
