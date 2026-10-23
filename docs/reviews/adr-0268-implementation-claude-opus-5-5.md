## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0268 implementation, model: claude-opus-5-5)

This report reviews branch `feat/adr-0262-function-volumes` at 40306cac, PR #1192, with ADR-0262 and ADR-0266. ADR-0268
realizes FEAT-0014/F181. It is reviewed against its own text: only the platform sets a volume's project id.

### Minor
- **The F181 row does not link ADR-0268** · attribution: model · `docs/feat/0014-feat-volumes.md:36` lists ADR-0262 and
  ADR-0266 only. The ADR's `Realizes: FEAT-0014/F181` header is right, but the row should link every ADR that realizes
  it (CLAUDE.md chain 1). Fix: add the link when the row next moves.

### ✅ Verified correct (keep it)
- **The filter follows Decision 1.** `withProjectIDFilter` (`internal/runtime/containerd/containerd_linux.go`) is in
  `ociOpts` for every worker, so a worker with no volume gets the same filter. Its default action is allow. The
  architectures are x86_64, x86 and x32 on amd64, and aarch64 and arm on arm64. There are four EPERM rules: `ioctl`
  arg 1 under `SCMP_CMP_MASKED_EQ` with Value `0xFFFFFFFF` (the mask) and ValueTwo the command, for `0x401C5820`,
  `0x40086602` and `0x40046602`, plus `file_setattr`.
- **The kernel workaround is in place (Decision 3).** `quotafs.Open` refuses to turn volumes on for a kernel from 6.17
  (`kernelSupported`, `uname` release).
- **The scenarios pass.** `TestScenarioHighBitsDoNotPass` (`internal/runtime/containerd/seccomp_linux_test.go`) PASS on
  Linux: `scripts/agent/d just test-linux -run HighBitsDoNotPass` exited 0, and
  `internal/runtime/containerd` was ok in 2.49 s. The volumes lane's probe (step V3) inside a real worker reads the
  platform's EPERM for the set calls and `ok` for the read ioctls, which covers `worker-cannot-set-project-id` and
  `other-ioctls-pass`. Step V4 then shows a write past the size still fails. The lane passed all eight steps, as the
  caller reported.
- **ADR-0268's substance is unchanged** apart from its status.

### Definition of Done
8/8 (5 Review-checklist + 3 DoD items) hold.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0268 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 8/8. See
docs/reviews/model-scorecard.md.

### Recommendation
Stamped `Implemented`. Add the ADR-0268 link to the F181 row when ADR-0262 passes and the row moves to `implemented`.
