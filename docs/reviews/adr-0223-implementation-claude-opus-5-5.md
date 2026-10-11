## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0223 implementation, model: claude-opus-5-5)

Reviewed commit `42c571d6` (`feat: implement ADR-0223 — funcdctl backup plan (FEAT-0009/F112)`) on
`feat/adr-0223-funcdctl-backup-plan`, diff `9fd461d9..42c571d6` (14 files, +1749 −49), against the ADR's Contracts,
Decisions, Scenarios, Implementation plan, Review checklist and Definition of done. The repo-wide gate (`just ci`,
e2e, lanes) runs before the PR; this review ran the checks of the touched packages, mutants and probes through
`go test -overlay` (copies of the files outside the worktree; nothing in the worktree was edited, `git status` clean).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Two command-level wirings are right but no test fails without them** · attribution: `model` ·
  `cmd/funcdctl/backup.go:212` hands `Derive` the located file's `config.Parse` (Review checklist item 3). A mutant
  that hands it a zero `config.Config` passes every test: the scenarios use files whose `rebaseline` and blob
  interval are the defaults. A probe shows the code is right (`blob.backup.interval: 2h` in the file, then
  `plan blob --keep 720h` prints `retention: 722h`; the mutant would print `721h` and exit 0).
  `cmd/funcdctl/backup.go:256` and `:279` run apply's `preflight` on the `BackupSchedule` (Decision 3). A mutant that
  removes both calls passes every test. A probe shows the code is right (a manifest without `resourceGroup` exits 1
  with `funcdctl apply: manifest is invalid (BackupSchedule "x"): …`, stdout empty).
  Fix: in `cmd/funcdctl/backup_plan_test.go`, add a case whose file sets a non-default blob interval (or a valid
  non-default `kvstore.backup.rebaseline`) and asserts the derived value, and a `TestFlagRules`-style case for a
  manifest the validator refuses (exit 1, apply's message, stdout empty, file unchanged).

- **Apply's insert before a trailing document marker has no test** · attribution: `model` ·
  `internal/backup/plan/edit.go:60` places a missing top-level key before the next `---`/`...` marker
  (`documentEnd`). Replacing that with `len(src)` passes every test. With the mutant, `backup:\n  interval: 1h\n---\n`
  plus `kvstore.backup.interval` puts the key in a second document. A probe shows `config.Parse` then reads only the
  first document (`kvstore.backup.interval` stays `""`, no error), so `--write` would report a change that the daemon
  never applies. The shipped code inserts before the marker (probe output
  `"backup:\n  interval: 1h\nkvstore:\n  backup:\n    interval: 30s\n---\n"`).
  Two cosmetic paths are also untested: the inferred nested step (`edit.go:215`; a 4-space file) and skipping a
  comment after a mapping's last child (`edit.go:276`).
  Fix: add a trailing-marker case (and a 4-space nested insert) to `TestApplyKeepsBytes`.

### Implementer's choices — judged

1. **Verify cadence printed in print and write mode** (`cmd/funcdctl/backup.go:232-236`): acceptable. Decision 4 lists
   the line among the `--write` bullets, but the cadence depends only on `--rpo`, not on the mode. No Scenario
   restricts print-mode stderr to the warning alone. The value matches ADR-0205 Decision 6's `(rpo − interval)/2`
   (`15m` at `--rpo 1h`, `7m30s` at `30m`). Not a finding.
2. **Every non-error finding of `CheckBackup`/`CheckBlob` printed, including the `backup.encryption.recipients`
   warning**: this is what Decision 3 says ("each finding … with `Error` false goes to stderr"). It is not a
   deviation from the daemon's loader. `logBackupFindings` (`cmd/funcd/main.go:1003-1011`) drops that finding with
   a target only to avoid a duplicate. The start still warns on the same condition through `envelope.New`
   (`internal/backup/envelope/envelope.go:93-94`: recipients set and no `secrets.encryptionKeyFile`), only with
   different wording. Not a finding.
3. **`-f` refused for a config component, `--config` for `schedule`**: acceptable. Decision 1's table limits each flag
   to its components. These are flag rules, they fail closed, and each names its flag (`TestFlagRules`).
4. **Apply also refuses a tagged scalar, a key with no value and a multi-line value**: acceptable. They fail closed
   with `fault.Invalid` naming the key, and exit 1 ("a key plan cannot edit", Decision 6). This matches the ADR's
   reason for refusing rare forms ("a guess in a rare form could corrupt the file"). `~`/`null` leaves are still
   replaced (probe).
5. **Blob `--keep` + interval above `MaxDuration` refused** (`internal/backup/plan/derive.go:80-83`): acceptable.
   `MaxDuration` is about `math.MaxInt64` ns, so `Keep + interval` would wrap negative. The check guards plan's own
   arithmetic, adds no daemon rule, names `--keep`, and is consistent with `RTOBound`'s cap.
6. **`exitCode` falls back to 1 when `ExitCode() <= 0`** (`cmd/funcdctl/main.go:23-29`): correct. An error must never
   exit 0, and `exec.ExitError` returns −1 for a signal. No other error in funcdctl carries `ExitCode()` today.
7. **`TestApplyKeepsBytes` in `cmd/funcdctl`**: acceptable. Its symlink case needs `commitFile`, which the
   Implementation plan places in `cmd/funcdctl/backup.go`. Side effect: `internal/backup/plan`'s own tests cover
   32.5% of the package (edit.go is not exercised there), and the `cmd/funcdctl` plan tests cover 87.2% of it
   (`-coverpkg`). Not a finding.

Unscored notes: `planSchedule`'s `n != 1` count (`cmd/funcdctl/backup.go:259-267`) repeats `Apply`'s own refusal
(the mutant that drops it survives; its message names `-f`, which is clearer). The doc comment at
`internal/backup/plan/edit.go:21` runs past the file's wrap width. A CRLF file gets LF-terminated inserted lines
(it still parses).

### ✅ Verified correct (keep it)

- **Build, vet, lint, format**: `scripts/agent/d go build ./...` exit 0 on darwin and `GOOS=linux`; `go vet` over the
  5 touched packages exit 0 on both; `go tool golangci-lint run` over them `0 issues.` exit 0 on darwin and
  `GOOS=linux` (host-built tool); `gofmt -l` on the changed Go files prints nothing; `go mod tidy -diff` exit 0;
  `go.mod`/`go.sum` unchanged (no new module, `go.yaml.in/yaml/v3` already direct).
- **Tests**: `scripts/agent/d go test -race -count=1` on `internal/platform/config`, `internal/platform/units`,
  `internal/backup/plan`, `internal/provider`, `cmd/funcdctl`: all `ok`, no `FAIL`.
- **One test per Scenario, un-skipped, passing, asserting the ADR's exact values** (`-race -v`):
  platform-from-objectives `TestScenarioPlatformFromObjectives` (stdout `interval: 30m`, `rpo: 1h`, `48/30/0` in
  block style, the daemon's no-target warning on stderr, exit 0); workload-keys-from-objectives
  `TestScenarioWorkloadKeysFromObjectives` (`30s` + `48/7/0`; `25/0/0`; `1h` + `721h`); schedule-from-objectives
  `TestScenarioScheduleFromObjectives` (byte-exact manifest with `0 * * * *` and `336h`, accepted by a real
  `funcdctl apply -f -` against an in-process control plane with the BackupSchedule admission; `--rpo 90s` exits 1
  naming `--rpo`, stdout empty); write-keeps-the-rest `TestScenarioWriteKeepsTheRest` (byte-exact file, both change
  lines incl. `(absent) → 30m`, mode 0640 kept, no temp file left); refused-as-the-daemon-refuses
  `TestScenarioRefusedAsTheDaemonRefuses` (both cases: error line equal to `config.Load`'s for the same bytes, exit
  1, file unchanged); rto-impossible `TestScenarioRTOImpossible` (`34m8s` and both knobs, exit 3, stdout empty,
  file unchanged); rto-lower-bound-only `TestScenarioRTOLowerBoundOnly` (`41s`, drill line, exit 0). Each 0.00-0.10 s.
- **Units of the test plan, all present and passing**: `TestFlagRules` (every Decision 1 refusal, exit 1, no output),
  `TestDeriveTable` (each row; kv floor at unset/12h/24h/48h and 48h by `FUNCD_KVSTORE_BACKUP_REBASELINE`; blob's
  added interval from R/2, cur and the 1h default), `TestLadder` (24h, 25h, 48h, 49h, 720h, 721h), `TestCron` (each
  period at 2P and 2P+1m, the floor, the cap, `CheckCron` and the firing gap), `TestApplyKeepsBytes`,
  `TestCheckMatchesParse` (40 error rows: plan's error equals `Parse`'s and `Load`'s), `TestRTOBound` (the cap),
  `TestParseBytesMoved` (`0`, overflow).
- **Revert check**: unregistering `plan` from the `backup` group fails all 7 scenario tests and `TestFlagRules`;
  making `Parse` skip the env overlay fails `TestDeriveTable`.
- **Mutants (57 run, 47 killed)**: killed: Ladder's 48h/720h bounds and ceiling; Cron's hour list, minute list, 1 min
  floor and 24 h cap; the kv floor `+1` and its `max`; blob's added interval and cur's interval; R ÷ 2; the ms
  truncation; Apply's splice end and token length; the indentation scan and inserted indent; the alias, anchor,
  merge-key, flow, block-scalar and multi-document refusals; render's plain/quoted choice; the kind match; the null
  parent; the unchanged-value case; `RTOBound`'s ceiling and cap; `CheckRTO`'s `<`; `RTOError.ExitCode`; `main`'s
  exit mapping; the RTO check moved after the write; checking the unmerged bytes; mode, symlink, `--write` with no
  file, the `-f -` rule, the `--rto/--size/--speed` trio, the cadence formula, the trailing `restart funcd to apply`.
  Survivors: 4 equivalent (warning filter after a passing `Parse`; either single preflight; committing identical
  bytes), 1 redundant check (`n != 1`), 2 cosmetic (step, trailing comment) and 3 test gaps (Minors above).
- **Contracts**: every signature matches (`Component` + 4 consts, `Objectives`, `Setting`, `Change`, `RTOError` with
  `Error`/`ExitCode`, `Derive`, `Ladder`, `Cron`, `Apply`, `CheckConfig`, `RTOBound`, `CheckRTO`, `config.Parse`
  with `Load` = `ReadFile` + `Parse` and messages unchanged (op stays `config.Load`), `units.ParseBytes`, `preflight`).
- **Decisions**: plan never calls `sdkClient` (`--server`/`--token` unused); the derivation table, ladder and cron
  rule; no key beyond Decision 2's (no target, credential, recipient, `timeZone`, `scope`); one loader (`Parse` with
  funcdctl's `FUNCD_*`, no `--memory`); `fault.NotFound` naming `--config` for `--write` with no file; the splice
  keeps every other byte; the commit resolves symlinks, uses a temp file beside the resolved file, `fsync`, rename,
  keeps the mode, and keeps owner and group where `chown` is allowed; stderr ends with `restart funcd to apply` or
  `funcdctl apply -f <file>`; exit 3 only after the settings pass, nothing printed or written.
- **Apply edge probes** (all correct): leading `---`; trailing `---`/`...`; CRLF; 4-space nesting; a comment after
  the last child; `~` leaf; value on the next line; `1h#x`; multi-byte characters before the value.
- **`parseMemoryBytes` move**: grammar unchanged (no suffix is a suffix of another, so the old map order did not
  matter); overflow now refused; `internal/provider` tests pass; no reference left.
- **Tree vs Repository surface**: every listed file is present (`internal/backup/plan/{derive,edit,check}.go`,
  `internal/platform/units/units.go`, `cmd/funcdctl/{backup,cli,main}.go`, `internal/platform/config/config.go`,
  `internal/provider/runtime.go`, `examples/backup-lifecycle.md`); the only extra files are the tests.
- **Tracking**: the ADR diff is the `Accepted → Reviewing` status line alone; the blueprint bullet was added at
  acceptance. By this run's design, the FEAT-0009 F112 row (still `accepted`) is left to the disaster-recovery
  session.

### Definition of Done

16 / 16 hold (ADR Review checklist 5, ADR Definition of done 1, generic implement-gate DoD 10). `just ci` itself
runs in the PR gate; every check it runs held on the touched packages. The feat row move is left to the
disaster-recovery session by design of this run.

### Model scorecard

To record: claude-opus-5-5 on ADR-0223 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 16/16.

### Recommendation

Sign off: stamp ADR-0223 `Implemented`. The two Minors go back to the builder as a follow-up: three test cases
(cur from the file, a schedule the pre-flight refuses, an insert before a trailing document marker). They do not
block.
