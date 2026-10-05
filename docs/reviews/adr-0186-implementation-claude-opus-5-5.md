# ADR-0186 implementation review — loop 1 (claude-opus-5-5)

- **ADR**: docs/adr/0186-containerd-clean-slate-at-boot.md (Accepted 2026-10-05)
- **Work**: branch `feat/adr-0186-containerd-clean-slate`, one commit `09c4b85d` on `65386fe0` (origin/main)
- **Files**: `internal/runtime/containerd/containerd_linux.go` (+52), `internal/runtime/containerd/bootsweep_linux_test.go`
  (+237), `internal/runtime/containerd/close_linux_test.go` (+12/-6), `cmd/funcd/main.go` (+7/-6),
  `cmd/funcd/crashrecovery_test.go` (+2/-2). No go.mod, docs or config change.
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (neither model-attributed).

## Verification run

All host commands ran in the worktree through `scripts/agent/d`. The Linux tests ran in Docker (`golang:1.26.4` on
colima, `--user 1000:1000`, the tree streamed in by tar, the module cache mounted read-only).

| Check | Where | Result |
|---|---|---|
| `go build ./...` | darwin | exit 0 |
| `GOOS=linux go build ./...` | darwin host | exit 0 |
| `go vet ./cmd/funcd/ ./internal/runtime/containerd/` | darwin and `GOOS=linux` | exit 0 / exit 0 |
| `golangci-lint run ./cmd/funcd/ ./internal/runtime/containerd/` | darwin and `GOOS=linux` (host binary) | 0 issues / 0 issues |
| `gofmt -l` on both packages | darwin | clean |
| `go test -race -count=1 -v ./internal/runtime/containerd/` | Linux container | exit 0; all 7 new tests PASS, `TestScenarioContainerdLeftoverRemovedAtBoot` and every `TestIssue706_*` PASS |
| `go test -race -run TestContainerdModeSweepsLeftoversAtBoot ./cmd/funcd/` | darwin and Linux container | ok / ok |

Not run, as instructed: e2e, `go test ./...`, Lima, `just ci-full` (the per-PR `scripts/agent/gate.sh` runs those).

### Overlay mutants (`go test -overlay`, the work itself untouched)

| Mutant | Line changed | Result |
|---|---|---|
| m1 — `BootSweep` is a pure delegate to `SweepAll` (the plan's step-1 state, i.e. today's behaviour) | `containerd_linux.go` BootSweep body | **killed**: ImagesClearedAtBoot, OverrideChangeLeavesOneImage, CuratedImageFromRunningBinary, NamespaceNotEmptyRetriedNextBoot FAIL |
| m2 — image Delete without `images.SynchronousDelete()` | image Delete opts | **killed**: ImagesClearedAtBoot, OverrideChange, CuratedImage FAIL |
| m3 — drop the `!d.cfg.Private` early return | BootSweep guard | **killed**: ExternalContainerdKeepsImages FAIL |
| m4 — ignore the namespace Delete error (boot dir removed anyway) | namespace Delete | **killed**: NamespaceNotEmptyRetriedNextBoot FAIL |
| m5 — drop the `funcd-` prefix filter | namespace loop | **killed**: ImagesClearedAtBoot FAIL (`other` deleted) |
| m6 — `cmd/funcd/main.go` from origin/main (hook still calls `SweepAll`) | hook method | **killed**: TestContainerdModeSweepsLeftoversAtBoot/swept and /sweep_failed FAIL ("the boot sweep runs once") |
| m7 — namespace Delete even when an image Delete failed | the `len(errs) > 0` gate | survived (see Minor 2) |
| m8 — a NotFound from image Delete counts as an error | the `!errdefs.IsNotFound` clause | survived (see Minor 2) |

m1 is the plan's step-1 proof re-run by the reviewer: with `BootSweep` delegating to `SweepAll`, the scenario tests fail
because no image and no namespace Delete is recorded, which is issue #729's reason.

## Findings

### Blocker
None.

### Major
None.

### Minor

- **Minor 1 — Implementation-plan step 5 (one run on a real containerd in a Lima VM, with the sweep duration and the
  first `Create` time) is not in the work.** The Definition of Done asks for it to be recorded. Neither the implementer
  nor this review may run Lima in this campaign; the main loop runs the containerd Lima lanes on the branch before
  merge. · attribution: **env** · action: record `ctr namespaces ls` (no `funcd-x`), the sweep duration and the first
  `Create` time in the PR when the lane runs.
- **Minor 2 — Two branches of Decision 4 have no test.** No fake makes an image Delete fail with a non-NotFound error
  or return NotFound, so mutants m7 (namespace Delete despite a failed image Delete) and m8 (NotFound counted as a
  failure) both pass the suite. The code is correct by reading (`clearNamespace` returns before the namespace Delete
  when `errs` is non-empty, and skips NotFound). Impact is low: on a real containerd m7 is turned into a
  FailedPrecondition by the namespace delete itself, and m8 only delays a namespace to the next boot. The plan's
  enumerated test list, which the work meets exactly, does not ask for these cases. · attribution: **adr** (test plan
  gap) · action: optional — a `deleteErr` on `recordingImages` would cover both in one table test.

