# Issue #43 Fix Review — a pooled Function deployed by tag alone never deploys

**Verdict**: **pass**. The regression test fails on the pre-fix code for the reported reason and passes with the fix
under `-race`. Three mutants of the fix's key lines each fail a test. A real-engine probe that uses the OCI
materializer goes from the issue's `no spec.imageDigest` requeue loop to a Ready pooled Function that answers 200.
The change removes the cause the issue names, stays in scope, and conforms to ADR-0035, ADR-0145 and ADR-0046. There
is one Minor, and it does not block sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #43 · ADR-0035 (the platform pins the digest into the Revision) · ADR-0145 (the platform
gate) · ADR-0046 Decisions 3–6 (membership and manifest) · ADR-0020 (Revision naming) · ADR-0002 · `CLAUDE.md` style rules

The change is one commit, `53a75fa`, on `fix/205-pooling`. Other issues of the same group have their own commits on
that branch, and this review covers only `53a75fa`. The commit touches `internal/function/pool.go` (`pinnedMember`,
called from `sameKeyFunctions`), `internal/function/function.go` (the `revisionName` helper, now shared with
`ensureRevision`) and `internal/function/pool_test.go` (one new test and two small fakes).

## Verdict: pass — 0 blockers, 0 majors  (issue #43 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — a sibling's reconcile drops a re-tagged member from the pool until the member's own reconcile
  runs.** `pinnedMember` returns the stored Function when the Revision for its current generation does not exist yet.
  A tag-only member that the user has just updated to a new generation therefore has an empty digest. If a sibling
  reconciles first, `poolManifest` cannot materialize the updated member, logs `pool member left out`, and restarts the
  pool worker without it. The member's own reconcile stamps the Revision, adds the member back and restarts the pool a
  second time. I confirmed this with an overlay probe in `internal/function`. Two tag-only members were Ready in one
  pool. I re-tagged `x` and reconciled `y` first: the manifest went from `[x, y]` to `[y]`. After `x`'s own reconcile
  it was `[x, y]` again. While the member is left out, it still reports Ready from its earlier pass, so calls to it fail
  until it reconciles. The window is short, because the controller enqueues the member on its own update. Nothing that
  worked before the fix regresses, because a tag-only pooled member never deployed before and a member with an explicit
  digest is unaffected. The commit message describes the fallback as intended. **Fix (optional)**: when the Revision
  for the current generation is not found, fall back to the member's `status.currentRevision` (its last stamped
  Revision) before falling back to the stored spec, so that the member keeps its previous pinned artifact in the
  manifest.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** In a detached review worktree at the branch head, `git revert
  --no-commit 53a75fa` merged cleanly for `pool.go` and `function.go` and conflicted only in `pool_test.go`. I kept the
  head version of the test file, so the test ran against the pre-fix code. `go test -race -run TestIssue43_` fails at
  `pool_test.go:191` (the `hello-pooled` reconcile) with `function.poolManifest: materialize default/hello-pooled:
  test.Materialize: function default/hello-pooled has no spec.imageDigest`. This is the issue's error, produced by a
  fake that rejects an empty digest in the same way as `OrasMaterializer.Materialize`.
- **It passes with the fix.** After `git reset --hard` to the branch head, `go test -race -count=3 -run TestIssue43_`
  passes 3/3. Nothing is skipped and the test does not sleep.
- **The user-visible behavior is fixed.** I wrote an e2e probe in `pkg/funcd` (InMemory, the node shim, `WithPoolShim`
  and `WithArtifactStore`; I did not commit it). It pushes `greeter` to an OCI layout and applies `hello-solo` and
  `hello-pooled` (`pooling.worker: agents`), both with the tag and no digest.
  - With the fix, both Functions are Ready in 0.4 s. `hello-pooled` shows `current=hello-pooled-1 specDigest=""`, and
    `POST /function/hello-pooled` returns `200 {"greeting":"Hello, funcd!"}`.
  - With the pre-fix `pool.go` and `function.go` overlaid, the controller logs `reconcile failed, requeueing …
    function.poolManifest: materialize default/hello-pooled: artifact.OrasMaterializer.Materialize: function
    default/hello-pooled has no spec.imageDigest (the digest is the authority)` once per second until the wait times out.
    This matches the issue's actual behavior.
  - I did not rerun the Workflow image-step case or the real-daemon Python 3.14 case. A Workflow step Function is built
    with only `Image` and `Pooling.Worker` and reaches the same `sameKeyFunctions` → `poolManifest` path, which I
    confirmed by reading the code.
