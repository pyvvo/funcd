# Fix review — issue #489 (Platform.Run returns on an egress or TLS setup error without shutting down)

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #489 fix, model: claude-opus-5-5)

Change: branch `fix/i489`, commit 1c70fb9 `fix(funcd): shut the platform down when Run fails to set up egress
isolation or TLS`. Files: `pkg/funcd/funcd.go` (+19/-7), `pkg/funcd/funcd_test.go` (+61).

The fix removes the cause the issue names: `Run` now runs its loops on its own cancellable context, and every
setup error (egress `Apply`, `edgetls.New`, `prov.Manage`, `prov.TLSConfig`) goes through one `abort` path that
cancels the loops, waits for them (`wg.Wait`), and calls the idempotent `Shutdown` with a `closeTimeout`-bounded
context before it returns the joined error. `p.tlsProvider` is now recorded right after `edgetls.New`, so
`Shutdown` also closes a provider whose `Manage` failed.

### 🟡 Minor 1 — the regression test does not pin the loop drain or the TLS provider close  ·  attribution: model

Evidence: two of three mutants survive.

- M2 — delete `cancelLoops()` and `wg.Wait()` from `abort` (`pkg/funcd/funcd.go:1073-1074`): `ok` with `-race`.
  The deferred `cancelLoops()` still cancels the loops when Run returns, but after `Shutdown`, so Shutdown can
  close the stores under a running controller — the exact ordering the issue describes. No test asserts that
  the loops have stopped before Shutdown.
- M3 — move `p.tlsProvider = prov` back after `prov.TLSConfig()`: `ok`. The `provided`-mode provider's `Close`
  has no observable effect in the test, so the "Shutdown closes a provider whose Manage failed" claim in the
  commit message is unpinned.

The core claim (Shutdown runs before Run returns) is pinned: M1 below fails. A probe loop (for example a
controller or blob watcher that records whether it returned before `Shutdown` began) would close the M2 gap.

### 🟡 Minor 2 — `removeProbe` is a near-copy of an existing package fake  ·  attribution: model

`pkg/funcd/logroutes_internal_test.go:89-94` already defines `shutdownCtxProbe`, a `network.Manager` fake that
records what `Shutdown` hands its `Remove`. The new `removeProbe` (`pkg/funcd/funcd_test.go:169-176`) adds an
`applyErr` and a `removed` flag. Trivial duplication; extending one fake would serve both tests.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 1c70fb9` with the test file kept:
  both subtests fail on `Shutdown ran before Run returned` (egress isolation, tls provisioning).
- **Passes with the fix under `-race`**: `TestIssue489_RunShutsDownOnSetupError` and
  `TestScenarioRunShutdownLifecycle` PASS; the whole `pkg/funcd` package passes with `-race` (11.0 s).
- **Mutant M1** (abort returns the error without `Shutdown`): both subtests fail. Killed.
- **Cause, not symptom**: the early returns now cancel, drain and shut down; no timeout, retry or swallowed error.
  The returned error keeps the original `fault` kind and message (`errors.Join` with Shutdown's error).
- **Normal path unchanged**: `<-ctx.Done()` waits on the derived context, which the caller's cancel still ends;
  the drain, `wg.Wait` and Shutdown sequence after it is untouched.
- **Concurrency**: `abort` before any `wg.Add` (the egress case) is a no-op wait; `Shutdown` is `sync.Once`, so
  the test's cleanup `Shutdown` and a caller's later `Shutdown` stay safe. The test reads `probe.removed` after
  receiving Run's result over a channel, so the race detector is satisfied.
- **Test quality**: it asserts the bus is closed and the control-plane listener refuses connections, not only the
  probe flag; it uses `InMemory()`, so no on-disk data dir and no socket-path limit; `t.TempDir()` holds only a
  non-existent cert path.
- **Scope**: every hunk serves the issue. The issue's alternative (caller calls `Shutdown` in `cmd/funcd`) was
  correctly not taken: Run owns the lifecycle (ADR-0028), and the fix makes that true on the error paths.
- **ADRs**: conforms to ADR-0028 (crash-only lifecycle owned by Run), ADR-0111 (TLS) and ADR-0115 (fail-closed
  egress: the half-applied fence is now removed by `Shutdown`). No ADR file touched.
- **Conventions**: `api/fault` wrapping kept, ctx-first, imports at top level, one short why-comment on `abort`.
- **Checks** (touched package): `go vet ./pkg/funcd/` clean; `golangci-lint run ./pkg/funcd/...` 0 issues.
- **Shape**: `fix(funcd):` subject, `Fixes #489`, attribution trailer, one issue in one commit.

### Definition of Done

10 of 10 applicable items hold. Item 8 is verified for the host-side package checks only (build, vet, lint,
`-race` tests of `pkg/funcd`); the Linux lint, the repo-wide tests and the e2e are left to the group gate.
Mutation (item 4) holds through the revert check and M1; the M2/M3 survivors are recorded as Minor 1.

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 2 minors (both model-attributed: test gap, duplicated fake).

### Recommendation

Pass. Optional follow-up for the fixer: assert in the test that the loops have returned before `Shutdown`
starts (kills M2), and fold `removeProbe` into the existing `shutdownCtxProbe`.
