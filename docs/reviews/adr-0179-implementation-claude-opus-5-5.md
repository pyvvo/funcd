## Verdict: pass — 0 blockers, 0 majors, 3 minors (ADR-0179 implementation, model: claude-opus-5-5)

Work reviewed: one commit, `7f7301a6` "fix(containerd): unrevisioned workers get CNI attachment IDs unique across
namespaces", `git diff origin/main...HEAD` = `internal/runtime/containerd/containerd_linux.go` (+10/-4) and
`internal/runtime/containerd/cniid_linux_test.go` (new, 174 lines). The tree is clean.

### Verification run (captured)

| Check | Where | Result |
|---|---|---|
| `go build ./...` | darwin, `scripts/agent/d` | exit 0 |
| `GOOS=linux go build ./...` | darwin cross-build | exit 0 |
| `go build -buildvcs=false ./...` | Linux, golang:1.26.4 on colima, CGO on | exit 0 |
| Three `TestScenario_*` tests, `-race -count=1 -v` | Linux container | all PASS (4 subtests of the leftover scenario: reclaim, sweep, revisioned, empty label), exit 0 |
| `go test -race -count=1 ./internal/runtime/containerd/...` | Linux container | `ok` (1.7 s), exit 0 |
| `go vet` | darwin `./internal/runtime/...`; linux cross-vet and in-container `./internal/runtime/containerd/...` | exit 0 (all three) |
| golangci-lint | darwin `./internal/runtime/...`; `GOOS=linux` on `./internal/runtime/containerd/...` (host-built lint binary) | `0 issues.`, exit 0 (both) |
| `gofmt -l internal/runtime/containerd/` | darwin | empty, exit 0 |
| Overlay mutants (`go test -overlay`, Linux) | see below | 4 of 4 killed |

Mutants, each on `containerd_linux.go` and run against the three scenario tests:

| Mutant | Killed by |
|---|---|
| m1: unrevisioned CNI separator `"_"` back to `"-"` (`workerNames`) | `UnrevisionedCNIIDsDistinctAcrossNamespaces` (expected `a-b_c-r0`, got `a-b-c-r0`) and `LeftoverOldFormAttachmentReleased` |
| m2: drop the old-form `d.cni.Remove` in `discard` | `LeftoverOldFormAttachmentReleased/reclaim` and `/sweep` (only `a-b_c-r0` removed) |
| m3: drop the `funcd/revision == ""` guard | `LeftoverOldFormAttachmentReleased/revisioned` (extra DEL of `default-lake-r0`) |
| m4: drop the `rep != ""` guard | `LeftoverOldFormAttachmentReleased/empty_label` (extra DEL of `a-b-c-r`) |

Not run, by instruction: `go test ./...`, the repo-wide `just ci` / `just ci-full` gate (runs once per PR via
`scripts/agent/gate.sh`), and the Lima lanes (the main loop runs every existing lane on this branch before merge).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — Implementation-plan step 3 (duckdb Lima lane cases) is not in the work** · attribution: **adr/env**
  (recorded deferral, not scored). The decider deferred it: `cnitool` cannot ADD under a chosen container ID, the
  lane's daemon config allows only the default namespace, and planting a container needs a per-namespace image
  import, so the lane cases need plumbing the ADR leaves open. The three unit scenario tests carry the proof; a
  follow-up issue tracks the lane cases. This leaves the lane half of Review-checklist item 4 and of "Done when"
  open. A grep of `e2e/`, `scripts/` and the Go tests found no lane or script that asserts a CNI ID or reads
  `/var/lib/cni`, so the existing lanes should not depend on the old ID form.
- **Minor 2 — Implementation-plan steps 4–5 (F12 row status, ADR-0143 back-link) are not in this commit** ·
  attribution: **env** (process, not scored). The ADR is held from publication and is not in the repo yet; the
  docs edits belong to the main loop when it lands the ADR. Review-checklist item 5 stays open until then:
  `docs/feat/0000-feat-v1.md` F12 needs a `cni ids:` entry and `docs/adr/0143-redeploy-by-revision-switch.md`
  needs the `Superseded in part by: ADR-0179` header line.
