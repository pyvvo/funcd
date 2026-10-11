## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0230 implementation, model: claude-opus-5-5)

**Not stamped.** Minor 1 is attributed to the ADR: Decision 4 names the wrong containerd root. The code is right and
needs no change. The decider chooses between an in-place correction and a superseding ADR. Until then ADR-0230 stays
`Reviewing` and the FEAT-0001 F46 cell stays `shared full-run VM: reviewing`.

Scope: `git log c37b2a0a..b98404e2` on `docs/adr-0230-shared-lane-vm`. The branch holds the acceptance commit
`5cd2a054`, the found-defect fix `9adbd564` (the lanes' funcd units now stop like the installed `funcd.service`) and
the implementation commit `b98404e2`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| lane and unit tests, race | `scripts/agent/d go test ./scripts/... ./cmd/funcd/ -run 'Lane\|Units' -race -count=1` | `ok` × 2, exit 0; with `-v`, the 10 lane tests and the 2 unit tests PASS |
| shell syntax | `bash -n` on `lane-provision.sh`, `lane-ready.sh`, `lane-reset.sh` and `lane-leakcheck.sh` | exit 0 × 4 |
| vet, lint | `go vet ./scripts/... ./cmd/funcd/`; `go tool golangci-lint run ./scripts/... ./cmd/funcd/...` | exit 0; `0 issues.`, exit 0 |
| Lima template | `limactl template validate` (Lima 2.1.2) on `scripts/lima-lane.yaml`, and on a scratch copy whose probe reads `lane-ready.sh` with `file:` | the template is OK; the copy is refused: "`probe[1].file.url` must be empty during validation (script should already be embedded)" |
| moved code | the inline provision and probe of `scripts/lima-lane.yaml` at `9adbd564`, diffed against the new scripts | unchanged, apart from the shebang, the header, the `boot`/`lane` split, `nftables` and the two exits when no lane is staged |
| found-defect revert check | `TestLaneDaemonUnitsStopLikeTheInstalledUnit` on the tree of `5cd2a054` (`git archive`), with the test file added | FAIL: `scripts/lima-lane.yaml:44 starts a funcd unit that stops unlike funcd.service`; PASS at `b98404e2` |
| recipe failure paths | the `lima-example-all` body (`just --show`, variables filled in) run by bash with fake `limactl`, `venom`, `just` and `go`, a fake staging step, a scratch lock dir and an unused VM name; the real `lane.py --venom-lanes` and `lane-lock.sh` | 7 scenarios, all as Decisions 1 and 5 say (table below) |
| single-lane run | `just lima-example env-echo` at `b98404e2`; the lane lock was free and no `funcd-bench*` VM was running | boot 35 s, 16/16 test cases PASS, exit 0 in 128 s; VM deleted, lock released |
| full run, forced failure | not rerun (the caller's instruction); the implementer's run logs in the session scratchpad were read | see "Proofs" below |
| gate | not rerun (the caller's instruction); `.cache/gate/` of the worktree, written 16:11–16:20, after the `b98404e2` commit (16:05) | audit PASS on `c37b2a0a..b98404e2` (0 hard flags); `ci-full` 116 × `ok` and no FAIL, e2e `ok … 371.526s`; Linux lint `0 issues.`; tree unchanged |

The simulated runs of `lima-example-all`:

| Scenario | What the recipe did |
|---|---|
| all green | built once and booted once; reset and leak check after the boot and after each of the 8 lanes, in registry order; stopped and deleted the VM, then ran `just lima-example-metastore`, whose nested `lane-lock.sh` exited 0 with no VM running; `PASSED:` lists all 9; exit 0 |
| the fn-to-fn suite fails | `FAILED: fn-to-fn`; the other 7 lanes and metastore ran; exit 1 |
| the s3 provision fails | no probe or suite for s3, but the reset and the leak check ran; `FAILED: s3`; the run went on; exit 1 |
| the leak check fails after duckdb | `FAILED: duckdb`; "lanes not run: s3 kv workflow funclog egress metastore"; VM deleted; metastore not run; exit 1 |
| the reset fails after the boot | all 8 lanes and metastore listed as not run; VM deleted; exit 1; no `FAILED:` line (Minor 2) |
| the boot fails | the same, with "the boot of … failed" |
| SIGINT to the process group during the duckdb suite | the EXIT trap stopped and deleted the VM; nothing else ran |

In every scenario the recipe shell held the lock from its first step, and the lock was released after it exited.

### Proofs of the implementation plan (from the implementer's logs)
- **Full run**: 492 s from start to `ALL VENOM E2E LANES PASSED` (build 8 s, boot 66 s, the 8 Venom lanes 6 min 12 s,
  metastore 46 s). Every lane passed. The leak check printed 10 zero counts after the boot and after each lane. The
  reset took 32 ms after the boot and 363–655 ms after a lane. The log names its base as `9adbd564` plus the working
  tree; `b98404e2` was committed 19 s after the run ended.
- **Forced failure**: a scratch registry ran egress, with a broken assertion, and then fn-to-fn. Egress failed, the
  reset and the leak check printed zeros, fn-to-fn passed on the same VM, and metastore passed. The summary was
  `PASSED: fn-to-fn metastore` and `FAILED: egress`. This one run gives the plan's "egress then fn-to-fn" proof and
  its forced-failure proof.
- **Single lane**: the implementer's env-echo run at 16:11, and the reviewer's run above.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Minor 1: Decision 4 names the wrong containerd root, so "keeps the containerd root" cannot hold** · attribution:
  adr (reported deviation 3)
  - Evidence: all 9 lane configs set `storage.dataDir: /var/lib/funcd/data`, and none sets `runtime.containerd.root`.
    The config derives the root as `<dataDir>/containerd` (`internal/platform/config/config.go:578-580`), and
    `cmd/funcd/main.go:1362` passes it to the private containerd as `DataRoot`. The suites use the socket
    `/var/lib/funcd/data/containerd/containerd.sock`. `/var/lib/funcd/containerd` is only the fallback of
    `ctrmanager` for an empty `DataRoot` (`internal/runtime/ctrmanager/manager_linux.go:41-42`).
  - Decision 4 orders the removal of `/var/lib/funcd/data`, and its leak check counts that path, so the containerd
    root goes with it. Its sentence "It keeps `/var/lib/funcd/bin` and the containerd root `/var/lib/funcd/containerd`:
    the next funcd's `BootSweep` removes the previous lane's containers at start" is therefore false for the lanes.
    Each lane starts with an empty containerd root, and `BootSweep` finds nothing to remove.
  - The code follows the only reading that agrees with the rest of Decision 4 and with the leak check, and
    `lane-reset.sh:4-6` states it. Keeping `<dataDir>/containerd` would contradict both. It costs no time: the boot
    and the 8 lanes took 7 min 18 s, against 7 min 16 s in the ADR's experiment.
  - Owner: the decider. Correct the last two sentences of Decision 4 in place, or write a superseding ADR. The code
    needs no change.
- **Minor 2: a run that stops before any lane gives `lanes.sh` no cause to print** · attribution: model
  - Evidence: when the boot or the first reset fails, the recipe exits 1 with an empty `PASSED:` line and no `FAILED:`
    line (simulation); the cause is on the "lanes not run" line (`justfile:299`). `scripts/agent/lanes.sh:33` prints
    the last line that matches `final status|FAILED:|lane-lock`, so its summary of such a run names no cause. Before
    this change, a lane whose VM did not boot was listed in `FAILED:`.
  - Fix (builder, optional): add `lanes not run` to that pattern.

### The six reported deviations

| # | Reported deviation | Judgement |
|---|---|---|
| 1 | The probe runs `lane-ready.sh`, which a `mode: data` provision step installs | Necessary: Lima 2.1.2 refuses a probe `file:` (reproduced above). The template still reads the shared script with `file:`, and the probe runs it. No finding. |
| 2 | With no argument, `lane-provision.sh` runs `boot` and then `lane`; the shared VM gets `boot` from the template, and the recipe runs only `lane` | Necessary: Lima passes no argument to a provision script, and a second `boot` through `limactl shell` would run it twice. `boot` runs once per VM and `lane` once per lane, from the same scripts, so Decision 2 holds. No finding. |
| 3 | The reset removes the containerd root together with the data dir | The ADR text is wrong: Minor 1. |
| 4 | The 10 s stop in Constraints was the found defect; the stop takes about 0.25 s after the fix | Context, not decision. The reset kills with SIGKILL, as Decision 4 says. The fix is its own commit, with a test that fails without it. No finding. |
| 5 | The leak check has no count for the CNI iptables lines that the reset removes | Not a deviation. Decision 4's list of counts has no iptables count either, and the script counts exactly its 10 items. No finding. |
| 6 | The reset waits for the unit to leave the active states before `reset-failed`, and runs `journalctl --sync` before `--rotate` | Implementation detail that makes Decision 4's steps work: a unit that is still stopping stays loaded, and unsynced lines would land after the rotation. No finding. |

Decision 2's sentence on how the template calls the scripts does not match Lima 2.1.2, and the 10 s stop in
Constraints was measured before the fix. Neither changes a decision; the decider may correct their wording together
with Minor 1.

### ✅ Verified correct (keep it)
- **Decision 1**: the recipe takes the lock first (`justfile:265`) and builds the images and the 3 binaries once
  (`:270-274`). It deletes a leftover VM and removes `lane.tgz` before the boot (`:279-281`), runs the lanes in
  registry order, and deletes the VM on every exit (`trap down EXIT`, `:279`) and before the metastore lane (`:297`).
- **Decision 2**: the boot part of `lane-provision.sh` installs funcdctl, python3-yaml and nftables, and the lane part
  runs only with a `lane.tgz` (`:16-26`). `lane-ready.sh` exits 0 without `/opt/lane/LANE` (`:6`). The template keeps
  no provision or probe code of its own (`TestLaneTemplateRunsTheSharedScripts`). After the boot, the leak check
  printed `data-dir=0 opt-lane=0 tokens=0`.
- **Decision 3**: per lane, `lane.py` stages it, then `lane-provision.sh lane`, `lane-ready.sh`, the suite, the reset
  and the leak check run (`justfile:290-294`). The reset and the leak check also run once after the boot (`:286`).
- **Decision 4**: `lane-reset.sh` has every removal: SIGKILL and `reset-failed` (`:15-27`), `cgroup.kill` and the
  cgroups (`:30-41`), the netns pins and the task mounts (`:45-52`), `funcd0`, `/var/lib/cni`, the CNI iptables lines
  and the two nftables tables (`:55-65`), the journal (`:69-70`) and the three paths (`:72`). It is idempotent: on a
  fresh VM it took 32 ms and 76 ms, and the leak check then printed zeros. `lane-leakcheck.sh:9-20` counts exactly
  the 10 items of Decision 4 and exits 1 on any count that is not zero.
- **Decision 5**: the 7 simulated scenarios above.
- **Decision 6**: no call of a bare `ctr` is left in `e2e/` or `scripts/`. `funcd install` lays down `bin/ctr`
  (`internal/runtime/provision/provision.go:103`). The env-echo cases that kill a worker with it passed in the
  reviewer's run.
- **Tests**: the three tests of Implementation plan item 5 are in `scripts/lanes_test.go`.
  `TestEveryLaneConfigSetsTheResetDataDir` reads the config of all 9 lanes, the optional lane included.
- **Found defect** (`9adbd564`): `TestLaneDaemonUnitsStopLikeTheInstalledUnit` reads `KillMode` and `TimeoutStopSec`
  from the rendered unit, checks every `systemd-run --unit funcd-*` call in `scripts/` and `e2e/`, and fails without
  the fix. The env-echo `daemon-restart` case, which starts funcd with that unit line, passed in the reviewer's run.
- **Tracking**: after acceptance, the ADR changed only in its Status line (`Accepted → Reviewing`). The F46 cell reads
  `shared full-run VM: reviewing`.
- **Conventions**: every added YAML line is block style, the Python heredocs import at their top, and the comments
  give the reason, not the steps.

### Definition of Done
17/17 hold: the 4 Review-checklist items, the 5 Definition-of-done items, and 8 generic items (scenario proofs, no
stubs, contracts, tree, conventions, dependencies, scope and tracking). Two items rest on logs that were read, not
rerun, as the caller instructed: the full shared run and the gate. Minor 1 leaves no item open: the reset and the
leak check cover every item, and only Decision 4's description of the root is wrong.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0230 (implementation) → pass, 0/0/2, 1 model-attributed, DoD 17/17. See
`docs/reviews/model-scorecard.md`.

### Recommendation
The work is done. The decider chooses how to correct Decision 4 (Minor 1). After an in-place correction, the
remaining steps are the stamp `Reviewing → Implemented`, naming this report, and the F46 cell
`shared full-run VM: implemented`. Minor 2 is an optional one-line fix in `scripts/agent/lanes.sh`.

`origin/main` moved to `46e2313c` (#960) after the branch was cut. `git merge-tree` shows a text conflict in
`scripts/lanes_test.go`, where both commits add tests at the end of the file, and a clean merge of `justfile`. The
rebase and a new gate run, which gains the `linux-tests` step of #960, are integration work, not a finding.