- **The cause is fixed, not the symptom.** Before the fix, the pool used the stored member, whose `spec.imageDigest` is
  empty for a tag-only deploy. `pinnedMember` now takes each member at its current generation's Revision through the
  existing `revisionTemplate`, with that Revision's pinned digest. The solo path does the same in `convergeRevision`. The
  copy is in memory only, so the spec keeps the user's input, and the test asserts this. Both the ADR-0145 platform
  gate (`placeable`) and the materializer now read the pinned digest. Before the fix, the gate read an empty digest and
  did not gate a tag-only member at all. The fix adds no retry, timeout or suppressed error. Self is always pinned:
  `Reconcile` calls `ensureRevision` (line 377) before `assign` (line 413).
- **Mutants**: each mutant was killed by a test.
  - Discarding the pinned digest (`_ = digest` instead of `tmpl.Spec.ImageDigest = digest`) fails
    `TestIssue43_TagOnlyPooledFunctionDeploys`.
  - Removing the NotFound fallback fails `TestIssue68_…`, `TestIssue69_…`, `TestIssue71_…` and `TestIssue72_…`.
  - Moving the `pinnedMember` call after the platform gate fails `TestIssue43_…`: the member built for no node platform
    enters the manifest.
- **Scope**: every hunk serves the issue. `revisionName` is extracted from `ensureRevision` without a change in
  behavior, so the pool and the stamp path cannot disagree on the name. No test was weakened or deleted.
- **Reuse**: the fix reuses `revisionTemplate`, `placeable`, and the existing `fakePlatforms`, `digestHere`/
  `digestElsewhere` and `withPlatforms` test fixtures. The two new fakes are justified. The existing `fakeResolver`
  returns one fixed digest, but the test needs a digest for each ref. The existing `recordingMaterializer` accepts an
  empty digest, but the test needs the OCI materializer's rejection.
- **Conventions**: errors use `api/fault` (`fault.KindOf(err) == fault.NotFound`), ctx comes first, and there is no
  `any`. Imports are at the top level, and the comments are short and name the ADR. The names follow the surrounding
  code.
- **ADRs**: the fix follows ADR-0035 (the digest is resolved once into the immutable Revision and applied to an
  in-memory copy), ADR-0145 (the gate uses the pinned digest) and ADR-0046 (membership and manifest are still computed
  over the same key). The commit edits no ADR file.
- **Checks**: `gofmt -l internal/function` is empty. `go build ./...` and `GOOS=linux go build ./...` pass. `go vet`
  passes on the host and on Linux for `internal/function` and `internal/pooling`. `golangci-lint run ./internal/function/...`
  reports `0 issues.` on the host and on Linux. `go test -race -count=1` passes for `./internal/function/...`,
  `./internal/pooling/...` and `./internal/workflow/...`. `go test -tags e2e -count=1 ./pkg/funcd/...` passes in 111.8 s.
  The batch rules exclude the Lima lane, so I did not run it. The group's full check set runs at a later stage.
- **Shape**: the subject is `fix(function): deploy pooled Functions that name their artifact by tag alone`. The body
  has `Fixes #43`, names the regression test and ends with the attribution trailer. The commit covers one issue.

### Definition of Done
11 / 11 items hold. The e2e suite passed. The Lima lane was not run (the batch rules give it to a later stage), so
item 8 counts the e2e suite only.

### Model scorecard
Not recorded by this review: the batch's later stage records the row. The values are claude-opus-5-5 on issue #43
(fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off. The Minor (falling back to the member's last stamped Revision while its new generation is not yet stamped)
is optional polish for `/fix`, or a follow-up issue. It does not block this fix.
