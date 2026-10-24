## Verdict: pass — 0 blockers, 0 majors, 0 minors  (pyvvo/funcd issue #1257 fix, model: claude-opus-5-5)

Branch `fix/1257-cni-conflist-durable` on `origin/main` 7e37e6f9 (`git diff origin/main...HEAD`), two commits:
52493031 `fix(containerd): the CNI conflist survives a power loss after funcd's first start` and f94d1902
`fix(install): funcd install writes its systemd unit durably`. Issue: "An empty CNI conflist after a power loss stops
funcd from ever starting again" (kind/bug, under #1245). The review ran in the session that made the fix, as the
decider asked on 2026-10-11 (no review agents); every claim below comes from a command that ran.

### Routed (not this work's)
- **A conflist that an older funcd already left empty still stops funcd** · destination: the decider, as a
  question · evidence: ADR-0056 says funcd "never clobbers an existing file", so the fix keeps any file it finds, an
  empty one included; the issue's workaround (delete the file) covers a host that hit #1257 before this fix.
  Rewriting an empty `10-funcd.conflist` would change that rule, so it needs a decision.

### ✅ Verified correct (keep it)
- **The issue reproduces, for its reason.** The issue's failure came from a real power cut in a lane VM: the
  conflist written at funcd's first start was empty after the reboot, and every start failed with `unexpected end of
  JSON input`. `internal/runtime` is the same at that run's commit and at `origin/main`.
- **The regression tests fail without the fix.** `TestIssue1257_ConflistReachesTheDiskBeforeItIsLoaded` traces
  `ensureConflist` through `durable.Disk`. Over the pre-fix `ensureConflist` with only the trace seam added (the
  `durable.Disk` parameter, unused), it fails with nothing synced (`got []`). `TestIssue1257_InstallWritesItsUnitDurably`
  fails the same way over the pre-fix unit write with only the seam (`writeUnit` around the old `os.WriteFile`).
  The test cannot compile against `origin/main`'s files, whose signatures have no seam, hence the seam-only overlays.
- **They pass with the fix**, un-skipped, and both packages' tests pass under `-race`.
- **Mutants.** An overlay that makes the conf dir with `os.MkdirAll`, one that writes the conflist with `os.WriteFile`,
  and one that writes the unit with `os.WriteFile` each fail their test.
- **Cause, not symptom.** Both files were written without a sync and are trusted at every later start (the
  conflist by `New`, which keeps any file it finds; the unit by systemd at each boot). Both now go through
  `durable.Disk`: a temporary file synced, renamed into place, its directory synced; the conf dir `ensureConflist`
  makes has its parent synced. A power loss leaves no file or a whole one.
- **The user-visible behavior.** On a Lima VM with containerd, experiment 0009's s3 power-loss case, run on this branch with no sync of the boot-time files, cut the power three times, the first seconds after funcd's first start: funcd started again after every cut and served. On the issue's run, whose `internal/runtime` is the same as `origin/main`'s, the same first cut left funcd unable to start at every later boot. The harness's own files, copied into the VM with no sync, came back empty after a cut in one run, the failure this fix removes from funcd's files; a rerun that copies them again after each cut read the objects back.
- **ADR-0056 holds.** An existing conflist is still never touched: `TestEnsureConflistWritesIfAbsentLeavesExistingUntouched`
  passes unchanged but for the new parameter.
- **Siblings.** Every non-test `os.WriteFile` under `cmd/`, `internal/` and `pkg/` was read. The systemd unit is the
  one with the same cause, fixed in its own commit with its own test. The rest is rewritten before it is used (the
  worker resolv.conf on every `New`, the node shim and pool host on every start, port files and netns pins per
  worker), regenerated when it does not load (the self-signed TLS pair), synced inside the artifact cache's fill
  (`SyncTree`, #1248), or developer or test output (`funcdctl dev`, `internal/testkit/bench`, specgen).
- **Reuse.** The fix uses `internal/platform/durable`, which #1248 added for this cause; the tests follow the step
  traces of #1248's tests. `writeUnit` is the seam that makes the root-only `runInstall` write testable.
- **Scope.** Five files: the two writers, the one call site in `containerd_linux.go`, and the two test files; the
  existing conflist test changed only its calls.
- **Conventions.** `fix(<scope>):` subjects, `Fixes #1257` on the conflist commit only, the attribution trailer;
  why-comments only; imports at the top.
- **Checks.** Both packages' tests with `-race`, `go vet` and `golangci-lint` on darwin and linux. `scripts/agent/gate.sh` on f94d1902 (rebased on 7e37e6f9): PASS, with the audit (0 hard flags), ci-full, the Linux build, vet and lint, the Linux tests, stress (the three new or changed tests, 20 runs each, 0 failed) and a clean tree. The first ci-full run failed on `TestScenarioHTTPStream`, an e2e timing check unrelated to this change, while a second gate loaded the host; it passed five runs alone, it is #1261, and the rerun of ci-full passed.

### Definition of Done
12 / 12 items hold. Item 13 does not apply: no design spec models the conflist or the unit.

### Model scorecard
claude-opus-5-5 on #1257 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12; the ledger row comes in a later ledger PR.

### Recommendation
Ready for the PR. Whether funcd may replace an empty conflist that an older version left goes to the decider.