## Contracts and Review checklist

| Item | Holds | Evidence |
|---|---|---|
| `bootSweeper` declares `BootSweep`, `main.go` calls it; `Close` still calls `SweepAll` only | yes | `cmd/funcd/main.go:781-783`, `:869`; `Close` at `containerd_linux.go:918-920` unchanged; m6 killed; ShutdownKeepsImages PASS |
| `TestContainerdModeSweepsLeftoversAtBoot` kept, `sweptRuntime` implements `BootSweep`, log assertion expects the new message | yes | `cmd/funcd/crashrecovery_test.go:287`, `:308`; warn text matches the Contract exactly |
| `BootSweep` returns early on a `SweepAll` error and on `!d.cfg.Private` | yes | `containerd_linux.go:967`; m3 killed; `TestBootSweep_ContainerSweepErrorDeletesNoImage` PASS |
| Every image Delete passes `images.SynchronousDelete()`; namespace Delete only after every image Delete succeeded; boot dir only after the namespace Delete | yes (by reading; see Minor 2) | `containerd_linux.go:994-1010`; m2, m4 killed |
| Only `funcd-` names are touched | yes | `containerd_linux.go:976`; m5 killed |
| A sweep error never fails startup | yes | `main.go:869-871` logs at warn and continues; `sweep_failed` subtest PASS |
| One test per scenario, named as in the plan, plus the unit test; the step-1 failure is shown | yes | all 7 names match; step-1 state reproduced by m1 (the PR body is the integrator's) |
| No new config key, port method, label or dependency | yes | `core/images` is already in the containerd module; go.mod untouched; no port or config change |
| Contract calls: `NamespaceService().List/Delete` on plain ctx, `ImageService().List/Delete` on `namespaces.WithNamespace`, `os.RemoveAll` skipped when `bootRoot == ""`, op `runtime.containerd.BootSweep` | yes | `containerd_linux.go:965-1011`; the `os.RemoveAll` error uses `fault.Wrapf(…, fault.Internal, …)`, which is what `mapErr`'s default case produces |
| `newCloseFixture` keeps its signature; `newBootFixture` passes both fakes in place of `fakeClient`'s defaults | yes | `close_linux_test.go:154-167` (`closeFixtureWith`); `bootsweep_linux_test.go` `newBootFixture` |

Definition of Done: the step-1 failure (reproduced, m1), every listed test passes with `-race` (yes), the repo-wide
checks once per PR in `gate.sh` (integrator), the Lima check (not yet, Minor 1). Total 11 of 12 checklist + DoD items
hold.

## Verified correct (keep it)

- **Small, exact change.** One driver method plus one helper, `clearNamespace`, each step gated on the one before it,
  as Decision 4 orders. `Close` and `SweepAll` are untouched, so shutdown stays bounded by `closeGrace`.
- **Strong scenario tests.** The `recordingImages` fake records whether each Delete was synchronous, so a dropped
  `SynchronousDelete` fails three tests (m2). The namespace-not-empty test also runs a second boot and proves the retry
  deletes both the namespace and the boot dir. The override test proves the first `Create` after the sweep starts
  exactly one pull of the new ref.
- **Isolation of the shared fixture.** `closeFixtureWith` lets the new tests swap in their fakes without changing
  `newCloseFixture`'s callers, and the existing `TestIssue706_*` and leftover-at-boot tests pass unchanged.
- **Brief compliance.** The `crashrecovery_test.go` edit stays inside lines 284-308, clear of ADR-0187's lines, so the
  integrator's rebase stays mechanical.

## Notes for the integrator

- The commit carries `Refs #729`, not `Fixes #729`. The ADR leaves the external-containerd leak open until a follow-up
  ADR, so keeping #729 open is defensible; decide in the PR description whether it closes the issue.
- Tracking (ADR `Accepted → Reviewing → Implemented`, the FEAT-0000/F12 row) is not moved in the work, as agreed: the
  wave's docs PR does it. Status for that PR: pass.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0186",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 0,
  "dod_passed": 11,
  "dod_total": 12,
  "report": "docs/reviews/adr-0186-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (09c4b85d on 65386fe0); Decisions 1-5 + Contracts hold (BootSweep = SweepAll then, private only, sync image deletes -> namespace -> boot dir; hook + warn text exact); build darwin+linux, vet+lint darwin+linux clean; containerd -race green on Linux in Docker (7 new tests + 706/leftover tests), cmd/funcd hook test green; mutants 6/8 killed incl. the step-1 delegate state and the main.go revert; survivors m7/m8 are untested Decision-4 branches (minor, adr test-plan gap); Lima step 5 not run (minor, env, main loop); tracking deferred to the wave docs PR."
}
```
