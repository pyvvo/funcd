# Fix review — issue #464 (The site e2e test can lose its S3 gateway port to another listener)

- **Change**: branch `fix/i464`, commit 32fefcf `fix(test): keep the site e2e S3 gateway port from being lost to another listener`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 blockers, 0 majors, 0 minors
- **Checklist**: 11 of 11 applicable items hold (item 8's Linux lint, the e2e suite and the lanes are left to the group gate)

## Summary

`TestScenarioE2ESite` reserved its S3 gateway port with `freeLoopbackAddr`, which releases the port, and then
started `p.Run` in a goroutine with no readiness wait. The fix moves the platform start into
`startSitePlatform`, which goes through the existing #288 bind-collision retry (`startS3Gateway` with
`platformRun`), exported to the external `funcd_test` package through a new `export_test.go`. Because a failed
attempt's `Platform.Shutdown` closes the injected bus, blob and store (`pkg/funcd/funcd.go`, `closeDriver(p.cfg.bus)`,
`closeDriver(p.cfg.blob)`, `closeDriver(p.cfg.store)`), each attempt builds and seeds its own substrate, and the
store of the successful attempt is the one returned to the scenario. The #288 test's inline "taken port" reserve
is extracted into `takenPortReserve` and shared by both regression tests.

## Blockers

None.

## Majors

None.

## Minors

None.

## Observations (not scored)

- The issue also names `pkg/funcd/funcd.go` (the gateway's bind error is only logged by `Platform.Run`). The fix
  leaves production code unchanged and handles the collision in the test harness, the same choice the #288 fix
  made (`startS3Gateway` doc comment). Whether `Platform.Run` should fail when the S3 gateway cannot bind is a
  behavior decision under ADR-0080/ADR-0085, not part of this test-flake fix.
- `freeLoopbackAddr` still exists in both `pkg/funcd/s3gateway_internal_test.go` and `pkg/funcd/site_e2e_test.go`.
  This duplication predates the fix; the new `export_test.go` could carry it later.

## ✅ Verified correct (keep it)

- **Revert check.** `git revert --no-commit 32fefcf` removes the regression test together with the fix (the whole
  change is test code), so `go test -tags e2e -run TestIssue464 ./pkg/funcd/` reports `[no tests to run]`. The
  pre-fix behavior was therefore reproduced as mutant M1 below, which restores the old one-shot start under the
  new test. The worktree was then reset to 32fefcf and is clean.
- **With the fix, under `-race`** (`go test -race -tags e2e -count=1 -run 'TestIssue464|TestScenarioE2ESite$|TestIssue288|TestScenarioS3Gateway' ./pkg/funcd/`):
  `TestIssue464_SiteS3GatewayStartsWhenItsReservedPortIsTaken` PASS (0.23s), `TestScenarioE2ESite` PASS (6.56s),
  `TestIssue288_S3GatewayStartsWhenItsReservedPortIsTaken` PASS, `TestScenarioS3GatewayDisabledByDefault` PASS,
  `TestScenarioS3GatewayEnabledOpensListener` PASS; `ok github.com/pyvvo/funcd/pkg/funcd 9.812s`.
- **Mutants** (each applied in place, run with `-run`, then restored with `git checkout`):
  - M1 — `StartWithS3Gateway` replaced by the pre-fix path (reserve once, `New`, `go p.Run`, no readiness wait, no
    retry): `TestIssue464` FAILS with "the S3 clients target the port another listener holds" — the issue's
    reason (the clients would dial the port the other listener holds).
  - M2 — `startS3Gateway`'s `const attempts = 5` set to `1`: `TestIssue464` and both `TestIssue288` subtests FAIL
    with `bind: address already in use`.
  - M3 — `StartWithS3Gateway` passes a plain logger instead of the `platformRun` logger: `TestIssue464` FAILS with
    "s3 gateway did not become ready", so the logger wiring that surfaces the gateway's Run error is covered.
- **Cause, not symptom.** The cause the issue names (reserve, release, then bind later, with no retry and no
  readiness wait) is removed for the site scenario: the start now waits on `p.s3gw.Ready()` and rebuilds only on
  `syscall.EADDRINUSE`; any other error fails the test. No timeout was raised and no error is swallowed.
- **Scope.** Three test files change. `TestScenarioE2ESite` keeps every assertion; the only edits in its body are
  `ns` → `siteNS`, the master literal → `siteMaster`, and the start moved into `startSitePlatform`. The S3 data
  dir and artifact store move from `t.TempDir()` to `shortDataDir(t)`, which the #41 socket-path rule requires for
  a platform assembled with `funcd.New`. The #288 test's refactor only extracts `takenPortReserve`; its assertion
  is unchanged.
- **Reuse.** The fix reuses `startS3Gateway`, `platformRun`, `gatewayStopped`, `shortDataDir`, `seed`,
  `pushSiteBundle` and `s3Client`/`s3Status` instead of writing a second retry loop, and it removes the #288 test's
  inline reserve by sharing `takenPortReserve`. `export_test.go` is the standard Go way to hand internal test
  helpers to an external test package.
- **Conventions.** Imports are at the top level, no YAML is touched, comments state the why (#288, #464, why each
  attempt reseeds) without per-line narration, names follow the surrounding test helpers.
- **ADRs.** No ADR file is touched; production behavior under ADR-0080/ADR-0085 is unchanged.
- **Checks on the touched package.** `go build ./pkg/funcd/...` OK; `go vet ./pkg/funcd/` and
  `go vet -tags e2e ./pkg/funcd/` OK; `golangci-lint run ./pkg/funcd/...` and with `--build-tags e2e`: `0 issues.`;
  `gofmt -l pkg/funcd` empty. (The first lint attempt hit the host-wide lock held by another agent's run, the #415
  condition; the rerun used `--allow-parallel-runners`.)
- **Shape.** Subject `fix(test): …`, body explains the cause and names the regression test, `Fixes #464`, the
  attribution trailer, one issue in one commit.

## Definition of Done

11 / 11 applicable items hold. Item 2 is met through mutant M1, because a plain revert removes the test with the
fix. Item 8 covers the host build, vet, lint and the touched tests; the Linux lint, the e2e suite and the lanes
run in the group gate.

## Model scorecard

To record: claude-opus-5-5 on issue #464 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

## Recommendation

Pass. Hand back to `/fix` to open the PR. The production question of whether `Platform.Run` should fail on an S3
gateway bind error can go to `/adr` separately if wanted.
