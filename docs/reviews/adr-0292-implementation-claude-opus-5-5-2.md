## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0292 implementation, round 2, model: claude-opus-5-5)

This re-review covers cdcba094 on `feat/adr-0291-workload-limits`, three commits on top of 31bd5849, which the first
round reviewed ([report](adr-0292-implementation-claude-opus-5-5.md)). 482519e5 fixes Blocker 1, bfdca8df changes a
test of the quota package for the #950 hygiene test, and cdcba094 folds both Minors. The checks ran in a worktree
detached at cdcba094. `scripts/agent/gate.sh` and the Lima lanes run once on the branch, in the integration session,
and did not run here.

### Checks run
- `go test -race -count=1` on the 30 packages the branch touches, `internal/lake/devengine` with `-tags dev`: ok.
  `TestScenarioResourcesChangeRestartsEngine` passes 3 of 3 with `-race`.
- `go vet` on the touched packages, plain, `-tags dev`, `-tags e2e` and `GOOS=linux`: ok.
- `golangci-lint run` on the touched packages, `--build-tags dev` for `internal/lake/devengine`, and with
  `GOOS=linux` for `internal/runtime/...`: 0 issues.
- `just check-hygiene check-specs`: ok.
- The round-1 survivor, the Site's `QuotaExceeded` branch removed (`internal/site/reconcile.go:384-386`), now fails
  `TestSite_BucketAtQuotaIsNotReadyQuotaExceeded`.

### Round-1 findings
- **Blocker 1, fixed** (482519e5): `TestScenarioResourcesChangeRestartsEngine` now asserts ADR-0292's mechanism. The
  reconciler tears no engine down, Converge gets 512Mi, and the directory and the data stay. Its comment cites
  ADR-0292 and #1195, the same reading as `TestGenerationChangeConvergesTheNewBound` one layer down. The test still
  waits three supervision periods, so a stray teardown would show.
- **Minor 1, fixed** (cdcba094): `TestSite_BucketAtQuotaIsNotReadyQuotaExceeded` builds the Site into a Bucket view
  that asks a Checker refusing every write. The Site is `NotReady`, reason `QuotaExceeded`, with the subject's usage
  in its message. There is no hot retry, and no Route is served.
- **Minor 2, fixed** (cdcba094): `blob.Quoted` treats a nil Checker as `quota.None`, as the KV and SQL facades do.
- **The routed changelog note**: the integration session's `BEGIN_COMMIT_OVERRIDE` carries a `feat(storage)!`
  subject naming the move into `spec.quota`, which is where the first round routed it.

### Also checked
- bfdca8df swaps `require.Never` for `poll.Never` in `TestEmptyWriteAtTheCap`, with the same window and assertion.
  The repo's #950 hygiene test (`TestIssue950_NeverChecksDoNotUseTheTest`) refuses `require.Never`.

### ✅ Verified correct (keep)
Everything the first round listed stands: the decision point and its design model, the sources and enforcement
points, the 507 mapping, the quota block and the move, the engine kept running on a quota change, one test per
scenario, and the lanes' cases.

### Definition of done
9 of 9: the four Review checklist items, plus the done-when line's per-change checks: the tests with `-race`, build,
vet, lint, and `just check-hygiene check-specs`. `scripts/agent/gate.sh` runs once on the branch in the integration
session.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0292 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 9/9. See
docs/reviews/model-scorecard.md.

### Recommendation
Stamp ADR-0292 `Implemented` and F207 `implemented`.
