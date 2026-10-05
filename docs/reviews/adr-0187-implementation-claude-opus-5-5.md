# ADR-0187 implementation review: claude-opus-5-5 (loop 1)

- **ADR**: docs/adr/0187-worker-identity-pid-start-boot.md (Accepted; Realizes FEAT-0000/F12)
- **Work**: branch `feat/adr-0187-worker-identity-pid-start-boot`, one commit `ac1698b6` on origin/main
  (11 files, +317/-85)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**: no Blockers, no Majors, 1 Minor (model)

The status stamps (ADR `Reviewing → Implemented`, feat row, board card) are left to the wave's docs PR, as
agreed. This review made no change to the work or to any document.

## Verification run (captured)

All commands ran in the worktree through `scripts/agent/d`.

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet` procreg, process, cmd/funcd, cmd/funcdctl (darwin, linux) | exit 0 / exit 0 |
| `go vet -tags dev` devengine, cmd/funcdctl (darwin, linux) | exit 0 / exit 0 |
| golangci-lint procreg, process, cmd/funcd (darwin) | 0 issues |
| golangci-lint `--build-tags dev` devengine, cmd/funcdctl (darwin) | 0 issues |
| golangci-lint procreg, process; `--build-tags dev` devengine (`GOOS=linux`, host-built binary as in `gate.sh`) | 0 issues / 0 issues |
| gofmt -l on the touched `.go` files | clean |
| `go test -race -count=1` procreg, process (darwin) | ok / ok: all 9 procreg tests pass, including the 4 darwin scenarios |
| `go test -race -tags dev` devengine `TestStateDirReapsEnginesOfACrashedRun` | ok |
| `go test -race` cmd/funcd `TestScenarioCrashRestartLeavesDesiredWorkers`, `TestScenarioDeletedWhileDownReaped` | ok |
| `go test -race -tags dev` cmd/funcdctl `TestScenarioDevPersistRestartReaps`, `TestDevRestartWithoutPersistReaps`, `TestDevStateDir`, `TestIssue700_DevHangupStopsWorkers` | ok |
| **Linux in Docker** (`golang:1.26.4` + Debian nodejs v20.19.2, `--user 1000:1000`, HOME/GOCACHE under `/tmp`, module cache read-only, `GOPROXY=off`): `go test -race` procreg, process, `-tags dev` devengine | ok / ok / ok: all 10 procreg tests pass, including `TestScenarioOtherBootEntryNeverKilled` |

### Mutants (overlay, `go test -overlay`; the tree was not touched)

| # | Mutation | OS | Result |
|---|---|---|---|
| m1 | darwin `bootMatches` restores main's argv rule (`argvContains` from `git show origin/main:…/identity_darwin.go`) | darwin | **killed**: `TestScenarioRetitledWorkerReaped` expected 1, got `killed=0` (the #730 reproduction); `TestOwned` and `TestScenarioLegacyEntryRule` fail too |
| m2 | `Alive` = `Owned` (no zombie check) | darwin | **killed**: `TestScenarioZombieLeaderCountsAsGone`: Reap took 2.01 s, not under 1 s |
| m3 | Linux `bootMatches` ignores the saved boot ID | linux | **killed**: `TestScenarioOtherBootEntryNeverKilled` and `TestOwned` ("another boot") fail |
| m4 | Linux legacy branch returns true without reading argv | linux | **killed**: `TestScenarioLegacyEntryRule` expected 1, got 2; `TestOwned` ("legacy entry without the token") fails |
| m5 | Linux `bootMatches` is main's argv rule for every entry | linux | **killed**: `TestScenarioRetitledWorkerReaped` got `killed=0` |

m1 and m5 also re-prove the defect on main's identity rule on both OSes. m5 confirms on a real Linux kernel the
mechanism that the ADR's Context marks as inferred: Node's `process.title` removes the token from
`/proc/<pid>/cmdline`.

## Findings

### Blocker
None.

### Major
None.

### Minor
- **n1 [model]: a stale doc comment.** `internal/runtime/process/process.go:34` still says the instance token is the
  one "which the reap matches (ADR-0167)". After ADR-0187 the reap reads the token only for a Linux entry without a
  boot ID (the legacy branch). Its purpose is now naming the instance in `ps` and manual cleanup (Decision 3). The
  line should say this and cite ADR-0187. It is one line in a file the ADR names and has no effect on behaviour.

## Review checklist (ADR-0187): 7 of 7 hold

- [x] `Owned` reads argv only for a Linux entry whose `BootID` is empty (`identity_linux.go` `bootMatches`); the
      darwin `argvContains` is gone (grep finds only the Linux copy). Proven by m3 and m4.
- [x] Both owner sites store `BootID` next to `StartTime` (`process.go` `saveLocked`, `devengine.go` `save`); these are
      the only two `Registry.Put` callers in non-test code. A `BootID()` error fails the save at both sites
      (Decision 2). A Linux entry with another boot ID is never signalled (`TestScenarioOtherBootEntryNeverKilled`,
      run in Docker; m3).
- [x] Any read error in `Owned` means "not ours": a `startTime` error, a `bootMatches` error, a `bootID` read error,
      and an empty `boot_id`. The empty `boot_id` case is stricter than the ADR requires and correct: it keeps a
      Linux entry from falling back to the legacy branch.
- [x] Reap selects groups by `Owned` (`procreg.go:104`), waits on `Alive` (`:118`), and at the deadline SIGKILLs
      every still-`Owned` group (`:128`).
- [x] No test uses `Owned` to mean "still runs". All 9 liveness probes the Contracts table lists now use `Alive`.
      The `alive` helper uses `Alive`. `token_test.go` uses `Owned` only as the identity check, after it proves the
      token with `ps -ww -o command=`.
- [x] The token is still appended (`process.go:190`) and still named by devengine (`funcd-engine-<id>`,
      `devengine.go:249`).
- [x] The first scenario test fails on main's rule (m1 on darwin, m5 on Linux, both `killed=0`, as in #730). Each
      scenario has one named test that passes. The Linux run in Docker passed (above).

## Contracts conformance

- `Entry` matches the contract field for field, including the `bootID,omitempty` tag and the field comments.
- `BootID()` is exported. On Linux it reads `/proc/sys/kernel/random/boot_id` with `sync.OnceValues` and trims the
  value. On macOS it returns `"", nil`.
- `Owned` matches the contract body exactly, and `Alive` is `Owned(e) && !zombie(e.PID)`.
- The per-OS functions are `bootMatches`, `zombie` and the Linux-only `argvContains`. Linux `zombie` returns true for
  state `Z` or `X` in field 3. Darwin `zombie` checks `P_stat == SZOMB`, with a local `sZomb = 5` and a comment on
  why the constant is local. On both OSes a read error makes `zombie` return false, as the contract says.
- `startTime` keeps its behaviour. It was refactored onto shared readers (`statFields` on Linux, `kinfo` on darwin)
  so that `zombie` reads the same source.
- The scope stays inside the files the ADR names, plus `procreg_linux_test.go` for the Linux-only scenario. The
  `cmd/funcd/crashrecovery_test.go` hunks are the three lines the brief assigns to 0187 (131, 193, 230), so the
  rebase onto 0186 is mechanical.

## Verified correct: keep these

- **The identity rule uses only facts the worker cannot rewrite.** The argv read is limited to the legacy branch, and
  the code comment on that branch points to the ADR's exit clause.
- **An empty `boot_id` is treated as an error.** This is a small defensive choice that closes the only route by which
  a new entry could become a legacy entry.
- **The tests are strong.** One named test per scenario, written to the ADR's per-OS expectations. All five mutants
  are killed. `TestScenarioRetitledWorkerReaped` fails when node is missing instead of skipping, as Implementation
  plan step 1 requires. `TestOwned` covers all six cases: live, retitled, another start time, another boot, legacy
  with and without the token, and a dead pid.
- **The zombie scenario is built correctly.** The parent does not wait for the leader until cleanup, and a member
  with `trap '' TERM` survives `exec sleep`. Together these prove both that Reap does not wait out the grace on a
  zombie leader and that the group is SIGKILLed.
- **The `Reap` and package doc comments describe the new rule**, including the case of a zombie leader.
- The commit message records the failing proof on main, and the Docker run reproduces it independently.

## Recommendation

Pass. n1 is a one-line comment fix. It can go into the integrator's batch, or wait for the next change that touches
`process.go`; it does not block. The wave's docs PR then stamps ADR-0187 `Reviewing → Implemented`, moves the
FEAT-0000/F12 row to match, and adds the `Superseded in part by ADR-0187` back-link on ADR-0167 if acceptance has not
added it yet.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0187",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 7,
  "dod_total": 7,
  "report": "docs/reviews/adr-0187-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (ac1698b6): all 4 Decisions + Contracts hold; build/vet/lint clean darwin + Linux (incl. -tags dev); procreg/process/devengine -race ok on darwin and in Docker Linux (uid 1000, node 20); cmd/funcd + cmd/funcdctl crash tests ok; 5 named scenario tests pass; mutants 5/5 killed (main argv rule darwin+Linux -> killed=0, Alive without zombie, boot ignored, legacy without argv); Linux retitle mechanism confirmed. n1 [model] process.go:34 instanceFlag comment still says the reap matches the token."
}
```
