## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0194 implementation, model: claude-opus-5-5)

Branch `feat/adr-0194-duration-strings`, head 7affea48 (PR #832; the remote branch is at the same commit), 9 commits on
merge base 64bd360b. `origin/main` is now a9dd0e7e, two docs-only commits ahead (the ADR-0195 and ADR-0197
acceptances); `git merge-tree` of the head against `origin/main` is clean. Loop 4. This loop checks only what changed
since loop 3 (head 511215cc): the rebase and the new pin commit.

### Loop-3 findings

| Loop-3 finding | Status at 7affea48 |
|---|---|
| Major 1: funcd-typescript is still pinned at v0.8.1 | **Resolved.** Commit 7affea48 changes only `go.mod` (`v0.8.1` → `v0.8.2`) and `go.sum` (the two v0.8.2 lines). |

### ✅ Verified correct (keep it)

- **The rebase changed nothing else.** `git range-diff 511215cc~8..511215cc HEAD~9..HEAD~1` marks all 8 commits `=`
  (5b2b8719→b2f8eb4b, d83e8f54→e9ecbf80, 75cf5e9d→acc6a656, 6d2fd99a→d12119bb, 9581b8f2→f2c83856,
  ecf52760→d479e10b, f6b05673→d97296c1, 511215cc→768b2235). The patches are identical, so the loop-1 to loop-3
  evidence (build, vet, lint, `-race` package tests, scenario tests, bounds tables, mutants, conformance) carries over.
- **The pin.** `go.mod` line 40 reads `github.com/pyvvo/funcd-typescript v0.8.2`; funcd-python stays at v0.5.1.
  `go mod verify`: "all modules verified". `git ls-remote --tags` on funcd-typescript lists `v0.8.2` at 8c8dc95a.
  `scripts/agent/d scripts/moddir.sh github.com/pyvvo/funcd-typescript` resolves to the `@v0.8.2` directory, and line 15
  of its `examples/workflow/schedule-source.yaml` reads `interval: 5s`.
- **What v0.8.2 changes.** A recursive diff of the two module-cache directories lists only the example above, a shim test
  (`shim/test/funclog.test.ts`), the changelog and the version files. No `shim/src` file differs, so the embedded shim
  that `cmd/funcd` and `cmd/funcdctl` import is unchanged. `go build ./cmd/funcd ./cmd/funcdctl`: exit 0.
- **Decode probe** (a test file mapped into `pkg/sdk` with `go test -overlay` from the session scratchpad; the work was
  not edited, and `git status` stayed clean):
  - `sdk.DecodeManifest` on the v0.8.2 `schedule-source.yaml` returns an `*EventSource` whose
    `Spec.Timer.Events[0].Interval` is 5s; `Validate()` returns nil (5s is inside the 100ms to 24h bound); the event
    marshals as `{"name":"tick","interval":"5s"}`.
  - Control: the same call on the v0.8.1 file fails with `v1alpha1.Duration.UnmarshalJSON: 5000000000 is not a JSON
    string: want a duration: …`, so the pin is what makes the lane's example apply.
  - All API manifests in the pinned examples decode with `sdk.DecodeManifests`: 40 in funcd-typescript v0.8.2 and 24 in
    funcd-python v0.5.1. The probe also picked up the 8 `funcdconfig.yaml` files, which are not API resources (`unknown
    kind "FuncdConfig"` is expected there). A second overlay probe loads those 8 files with `config.Load`: all pass,
    so no example config carries a duration that the new config grammar refuses.
- `go test -count=1 ./pkg/sdk/`: ok.
- **Conformance and propagation**: the ADR and the FEAT-0000 F02 sub-status are unchanged since loop 3 (range-diff
  `=`): ADR `Reviewing`, sub-status `reviewing`. The new commit names no machine path or local username. Its author is
  green-0-rabbit and it carries the attribution line. Older ADRs that mention funcd-typescript v0.8.1 (0187, 0198) are
  frozen history; they are not a hygiene issue, because `just check-hygiene` limits version pins, not prose.
- **Repo gate**: reported as GATE PASS on this head (audit, ci-full with e2e, linux, tree). This review did not rerun it.

### Definition of Done

7 of 8 items verified (5 Review-checklist items and 3 ADR DoD items).

- Checklist item 5 (the funcd-typescript pin is the new tag) now holds.
- Pending, not a finding: DoD item 1, the `workflow` Lima lane green with the new pin, and with it the scenario
  `schedule-source-example-runs`. The lane runs separately and this review did not run it. Its precondition holds:
  the pinned example decodes and validates in process, and the old pin's example is refused.

### Model scorecard

Not recorded by this gate run. The integrator records the row below.

### Recommendation

Pass. The only open item is the `workflow` Lima lane result (DoD item 1). If the lane is green, the review gate can
stamp ADR-0194 `Reviewing → Implemented` and move the FEAT-0000 F02 sub-status to `implemented`. If the lane fails,
loop back with the lane log. Rebase onto `origin/main` (a9dd0e7e) before queueing if the merge queue requires it; the
merge is clean.

```json
{"date": "2026-10-07", "adr": "0194", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 0, "model_attributed": 0, "dod_passed": 7, "dod_total": 8, "report": "docs/reviews/adr-0194-implementation-claude-opus-5-5-4.md", "notes": "loop 4 (7affea48, PR #832): loop-3 M1 resolved, go.mod/go.sum pin funcd-typescript v0.8.2 (go mod verify ok; schedule-source.yaml interval: 5s; v0.8.2 changes no shim/src file). Rebase onto 64bd360b changed nothing (range-diff 8/8 =); merge-tree clean on origin/main a9dd0e7e. Overlay probe: sdk.DecodeManifest decodes the pinned example (5s, Validate ok, marshals \"5s\"); v0.8.1 example refused; all 64 example API manifests (ts 40, py 24) decode; 8 example funcdconfigs pass config.Load. Gate PASS reported on this head (not rerun). Pending: workflow Lima lane (DoD 1, run separately)."}
```
