## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #38 fix, model: claude-opus-5-5, re-review round 2)

Reviewed commits `2c6a9ef` ("fix(function): keep one broken pool member from stopping its siblings' pool") and
`fa1618e` ("fix(function): address review of #38") on branch `fix/205-pooling` at HEAD `fa1618e`. Only #38's two
commits are in scope; the group's other commits were kept in place.

The round-1 report asked for two changes. Both are done. A serving pooled member whose update cannot be materialized,
or whose new digest's platforms cannot be listed, now keeps the revision it serves (`status.servingRevision`) in its
pool, so its calls keep working, as on the solo path. The replica-count move is now covered by a test. The original
fix still holds: a broken member no longer stops its healthy siblings' reconciles, and a dead pool is restarted.

### Round-1 findings

- **Major 1 (a serving member with a broken update lost its traffic and still reported Ready) — resolved.**
  `servingMember` (`internal/function/pool.go`) returns the member at its serving revision while the member serves
  (Ready or Degraded) and that revision is not the current generation's. `poolManifest` falls back to it when a
  sibling's Materialize fails, and `sameKeyFunctions` falls back to it when the platform lookup fails. A member that
  does not serve (Idle, or no serving revision) is still left out, which matches the solo path: with desired = 0,
  `convergeSolo` clears `servingRevision`. Evidence on the real pool (scratch e2e probe through `newPoolHarness`,
  overlaid, not committed):

  ```
  round 1 (2500327): after broken update + a supervision period: a=200 c=404 ... c.phase="Ready"
  round 2 (fa1618e): after broken update + a supervision period: a=200 c=200 ... c.phase="Ready"
  round 2 (fa1618e): after broken update of c + pool SIGKILL: a ok=true c ok=true c.phase="Ready" (new pool pid)
  solo  (fa1618e):   solo after broken update: s=200 ... phase="Ready"
  ```

  The new test `TestIssue38_ServingMemberKeepsItsRevisionOnBrokenUpdate` (two cases: `unmaterializable` and `outage`)
  fails on the round-1 code: `Not equal ... Messages: c keeps the revision it serves in the pool`, because the
  manifest holds only `a`.
- **Minor 1 (the move of the desired-replica count was not covered) — resolved.** The new test
  `TestIssue38_LeftOutMemberKeepsNoPoolUp` kills the mutant that counts `desiredReplicas` before the Materialize
  call (mutant m1 below).

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** `git revert --no-commit fa1618e` applies
  cleanly, but the revert of `2c6a9ef` then conflicts with later group commits in `pool.go` and `pool_test.go`. The
  review therefore used a `go test -overlay` copy of `internal/function/pool.go` at `2500327` with exactly the
  `2c6a9ef` hunks reversed (checked with `diff`: only those hunks differ; the later `pinnedMember` and
  `resolveBindingEnv` changes stay). All five `TestIssue38_…` tests fail, each with a sibling's error returned from a
  healthy member's reconcile: `function.assign: list same-key functions: function.artifactPlatforms: ... fake.Platforms:
  registry unreachable`, and `function.poolManifest: materialize default/c: function.FileMaterializer.Materialize:
  artifact ... not found`.
- **The tests pass with the fix, without skips, under `-race`** (`-count=3`): all five tests and both subtests PASS,
  `ok github.com/pyvvo/funcd/internal/function`.
- **The user-visible behavior is fixed, on the real pool.** The issue's steps (a is Ready, c is added with a missing
  `file://` image, the pool gets SIGKILL) give "a served after pool SIGKILL with broken sibling: true" with a new pool
  PID. The round-1 regression (a Ready member updated to a broken image) now keeps serving, also after a pool SIGKILL.
- **The fix removes the cause, not the symptom.** The two early returns that the issue names (`sameKeyFunctions` and
  `poolManifest`) are gone. The reconciled member's own Materialize error is still returned, and the step-2b gate still
  returns its own platform-lookup error. No timeout, retry or swallowed own-error was added.
- **Mutants** (overlay, package suite): m1 counts `desiredReplicas` before Materialize → `TestIssue38_LeftOutMemberKeepsNoPoolUp`
  fails. m2 disables the `servingMember` fallback in `poolManifest` → `…ServingMember…/unmaterializable` fails. m3
  disables the fallback in `sameKeyFunctions` → `…ServingMember…/outage` fails. m4 drops the serving-phase gate in
  `servingMember` → `TestIssue38_IdleMemberWithBrokenUpdateLeftOut` fails. No mutant survived.
- **Scope:** every hunk serves #38. No test was weakened or deleted. The new test helper `shimHarness.poolManifest`
  reads the manifest that three existing tests read inline; it adds no new mechanism.
- **Reuse:** `servingMember` builds on the existing `revisionTemplate`, `servingPhase` and `revisionName`; the tests
  reuse `newShimHarness`, `withPlatforms`/`fakePlatforms`, `digestHere`/`digestOutage`, `h.apply`, `h.setPhase`,
  `h.rt.exit` and `h.rt.specFor`. `servingMember` and `pinnedMember` share two lines (revision template plus pinned
  digest) but have different error handling, so this is not counted as duplication.
- **Conventions:** imports are at the top level, there is no comment bloat, errors keep their kind through
  `fault.Wrapf`, and logging uses the reconciler's slog logger. The doc comments name ADR-0046, ADR-0143 and ADR-0145.
- **ADRs:** no ADR file was edited. ADR-0143 Decision 8 keeps pooled members out of the revision switch, and its
  checklist asks that "pooled members ... behave as before". Before #38, the running pool kept serving the broken
  member's old artifact, because the sibling's reconcile never rebuilt the pool (round-1 probe: pre-fix c=200). The
  round-2 fix keeps that behavior and adds sibling supervision. No Accepted ADR is contradicted.
- **Checks (HEAD `fa1618e`):** `gofmt -l internal/function` is clean. `go build ./...` passes on the host and with
  `GOOS=linux`. `go vet` passes for `internal/function` and `pkg/funcd`, also with `-tags e2e`. `golangci-lint`
  reports `0 issues.` on the host, on Linux, and with `--build-tags e2e` for `pkg/funcd`. `go test -race ./internal/...`
  is all ok on a rerun (see the note below). `go test -tags e2e ./pkg/funcd/...` is `ok ... 116.304s`.
  `just check-hygiene` reports `hygiene: clean`. The Lima lane was not run here; a later stage owns it.
- **Shape:** `2c6a9ef` has a `fix(function): …` subject, `Fixes #38` and the Co-Authored-By trailer. `fa1618e` has a
  `fix(function): …` subject, `Refs #38` and the trailer. Both commits cover only #38.

### Note (env, not scored)

The first `go test -race ./internal/...` run reported one data race in `internal/blob/s3gateway`
(`TestScenarioOwnerWrites`, between fasthttp's `Server.ShutdownWithContext` and `RequestCtx.Done` during test
cleanup). This branch does not touch that package. A full rerun and three more `-race` runs of the package passed.
The race comes from a third-party shutdown path and is not related to #38.

### Definition of Done

11 / 11 items hold. Item 8 holds for build, vet, lint, the unit tests and the e2e suite; the Lima lane is deferred to
the group's later stage.

### Model scorecard

Not recorded here: the batch's later stage records the ledger row. Fields: claude-opus-5-5 on issue #38 (fix,
round 2) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation

Pass. The fix can go into the group PR. If the group PR keeps one commit per issue, squash `fa1618e` into `2c6a9ef`
and keep `Fixes #38`.
