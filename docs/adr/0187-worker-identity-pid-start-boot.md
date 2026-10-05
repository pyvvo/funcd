# ADR-0187: Worker identity — pid, start time and boot, never the worker's own argv

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: runtime, process-driver, crash-only
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port)
- **Supersedes in part**: [ADR-0167](0167-process-worker-crash-recovery.md) (Implemented), these clauses only; the
  `Superseded in part by ADR-0187` back-link is added to ADR-0167 at acceptance:
  - Decision 1 (lines 91-92), the entry's field list "(instance ID, pid, pgid, start time, identity token, temp-file
    paths)": it gains the boot ID (Decision 2 here).
  - Decision 4 (lines 105-114), the rule "an entry is ours only if the live pid's start time equals the saved one
    **and** its argv contains the token" and the sentences that justify the argv check: replaced by Decision 1 here.
    The token append (lines 105-106) and "never signals a pgid other than the saved one" (line 114) stand.
  - Contracts (lines 152-159 `Entry`, 171 `argvContains`, 172 `Owned`): replaced by the Contracts here.
  - Scenario `reused-pid-never-killed` (lines 44-46), Implementation plan step 1's "a reused pid (a child started
    without the token)" (lines 192-193) and the Review checklist line "start time and argv token both match" (line
    208).
  Decisions 2, 3 and 5-9 stand here (ADR-0186 supersedes a separate clause of Decision 8).
