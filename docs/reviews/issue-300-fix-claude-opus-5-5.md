# Issue #300 Fix Review — the control-plane server has no ReadTimeout or IdleTimeout

**Verdict**: **pass**. The regression test fails on the pre-fix code for the issue's reason in both of its cases (a
silent body and an idle keep-alive connection), and it passes with the fix under `-race`. Two targeted mutants each
fail a test. The change removes the cause the issue names, touches nothing else, and conforms to ADR-0028 and ADR-0002.
One Minor finding is recorded: a single unexplained package-test failure that did not reproduce. It is attributed to
the environment and does not count against the model.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #300 · ADR-0028 (control-plane wiring) · the issue #90 data-plane precedent · ADR-0002 ·
`CLAUDE.md` style rules

The fix is one commit, `02bc54d` (`fix(funcd): close silent and idle control-plane connections`), on branch
`fix/i300`. It touches `pkg/funcd/funcd.go` (one line in `buildControlPlane`, plus a two-line comment) and
`pkg/funcd/limits_e2e_test.go` (one new test, behind the `e2e` build tag).

## Verdict: pass — 0 blockers, 0 majors  (issue #300 fix, model: claude-opus-5-5)

### 🟡 Major / Minor

- **Minor (env) — one unexplained package-test failure.** The first run of `go test -race -count=1 ./pkg/funcd/`
  (the non-e2e tests) ended with `FAIL` after 6.8 s, but printed no `--- FAIL` line, no assertion and no panic. Five
  later runs of the same command passed (4.2–5.4 s). The fix changes only the control-plane server's read deadline.
  The non-e2e tests finish in less than the new 10 s bound, so the fix cannot plausibly cause this failure. The host
  is shared with other agents, so the failure is attributed to the environment. The group gate's full run is where
  this failure would show again if it is real.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix.** In the worktree I ran `git revert --no-commit 02bc54d`, restored the
  new test file from `HEAD`, and ran `go test -tags e2e -count=1 -run TestIssue300 ./pkg/funcd/`. Both subtests fail
  after the test's own 20 s client deadline. `silent body` fails because `http.ReadResponse` gets `i/o timeout`: the
  server never answers, because it waits on the missing body. `idle keep-alive` gets its 401 and then fails because
  the next read returns `i/o timeout` instead of `EOF`: the server never closes the idle connection. These are the
  two behaviors the issue reports.
- **It passes with the fix.** After `git reset --hard 02bc54d`,
  `go test -tags e2e -race -count=1 -run 'TestIssue300|TestIssue90' -v ./pkg/funcd/` passes. Both `TestIssue300`
  subtests take 10.00 s, which is the new bound, and the issue #90 data-plane test still passes. Nothing is skipped.
  The worktree was left clean at `02bc54d`.
- **The user-visible behavior is fixed.** The test is the issue's own probe. It sends raw requests over TCP to a real
  platform from `bringUp`, with no credential: one request with `Content-Length: 100` and a 5-byte body, and one
  complete GET followed by silence. It then asserts that the server answers and closes the connection.
- **The cause is fixed, not the symptom.** The issue's root cause is that `ReadTimeout` and `IdleTimeout` were both
  zero. The fix sets `ReadTimeout: 10 * time.Second`, the same value the data plane has used since issue #90. The
  Go 1.26 `net/http` source confirms the three properties the fix relies on:
  - The whole-request read deadline bounds the body read.
  - `Server.idleTimeout()` falls back to `ReadTimeout` when `IdleTimeout` is zero, so idle keep-alive connections are
    closed after 10 s.
  - `connReader.startBackgroundRead` clears the read deadline (`SetReadDeadline(time.Time{})`) after the body has
    been read, so the new bound does not cancel the context of a long-running handler, such as a log follow.

  The control plane has no `MaxBytesReader` and serves small JSON manifests, so a 10 s body read is not a practical
  limit for a real client. Relying on the documented `IdleTimeout` fallback instead of setting `IdleTimeout`
  explicitly is acceptable. The comment states the fallback, so a later reader does not lose the idle bound when
  changing the server.
- **Mutants** (each built with `go test -overlay` on `funcd.go`, and each one killed):
  - `ReadTimeout` kept and `IdleTimeout: time.Hour` added: `idle keep-alive` fails after 20 s. This shows that the
    test exercises the idle bound on its own.
  - `ReadTimeout` removed and `IdleTimeout: 10 * time.Second` set instead: `silent body` fails after 20 s. This shows
    that an idle bound alone does not fix the body case, and that the test catches the difference.
  - The full revert above fails both subtests.
- **Scope.** Both hunks serve the issue. No existing test was changed or removed, and no ADR or living doc was
  touched.
- **Reuse.** The fix adds no helper, type, constant or dependency. It reuses the data plane's literal timeout values,
  and the test reuses the package's `bringUp` harness and sits next to `TestIssue90_SilentBodyReleasesInFlightSlot`,
  which covers the same class of defect.
- **Conventions.** The server literal matches the data-plane line in the same file. The new comment gives the reason
  and the issue numbers, not a description of the code. The test uses top-level imports, the external `funcd_test`
  package, the `TestIssue<N>_…` name, `t.Parallel` subtests, and a cleanup that closes the connection. It contains no
  YAML and no new data directory.
- **ADRs.** ADR-0028 wires the control-plane server onto a listener and does not fix its timeouts. The fix
  contradicts no Accepted or Implemented ADR, and no ADR file was edited.
- **Checks** (all through the cached pinned dev shell, on the touched package only): `go vet ./pkg/funcd/` and
  `go vet -tags e2e ./pkg/funcd/` pass; `golangci-lint run` on `./pkg/funcd/...` reports `0 issues` with and without
  `--build-tags e2e`; `go test -race -count=1 ./pkg/funcd/` passes (see the Minor finding above for the one failed
  run); and the targeted e2e tests pass as above. The Linux lint, the full e2e suite and the repo-wide tests are left
  to the group gate, as this run's scope requires.
- **Shape.** The commit has a `fix(funcd):` subject, `Fixes #300`, the `Co-Authored-By` trailer, and covers one
  issue. Its body names the cause, the fix and the regression test.

## Recommendation

Merge as is with its group. Before the merge, the group gate's full `pkg/funcd` run (non-e2e and e2e, host and Linux)
should confirm that the single unexplained package-test failure does not occur again.
