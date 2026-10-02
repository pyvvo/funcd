## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (issue #38 fix, model: claude-opus-5-5)

Reviewed commit `2c6a9ef` ("fix(function): keep one broken pool member from stopping its siblings' pool") on
branch `fix/205-pooling` at HEAD `2500327`. Only #38's commit is in scope; the group's other commits were kept in place.

The fix removes the cause that the issue names. A sibling's reconcile no longer returns a broken member's
platform-lookup error or Materialize error, so `ensurePool` runs and restarts a dead pool. On a real pool, the
issue's scenario now recovers. However, the fix adds a new silent outage: a pooled member that was serving and then
gets an update that cannot be materialized is now dropped from the pool. Its calls return 404 while its status still
says `Ready`. Before the fix, this member kept serving. A solo Function in the same situation also keeps serving.

### 🟡 Major 1 — a serving pooled member with a broken update loses its traffic and still reports Ready  ·  attribution: model

`internal/function/pool.go` `poolManifest`: when a sibling's Materialize call fails, the member is now dropped
with `continue`. `sameKeyFunctions` does the same when a member's platforms cannot be listed. The member's own
reconcile returns the error and writes no status, so nothing tells the user that the member is no longer in the
pool. The next reconcile of a healthy sibling rebuilds the pool without that member.

Evidence: a scratch e2e probe that runs the real process pool through `newPoolHarness` (overlaid, not committed),
`go test -tags e2e ./pkg/funcd/ -run TestProbe38_`:

```
fixed code:   before update: a=200 c=200
              after broken update + a supervision period: a=200 c=404 body="{\"error\":\"unknown function c\"}" c.phase="Ready"
pre-fix code: after broken update + a supervision period: a=200 c=200 ... c.phase="Ready"
solo (fixed): solo after broken update: s=200 ... phase="Ready"
```

ADR-0143 leaves pooled members out of the revision switch, so no Accepted ADR is contradicted. But the commit
message says the reconciled member's error is returned "as on the solo path", and the solo path keeps the old
revision serving (ADR-0143 Decision 6). The issue asks that "a bad member fails alone, with its own status". The
fix keeps the peers serving. The bad member, however, now fails silently, and it goes from working to broken while
`Ready=True` says otherwise. The same thing happens when a Ready member moves to a digest whose platforms cannot be
listed, because the platform cache only holds digests that were resolved successfully.

Fix (builder): either keep a sibling's last good manifest entry when its new artifact cannot be materialized or its
platforms cannot be listed, or make the dropped member's own reconcile report it as not Ready with a reason, so a
dropped member never shows `Ready` with a dead route. Add a `TestIssue38_…` case for a member that was Ready and
then gets a broken update.

### Minor 1 — the move of the desired-replica count is not covered by any test  ·  attribution: model

The fix moves `desiredReplicas` after the Materialize call, so a member that is left out no longer keeps the pool
up. An overlay mutant that puts the count back before the Materialize call survives the package suite
(`ok github.com/pyvvo/funcd/internal/function`). Fix: add an assertion where only the broken member wants a replica,
and check that the pool is reclaimed.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** `git revert` of `2c6a9ef` conflicts with
  later commits on the branch, so the review used a `go test -overlay` copy of `internal/function/pool.go`. This
  copy has the fix hunks reversed and keeps the later group changes (`pinnedMember`, the `resolveBindingEnv` gate).
  Both tests fail with the sibling's error returned from a healthy member's reconcile:
  `function.assign: list same-key functions: function.artifactPlatforms: ... fake.Platforms: registry unreachable`,
  and `function.poolManifest: materialize default/c: function.FileMaterializer.Materialize: artifact ... not found`.
- **The tests pass with the fix**, without skips, under `-race`: `--- PASS: TestIssue38_PlatformOutageMemberFailsAlone`,
  `--- PASS: TestIssue38_UnmaterializableMemberFailsAlone`.
- **The user-visible behavior is fixed, on the real pool.** The scratch e2e probe ran the issue's steps: `a` is
  Ready, then `c` is added with a missing `file://` image, then the pool process gets SIGKILL. Result: "a served
  after pool SIGKILL with broken sibling: true", with a new pool PID.
- **The fix removes the cause, not the symptom.** The two early returns that the issue names (pool.go,
  `sameKeyFunctions` and `poolManifest`) are gone. The fix adds no timeout, retry or swallowed error for the
  member's own reconcile: `self`'s Materialize error is still returned, and the step-2b gate still returns `self`'s
  own platform-lookup error (function.go).
- **Mutants:** removing the `self` return makes `TestIssue38_UnmaterializableMemberFailsAlone` fail. Putting back
  `return nil, perr` in `sameKeyFunctions` makes `TestIssue38_PlatformOutageMemberFailsAlone` fail.
- **Scope:** every hunk serves #38. No test was weakened or deleted.
- **Reuse:** the tests reuse `newShimHarness`, `withPlatforms`/`fakePlatforms`, `digestHere`/`digestOutage`
  (platform_test.go) and `h.rt.exit`. The logging uses the reconciler's slog logger. Errors are wrapped with
  `fault.Wrapf`, which keeps the kind.
- **Conventions:** imports are at the top level, there is no comment bloat, and the doc comments name ADR-0046 and
  ADR-0145. The `self` comparison by name is correct, because a pool key is scoped to one namespace
  (`pooling.PoolKey`).
- **ADRs:** no ADR file was edited. The exclusion follows the precedent of ADR-0145, where a mismatched member is
  left out and its peers keep serving.
- **Checks (fix tree, HEAD `2500327`):** `gofmt -l` is clean. `go build ./...` passes on the host and with
  `GOOS=linux`. `go vet` passes, also with `-tags e2e` for `pkg/funcd`. `golangci-lint` reports `0 issues.` on the
  host and on Linux. `go test -race ./internal/...` is all ok. `go test -tags e2e ./pkg/funcd/...` is
  `ok ... 111.709s`. `just check-hygiene` reports `hygiene: clean`.
- **Shape:** the subject is `fix(function): …`, the message has `Fixes #38` and the Co-Authored-By trailer, and the
  commit covers one issue.

### Definition of Done

10 / 11 items hold. Miss: item 4 (one of three mutants on the fix's lines survived; Minor 1, model). Major 1 is a
new defect outside the checklist's letter (the fix adds a silent outage for the culprit member), and the verdict
rests on it.

### Model scorecard

Not recorded here: the batch's later stage records the ledger row. Fields: claude-opus-5-5 on issue #38 (fix) →
changes-requested, 0/1/1, 2 model-attributed, DoD 10/11.

### Recommendation

Return to `/fix`. Keep the exclusion of broken siblings, but make sure a dropped member never shows `Ready` with a
dead route: either keep its last good pool entry, or report it as not Ready with a reason. Add a regression case for
a Ready member that gets a broken update, and an assertion for the desired-replica count.
