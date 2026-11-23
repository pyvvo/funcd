## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1531 fix, model: claude-opus-5-5)

This report reviews commit 82b3b0a0 `test(funcdctl): an Addon's registration waits for its revision stamp`, the one
commit of `fix/1531-register-waits-for-the-stamp` on `origin/main` 701f8532 (`git diff origin/main...82b3b0a0`:
`cmd/funcdctl/app_addon_test.go` only). Issue #1531 (`kind/flake`): `TestScenarioAddonTemplateAdminUpdate` failed
once in the merge queue, because the deploy's Addon update answered 409. An independent agent ran this review. It
was briefed with the issue and the commit, not with the author's findings. Every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### Routed (not this work's)

- **Whether the deploy's Addon update re-reads and retries a Conflict** · home: elsewhere · destination: the
  decider. #1531 asks this question, and it is still open. The deploy sends its update on the resourceVersion it
  listed (`cmd/funcdctl/app.go`, `deployAddon`). The real Addon reconciler writes `status.currentRevision` through
  the store (`internal/services/addon/revisions.go`, the `a.Status.CurrentRevision != name` update). Suppose another
  write, such as an admin's `funcdctl apply -f addon.yaml`, creates the Addon or changes its spec, and a deploy
  updates that Addon before the reconciler's stamp lands. Then, by the code (read, not run on a real platform), the
  deploy gets the same 409 and exits 1. ADR-0337 Decision 2 decides that a refused update exits 1 and
  applies no App. No clause decides a retry: ADR-0210 Decision 4 names the four commands that retry, ADR-0217
  Decision 8 adds the deploy's App write, ADR-0337 does not mention a retry, and the `docs/behavior/funcdctl.md` row
  "An admin's `app deploy` of a template with an Addon" says only "a refusal applies no App". The verdict is
  `uncovered`. No board card and no other issue holds the question: an issue search for its terms finds only #1531,
  and none of the board's add-on cards is about it. `Fixes #1531` closes the issue at merge, so the question needs
  another home first: a board card, a `needs-adr` issue, or the decider's answer. Nothing stays here, because the fix
  is correct whatever the answer is.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** The fix changes only a test file, so the
  revert check overlays `cmd/funcdctl/app_addon_test.go` with `register`'s wait removed and keeps the rest of the
  branch's file, the regression test included. In that run, `TestIssue1531_DeployAfterRegisterMeetsNoStamp` fails
  with `controlplane.precondition: Addon login-provider has changed since resourceVersion "…-1"; re-read it and apply
  the change again`. That is the 409 that the issue quotes from its forced-order runs. The merge-queue failure's
  `store.Update: … resourceVersion mismatch` is the same 409, caught one step later, when the stamp lands between
  the handler's read and the store's write (ADR-0210 Decision 2). With the fix, the test passes under `-race` in
  0.71 s.
- **Mutants: 3 of 3 fail the regression test**, each with the same 409. The mutants are: the wait removed (the
  revert), `e.stamped = true` dropped (so `register` never waits), and the wait's condition weakened to "the Addon
  exists".
- **The cause is fixed, not masked.** `register` returned while the stand-in reconciler still had the Addon's stamp
  to write. Now `register` returns only when the store shows a `status.currentRevision` that names the generation it
  wrote. So the test's Given is a settled registered Addon, and no writer is left to race the deploy's update. The fix
  raises no timeout, adds no retry and skips nothing. The deploy's code is unchanged, so a real race still gets the
  decided answer: a refused update exits 1.
- **The waits run on the right goroutines.** `register`'s condition reads the store and returns false on an error. It
  does not call `e.addon`, whose `require.NoError` would call `FailNow` from the goroutine that `require.Eventually`
  starts. The transport's call to `e.addon` runs on the test goroutine: `deployAs` runs the command synchronously,
  and `http.Client` calls `RoundTrip` on the caller's goroutine.
