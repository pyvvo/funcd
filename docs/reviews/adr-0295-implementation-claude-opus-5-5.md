## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0295 implementation, model: claude-opus-5-5)

This review covers ADR-0295's work on `fix/limits-followup` at a7cccf39: 19b19ab4, plus the image code that b7e6798e
moved to `internal/platform/loopfs`. The checks ran in a worktree detached at a7cccf39. `scripts/agent/gate.sh` runs once
on the branch, in the integration session, and did not run here.

### Checks run
- `go build ./...`, on darwin and with `GOOS=linux`: ok.
- `go test -race -count=1 ./internal/volume/quotafs/`: ok, `TestInodesFor` included.
- `just test-volumes-root` in funcd-linux-tests, as root: `TestScenarioInodeLimitApplied`,
  `TestScenarioExistingVolumeLimited` and `TestScenarioEmptyDirLimited` pass, beside the earlier quota tests.
- `go vet` and `golangci-lint run` on the touched packages, on darwin and with `GOOS=linux`: ok, 0 issues.
- The volumes lane's V10 case (`e2e/volumes.venom.yml:269`) passed in the integration session's rerun on cd905b9b. In
  it, a Function fills its volume's inodes and its peer's volume still takes a file.

### Minor
- **No doc says a volume holds at most its share of the image's inodes** (`model`). The ADR's Implementation plan asks
  for it in "the volume reference", and its accepted Negative tells users to pick a larger size when they run out of
  inodes. The repo has no volume reference, and the branch documents the rule nowhere. A user who gets "disk quota
  exceeded" with bytes left has nothing to read. A line in the `volumes:` section of `examples/funcdconfig.yaml`
  (`:257-261`) or in `docs/install.md` would do.

### ✅ Verified correct (keep)
- **The limit** (Decision 1): `inodesFor` computes ⌊size × inodes ÷ bytes⌋ with a 128-bit product
  (`bits.Mul64`/`Div64`), has a floor of 1, and gives a size of the whole image or more every inode. The test cases
  cover a product past 64 bits and an unknown image.
- **Read, never assumed**: `Open` reads `f_files` and `f_blocks × f_bsize` from the mounted image
  (`internal/volume/quotafs/quotafs_linux.go:112`). `Grow` runs only inside `Open`, before the mount, so this one reading
  also covers a grown image.
- **Every call** (Decision 2): `Limit` sets `QIF_BLIMITS|QIF_ILIMITS` with both hard and soft inode limits.
  `TestScenarioExistingVolumeLimited` clears a volume's inode limit and shows the next `Reserve` pass restores it.
- **The image is still preallocated**: `loopfs.Create` formats with `-E nodiscard` (d15b6bd0). `TestLoopfsAsRoot` checks
  that the image's blocks are allocated, and it passes as root.
- **Tracking**: the ADR is at `Reviewing` and its substance is unchanged since acceptance. F211 is `reviewing`, and the
  blueprint names the file cap.

### Definition of done
8 of 8. That is the three Review checklist items, plus the done-when line's per-change checks: the tests with `-race`,
the root tests, build, vet and lint. The volumes lane passed in the integration session, and `scripts/agent/gate.sh`
runs once on the branch.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0295 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 8/8. See
docs/reviews/model-scorecard.md.

### Recommendation
ADR-0295 meets its bar. Stamp it `Implemented` with F211 in the batch's stamp commit. The doc line can ride along with
ADR-0294's fix.