- **Minor 3 — The commit body says more than the ADR's Publication clause allows for the PR text** ·
  attribution: **model**. The ADR says "the PR and release notes describe the change as a CNI attachment naming
  fix, without further detail". The commit body names the colliding pair (`a-b`/`c` and `a`/`b-c`) and says they
  "no longer share one node-global attachment". A single-commit PR built with `--fill`, or the squash message,
  would carry that text. Fix: the integrator writes the PR title/body (and the squash message) as a CNI attachment
  naming fix only. Impact is low because the diff and the test names show the same facts, and the precedent
  commits for held ADRs (#640, #641) describe behavior at a similar level, so this is not a Major.

### ✅ Verified correct (keep it)

- **Contracts, `workerNames`** (`containerd_linux.go:418-423`): unrevisioned → (`<name>-r<replica>`,
  `<ns>_<name>-r<replica>`); revisioned → (`<revision>.r<replica>`, `<ns>.<revision>.r<replica>`). This matches the
  ADR's Contracts table and code block. The doc comment cites ADR-0143 and ADR-0179 and states why `_` works.
- **Contracts, `discard`** (`containerd_linux.go:585-593`): inside the existing `c.Labels` block, with no second
  `Labels` call; the current-form `Remove` comes first, then the old-form `Remove(ns+"-"+name+"-r"+rep, "")`
  only when `funcd/revision` is empty and namespace, name and replica are all set. Errors are ignored and netns is
  `""`, as the Contracts specify. The one comment says why (pre-ADR-0179 attachments), not what.
- **No other call site builds a CNI ID**: grep finds the CNI ID built only at `Create` (`:257`, via
  `workerNames`) and in `discard`. `Stop` still removes the stored `sb.cniID` (`:486`), `reclaim` and `Sweep` reach
  the change only through `discard`, and no code outside the package builds one.
- **Container IDs, instance IDs, revisioned CNI IDs, labels unchanged**: the diff touches only the unrevisioned
  CNI string and the `discard` block. `TestScenario_OtherWorkerNamesUnchanged` pins container IDs (including a
  pool name), revisioned CNI IDs, and the instance IDs `default/lake/lake-2/r0` and `a-b/c/r0` through a real
  `Create`.
- **Scenarios, one named test each, in the planned file with the planned fakes**:
  `TestScenario_UnrevisionedCNIIDsDistinctAcrossNamespaces` (distinct `workerNames`, two `Create`s with two `Setup`
  IDs, `Stop` removes only its own ID with its netns); `TestScenario_LeftoverOldFormAttachmentReleased` (reclaim
  via `Create` and `Sweep` both release `a-b_c-r0` then `a-b-c-r0`; revisioned → only its ID; missing replica
  label → no old-form DEL); `TestScenario_OtherWorkerNamesUnchanged`. They reuse the package's fake client, image
  and snapshotter (`fakeClient`, `fakeImage`, `memContainers`, `memSnapshotter`). The recording CNI fake is
  mutex-guarded and passes under `-race`. All four mutants on the key lines fail a test.
- **Decision 1 holds beyond its wording**: a namespace never contains `_`, so the namespace always ends at the
  first `_`, and the ID stays unique even for a name that contains `_` (the `__pool__…` case in the test; pools do
  not run on containerd, ADR-0173). libcni accepts the IDs (they start with an alphanumeric character and use only
  `[a-z0-9_.-]`).
- **Conventions**: no new exported name, no `any`, no `panic`, no logging change. YAML is not touched. The
  scenario comment tags follow the package's `// scenario: <id>` style. Citing a held ADR by number in code
  follows existing precedent (ADR-0153 and ADR-0176 are cited in code but not yet in `docs/adr/`).
- **Scope**: two files, as the Implementation plan lists for steps 1–2. The process driver, masquerade rules and
  pool paths are untouched.

### Definition of Done

4 / 6 hold (ADR Review checklist with item 4 split into its test and lane halves; "Done when" folds into them).

| Item | Holds |
|---|---|
| 1. `workerNames` new unrevisioned form; every revisioned name and container ID as in ADR-0143 | ✅ |
| 2. `discard` old-form DEL only with empty revision and all three labels, after the current ID, errors ignored | ✅ |
| 3. No other CNI ID call site; `Stop` uses `sb.cniID`; Create/Stop/reclaim/Sweep otherwise unchanged | ✅ |
| 4a. Each scenario has one named, passing test (under `-race`) | ✅ |
| 4b. duckdb lane plants an old-form leftover and sees it released | ❌ deferred (Minor 1, adr/env) |
| 5. F12 row names this ADR with its status; ADR-0143 carries the back-link | ❌ docs step outside this commit (Minor 2, env) |

`just ci` (the "Done when" gate) was not run here. Its parts are green on the touched scope (build on both GOOS,
vet, lint on both GOOS, gofmt, touched-package tests under `-race`); the repo-wide gate runs once per PR.

### Model scorecard

To record: claude-opus-5-5 on ADR-0179 (implementation) → pass, 0/0/3, 1 model-attributed, DoD 4/6. Not written
to the ledger by this review (the main loop records ledger rows in one PR); the row is below.

### Recommendation

Pass on the code. Before merge, the main loop should (a) write the PR title/body and squash message as a CNI
attachment naming fix only (Minor 3), (b) run the existing Lima lanes on this branch as planned and file the
follow-up issue for the deferred lane cases (Minor 1), and (c) when it lands the ADR, advance the F12 row and add
the ADR-0143 back-link (Minor 2). Do not stamp `Implemented` until (c) is done.

```json
{
  "date": "2026-10-05",
  "adr": "0179",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 1,
  "dod_passed": 4,
  "dod_total": 6,
  "report": "docs/reviews/adr-0179-implementation-claude-opus-5-5.md",
  "notes": "Contracts match (workerNames '_' form, discard old-form DEL with revision/label guards, Stop keeps sb.cniID); 3 scenario tests pass under -race on Linux, package -race green, build darwin+linux, vet, lint darwin+linux 0 issues, 4/4 overlay mutants killed; commit body details the collision beyond the Publication clause (model); duckdb lane cases deferred by the decider, follow-up issue (adr/env); F12 row + ADR-0143 back-link are main-loop docs steps, ADR held (env)"
}
```