- **The regression test does not rely on luck.** With the fix, the transport never writes, because `register`
  returns only after the stamp. Without the fix, the stand-in's 200 ms delay holds the stamp back. The transport
  writes the stamp before the Addon's PUT while the stamp is still due, so the test fails whenever the deploy lists
  the Addons inside that delay. The reverted test failed in 10 of 10 runs under `-race`, each with the 409. With the
  fix, the regression test and `TestScenarioAddonTemplateAdminUpdate` each passed 20 of 20 runs under `-race`. The
  regression test takes 0.71 to 0.73 s.
- **Scope.** One file, and every hunk serves the issue: the `stamped` flag that tells `register` to wait, the `delay`
  that the regression test needs to hold the stamp back, the `url` for its hooked client, and the test itself. No
  assertion is removed or weakened. Every existing caller of `register` (`TestScenarioAddonTemplateAdminUpdate`,
  `TestScenarioAddonTemplateDeveloperDeploy` and `TestScenarioAddonAppDelete`) gets the settled Given through the
  helper.
- **Reuse.** `cmd/funcdctl` has no function adapter for `http.RoundTripper`: its transports (`putConflicts`,
  `signalTransport`, `firstPutFault`, `deleteStatus`) are single-purpose structs. The only other `roundTripFunc` in
  the repository is in the test files of `internal/activator`, which this package cannot import. The wait follows the
  package's `require.Eventually` and `cliWithin` idiom. The production `waitAddonRevision` makes the same check, but
  it is the code under test, it polls every 200 ms and it has no deadline.
- **Conventions.** The only `time.Sleep` is in the stand-in, where it simulates the reconciler's latency. The inline
  durations are in a test file, which the named-number rule skips (`scripts/behavior/main.go` reads non-test files
  only). The imports are at the top level, and the comments say why (#1531), not what the code does.
- **ADRs and docs.** The commit touches no ADR and no doc. It changes no Accepted or Implemented ADR's decision,
  because the deploy's code is unchanged. The `docs/behavior` row for an admin's deploy of a template with an Addon
  stays true. No design spec models this test helper.
- **No sibling.** Only this helper writes an Addon and then returns before an asynchronous stamp. The package's dev
  tests run the real platform and send unversioned writes (ADR-0210 Decision 4: `applyDesired` sends no version).
  The Addon tests of `pkg/funcd` wait for the current revision before they continue (`waitCurrent` in
  `pkg/funcd/addon_revisions_internal_test.go`). #1401 concerns another object (a Bucket) and stays open on its own.
- **Checks.** `go test -race -count=1 ./cmd/funcdctl` passed (ok, 10.0 s), and so did the same run with `-tags dev`
  (ok, 257.8 s). `go vet` and `golangci-lint run` on `cmd/funcdctl` are clean (0 issues), with and without
  `-tags dev`, on the host and with `GOOS=linux`. `gofmt -l` lists nothing. This review did not run the repo-wide gate
  (`scripts/agent/gate.sh`, then CI). The author runs it once, before the PR.
- **Shape.** The subject's type is `test(funcdctl):`, because the change touches only a test. The repository types
  its flake fixes this way (#1242 and #1250 on main, and the reviews of #1102, #1149 and #1155 to #1159). The commit
  has `Fixes #1531` and the `Co-Authored-By` trailer, and it is one commit for one issue. Its body names the cause,
  the fix and the regression test.

### Definition of Done

12 of the 12 items that apply hold. Item 8 holds for the package's tests, vet and lint, on the host and for Linux;
the e2e suite and the lanes do not cover this test helper, and the repo-wide gate is the author's step. Item 13 does
not apply, because no design spec models this test helper.

### Model scorecard

Recorded: claude-opus-5-5 on #1531 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Ship it with `Fixes #1531` after the author's gate run. Before the PR closes #1531, move the issue's question about a
retry to its own card or issue (Routed).