- **Relates to**: [ADR-0014](0014-platform-facade-lifecycle-harness.md) C4 (crash-only boot, unchanged) ·
  [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (no shim release needed) · [ADR-0186](0186-containerd-clean-slate-at-boot.md) (supersedes a
  different clause of ADR-0167, Decision 8; no overlap).

## Context & Need

Issue [#730](https://github.com/pyvvo/funcd/issues/730), reproduced on macOS on main a394c6f1 (release 0.6.0) and
re-run by an independent refuter; code lines below are at origin/main 6b06320c. The crash reap (ADR-0167) calls a
saved worker "ours" only if its live argv still contains `--funcd-instance=<id>`: `Owned`
(`internal/runtime/procreg/procreg.go:200-209`) calls `argvContains` (`identity_darwin.go:27-53` reads
`kern.procargs2`, `identity_linux.go:38-50` reads `/proc/<pid>/cmdline`). That memory belongs to the worker. Node's
`process.title` and Python's `setproctitle` overwrite it, so a handler, or a library it loads, removes the token. The
reap (`procreg.go:102`) then skips the worker, and after a restart the orphan serves next to the new replica: the
duplicate that #44 removed. The probe showed that start time and pgid still match after the title change; only the argv
check fails. The pinned shims (funcd-typescript v0.8.1, funcd-python v0.5.1) never set a title, so the trigger is
always handler code. The Linux side was not run; the mechanism (the title setter writes argv memory, `cmdline` reads
it back) is inferred.

`Owned` also serves as a liveness probe: the Reap wait loop (`procreg.go:116-122`) and tests treat `!Owned` as
"gone". A zombie keeps a readable, matching start time; today only the argv read (it fails or comes back empty for a
zombie) makes a zombie count as gone.

On Linux, the `funcd install` unit has `KillMode=mixed` (`cmd/funcd/install.go:55`), so systemd already kills
leftover workers when the unit stops or restarts (read in code, not run). The gap is macOS, `funcdctl dev`, and a
daemon not started by the unit.

**Purpose.** `procreg.Owned` tells the reap, called once at driver open by the process driver and the dev catalog
engine, whether a saved pid still names the process funcd started, using only facts the process cannot rewrite.

## Scenarios

- **scenario: retitled-worker-reaped** (the #730 reproduction) — Given a registry entry left by a crashed run for a
  live Node worker that set `process.title`, so its argv no longer holds the token, with its saved pid, pgid and start
  time, When the driver opens, Then the reap signals its process group, `Reap` reports `killed=1`, and the worker is
  gone.
- **scenario: reused-pid-never-killed** — Given an entry whose pid now belongs to a process with another start time,
  When the driver opens, Then that process is not signalled and the entry is cleared.
- **scenario: other-boot-entry-never-killed** (Linux) — Given an entry whose `bootID` differs from the current boot ID
  while a live process has its pid and start time, When the driver opens, Then that process is not signalled and the
  entry is cleared.
- **scenario: legacy-entry-rule** — Given an entry without `bootID` (written by an earlier release) whose live pid has
  the saved start time, When the driver opens, Then on Linux it is reaped only if its argv contains the token, and on
  macOS it is reaped whether or not its argv contains the token.
- **scenario: zombie-leader-counts-as-gone** — Given an owned entry whose leader exits on SIGTERM but stays a zombie
  (its parent does not wait for it) and a group member that ignores SIGTERM, When the driver opens with a 2 s grace,
  Then `Reap` returns in under 1 s (it polls every 20 ms, `procreg.go:22`) and the member is gone.

## Scope

**In:** the identity rule of `procreg.Owned`; the `bootID` entry field; a liveness check apart from identity; the
tests that use `Owned` as a liveness probe.

**Out:** handler code that wants to escape (it calls `setsid`/`setpgid` or double-forks): not a security boundary;
signalling the pid as well as `-PGID` (Open questions); a cgroup per worker and the lifeline (backlog board); the
containerd driver; the shims.

## Constraints & Decision drivers

- Never signal a process funcd did not start (ADR-0167 Constraints): a pid plus its start time names one process
  within one boot.
- Linux start time counts clock ticks since boot, so after a reboot a saved entry can match an unrelated process;
  the argv token was in practice the only Linux reboot guard. macOS `p_starttime` is µs since the epoch.
- The registry lives in the data dir, not on a tmpfs, so a reboot does not clear it; the boot scope must be explicit.
- No shim release (ADR-0141), no config key, no user-visible change.
- An upgrade after a crash must never kill a wrong process.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. pid + start time, Linux boot ID, liveness apart** | Nothing the worker can rewrite; one package; no shim or config change | One new field; a legacy branch for one upgrade | **chosen** (decided) |
| A with "live pgid == saved pgid" as the Linux reboot guard | No new field | A no-op: every entry saves `PGID == PID` (`process.go:264`, `devengine.go:191`) and Reap signals only `-PGID`, which lands only when P leads group P | rejected |
| A with the registry file's mtime against the boot time | No new field | Depends on a wall clock that can be wrong at boot | rejected |
| Linux entry without `bootID`: skip it | Simple | A crash before the upgrade leaks its orphans once | rejected |
| Linux entry without `bootID`: treat it as this boot | Simple | Reopens the reboot hole for that open | rejected |
| Also record macOS `kern.bootsessionuuid` | Symmetric entries | No gain: `p_starttime` already differs across boots | rejected (decided: Linux only) |
| B. Lifeline fd watched by the shim | No orphan at all | Shim releases in both language repos, a funcd ↔ shim contract ADR (ADR-0141), no cover for non-shim workers | rejected; stays on the backlog |
| C. Keep the rule; tell authors not to set a title | No code | The duplicate after a crash stays | rejected |

Prior art (survey of 2026-10-05): no surveyed platform identifies its processes by argv or env. systemd, kubelet
and Nomad use cgroup membership; containerd-shim and supervisors keep a parent alive; runc compares the start time
of a pid whose state dir lives on tmpfs, which scopes it to one boot.

## Decision

1. **Identity.** `Owned(e)` is true only if the live pid's start time equals `e.StartTime` **and** `bootMatches(e)`:
   - Linux, `e.BootID != ""`: the current boot ID (`/proc/sys/kernel/random/boot_id`, whitespace trimmed, read once
     per process) equals `e.BootID`.
   - Linux, `e.BootID == ""` (an entry of an earlier release): today's rule, `argvContains(e.PID, e.Token)`.
   - macOS, every entry: true, since `p_starttime` (µs since the epoch) already differs across boots.

   Any read error means "not ours". The argv is never read for an entry that carries a `bootID`, nor on macOS. The
   reap still never signals a pgid other than the saved one.
2. **The boot ID field.** Each entry gains `bootID` (new). Both owners read `procreg.BootID()` next to the start time
   and store it: the process driver in `saveLocked` (`internal/runtime/process/process.go:259-266`), devengine in
   `save` (`internal/catalog/devengine/devengine.go:187-191`). On macOS it is `""`. On Linux a `BootID()` error fails
   the save and is returned, as a `StartTime` error is at both sites (`process.go:259-262`, `devengine.go:187-190`):
   an empty `bootID` would make the entry a legacy one and bring #730 back for that worker.
3. **The token stays.** The driver still appends `--funcd-instance=<id>` (`process.go:188-191`) and devengine still
   names `funcd-engine-<id>`, both saved as `Token`, so `ps` names the instance of a worker that did not retitle
   itself, manual cleanup can find it, and a Linux reader of a legacy entry still has a token. ADR-0167's "Exposes"
   row is unchanged.
4. **Liveness apart from identity.** `Alive(e)` (new) is `Owned(e)` and the pid is not a zombie: Linux `stat` field 3
   is `Z` or `X`; macOS `kinfo_proc.kp_proc.p_stat == SZOMB` (5 in `sys/proc.h`; `golang.org/x/sys/unix` has no
   constant, so a local one). Reap picks the groups to signal by `Owned` (`procreg.go:102`), waits on `Alive`
   (`:116-122`), and at the deadline sends SIGKILL to every still-`Owned` group (`:125-129`). A zombie leader thus
   counts as gone and its group gets SIGKILL at once, as a leader's exit does today; a zombie leader whose group
   still has live members now gets that group signalled. Tests that ask "does it still run" use `Alive`.

## Temporary workarounds

- **The Linux legacy branch** (Decision 1, second bullet): an entry without `bootID` keeps ADR-0167's argv rule, so an
  upgrade after a crash never kills a wrong process. Each open empties the registry (`procreg.go:137-138`), so a
  legacy entry is read at most once. **Exit:** the branch and the Linux `argvContains` are deleted in the first minor
  release after the one that ships this ADR. From then on a Linux entry without `bootID` is not ours: it is skipped,
  and its files and the registry are cleared as for any entry. A user who jumps after a crash from a release before
  this ADR straight to that release leaks those orphans once; accepted.

## Contracts

```go
// internal/runtime/procreg
type Entry struct {
	ID        string   `json:"id"`
	PID       int      `json:"pid"`
	PGID      int      `json:"pgid"`
	StartTime uint64   `json:"startTime"`        // OS-native: clock ticks since boot (Linux), µs since epoch (macOS)
	BootID    string   `json:"bootID,omitempty"` // new: Linux boot_id at save; "" on macOS and in entries of earlier releases
	Token     string   `json:"token"`            // still in argv; checked only for a Linux entry without BootID
	Files     []string `json:"files"`
}

func StartTime(pid int) (uint64, error) // unchanged
func BootID() (string, error)           // new: Linux boot_id, trimmed, read once (sync.OnceValues); macOS "", nil

func Owned(e Entry) bool {
	st, err := startTime(e.PID)
	if err != nil || st != e.StartTime {
		return false
	}
	ok, err := bootMatches(e)
	return err == nil && ok
}

func Alive(e Entry) bool { return Owned(e) && !zombie(e.PID) } // new

// per OS, build-tagged
func startTime(pid int) (uint64, error)                // unchanged
func bootMatches(e Entry) (bool, error)                // new; Decision 1
func zombie(pid int) bool                              // new; read error means false (Owned already failed)
func argvContains(pid int, token string) (bool, error) // Linux only (legacy branch); the darwin copy is deleted
```

| Site (origin/main 6b06320c) | Change |
|---|---|
| `internal/runtime/procreg/procreg.go:25-32` | `Entry` gains `BootID` |
| `procreg.go:116-122` | wait loop uses `Alive` |
| `procreg.go:200-209` | `Owned` per the Contract; `Alive` and `BootID` added |
| `identity_linux.go:14-36`, `:38-50` | `bootID`, `bootMatches`, `zombie` (field 3, parsed as at `:21-27`); `argvContains` kept for the legacy branch |
| `identity_darwin.go:14-25`, `:27-53` | `bootMatches`, `zombie` (`P_stat`); `argvContains` deleted |
| `internal/runtime/process/process.go:259-266`, `internal/catalog/devengine/devengine.go:187-191` | store `BootID` |
| `procreg_test.go:42` (`startChild`), `:46-48` (`alive`), `:211` | entries carry `BootID: mustBootID(t)` (a test helper over `procreg.BootID()`, like `mustStart`) (else every Linux test entry is a legacy one); `alive` uses `Alive` |
| `procreg_test.go:116-132` (`TestOwned`), `:136-154` (`TestScenarioReusedPidNeverKilled`) | follow this rule |
| `internal/runtime/process/token_test.go:113` | proves the token with `ps -o command= -p <pid>`, then `Owned` |
| `cmd/funcd/crashrecovery_test.go:131`, `:193`, `:230`; `cmd/funcdctl/dev_crash_test.go:65`, `:104`, `:110`, `:242`; `internal/catalog/devengine/reap_test.go:65`, `:81` | liveness probes use `Alive` |

| Consumes | Exposes |
|---|---|
| `/proc/<pid>/stat` fields 3 and 22, `/proc/sys/kernel/random/boot_id`; `/proc/<pid>/cmdline` for a legacy Linux entry only; macOS `kern.proc.pid.<pid>` (`p_starttime`, `p_stat`) | `bootID` in each `workers.json` and `engines.json` entry; the argv token unchanged |

No config key; no YAML.

## Implementation plan

1. **Prove first.** Add `TestScenarioRetitledWorkerReaped` in `internal/runtime/procreg`: start
   `node -e 'process.title="my-function"; console.log("up"); setInterval(()=>{},1000)' -- --funcd-instance=default/f/r1`
   with `Setpgid`, read one line, `seed` the entry, `Open`, `Reap(ctx, 2s)`; expect `killed == 1` and the worker gone.
   Run it on current main and record that it fails with `killed=0`, as in #730. node comes from PATH (the flake has
   none); unlike the node tests that skip (`cmd/funcd/main_test.go:213`), a missing node fails this one. CI relies on
   the `ubuntu-latest` image's node (not checked in a run); if CI lacks it, the flake gains a pinned `nodejs`, never a
   skip.
2. `Entry.BootID`, `BootID()`, `bootMatches`, the new `Owned`; delete the darwin `argvContains`; store `BootID` at the
   two owner sites. From here on `startChild`, `alive`, the step-1 test and `procreg_test.go:211` fill
   `BootID: mustBootID(t)`; only `TestScenarioLegacyEntryRule` (empty) and `TestScenarioOtherBootEntryNeverKilled`
   (foreign) set it by hand. Without that, every Linux test entry takes the argv rule and step 1 stays at `killed=0`.
3. `zombie`, `Alive`, the Reap wait loop on `Alive`; move the liveness probes listed in Contracts to `Alive`.
4. One test per scenario, named after it: `TestScenarioRetitledWorkerReaped` (step 1);
   `TestScenarioReusedPidNeverKilled` rewritten to "another start time" (a live child without the token and with
   the saved start time is now ours); `TestScenarioOtherBootEntryNeverKilled` (Linux, entry seeded with a foreign
   `bootID`); `TestScenarioLegacyEntryRule` (per-OS expectations); `TestScenarioZombieLeaderCountsAsGone` (a leader
   the test does not wait for, with a `trap '' TERM` member). `TestOwned` covers a live child, another start time,
   another boot (Linux), a legacy entry and a dead pid.
5. Run the `procreg`, `process` and `devengine` tests once on Linux in Docker: a throwaway image `FROM golang:1.26.4`
   (go.mod) plus `apt-get install -y nodejs` (Debian's node; any version sets `process.title`), run with
   `--user 1000:1000` and HOME, GOCACHE and GOMODCACHE under `/tmp`; the Linux path was never run before. Done when
   the touched packages' tests, vet and lint pass on macOS and Linux; the repo-wide checks run once per PR in
   `scripts/agent/gate.sh`.

## Review checklist

- [ ] `Owned` reads argv only for a Linux entry whose `BootID` is empty; the darwin `argvContains` is gone.
- [ ] Both owner sites store `BootID` next to `StartTime`; a Linux entry with another boot ID is never signalled.
- [ ] Any read error in `Owned` means "not ours".
- [ ] Reap selects by `Owned`, waits on `Alive`, and SIGKILLs a still-`Owned` group at the deadline.
- [ ] No test uses `Owned` to mean "still runs".
- [ ] The token is still appended at `process.go:188-191` and still named by devengine.
- [ ] The first scenario test failed on main before the change; one named, passing test per scenario; the Linux run
      in Docker passed.

## Consequences

- Positive: a worker that rewrites its argv is reaped on both OSes; identity no longer depends on memory the worker
  controls; a zombie leader with live group members gets its group signalled.
- Negative: each entry gains one field; the Linux legacy branch stays until its exit (Temporary workarounds).
- Risks accepted: code that wants to escape still can (`setsid`, double fork); identity relies on no reused pid
  starting in the same start-time unit as the saved one within one boot (one µs on macOS, one clock tick on Linux:
  10 ms at `USER_HZ=100`), as ADR-0167 already did.

## Open questions

- Signal the owned pid as well as `-PGID`, so a worker that calls `setsid`/`setpgid` is still killed (the escape that
  Nomad `raw_exec` documents)? Answered by a follow-up ADR or a board card; not decided here.

## References

- Issues [#730](https://github.com/pyvvo/funcd/issues/730), [#44](https://github.com/pyvvo/funcd/issues/44);
  ADR-0167 (Decisions 1, 4; Contracts), ADR-0014 C4, ADR-0141; ADR-0186
- Linux `proc(5)`: `/proc/<pid>/stat` fields 3 (state) and 22 (starttime), `/proc/sys/kernel/random/boot_id`; macOS
  `sys/proc.h` (`SZOMB`), `sysctl` `KERN_PROC_PID`
- Prior art: runc `hasInit` (start-time compare, state on tmpfs); Nomad `raw_exec`; systemd and kubelet cgroup
  tracking
