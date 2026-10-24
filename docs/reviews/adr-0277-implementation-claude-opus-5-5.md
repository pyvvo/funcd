## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0277 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/adr-0277-containerd-stop-grace` against ADR-0277's Contracts, Scenarios, Review
checklist and Definition of done. The `cowork` session built it; this session reviewed it by running every check below.

### Minor
- **The process driver's grace had no test** (`model`): a `StopGrace()` that returned 0 passed every test of
  `internal/runtime/process` and `pkg/funcd`; in process mode it would cut Run's bound to 2 s, below the 3 s process
  grace. Fixed: `TestStopGraceReported` (`internal/runtime/process`)
  checks that `New`, `Open` with a grace and `Open` with none report it through `runtime.StopGracer`, and the mutant
  now fails it.
- **An overstated budget note** (`model`): `examples/funcdconfig.yaml` said a grace below about 6 s cuts a Lake stop
  "at every stop"; only a stop that waits out a running query is cut before its last save (ADR-0273). Fixed in that wording.

### Verified correct (keep)
- **Decision 1, the key:** `runtime.containerd.stopGrace` (`FUNCD_CONTAINERD_STOP_GRACE`, `internal/platform/config`)
  defaults to `10s`; `containerdStopGrace` (`cmd/funcd/main.go`) parses it with the ADR-0194 grammar, from 1 ms to
  10 s, in every runtime mode, so a bad value stops funcd in process mode too, naming the key.
- **Decision 2, one grace:** the containerd driver's `Stop` and `Close` both wait `d.StopGrace()` (the key, else
  `DefaultStopGrace`), `Close` for every worker at once; `closeGrace` is gone. The boot sweep and reclaim keep their
  own fixed wait after the SIGKILL they send with no SIGTERM first (`killWait`, 10 s).
- **Decision 3, Shutdown's bound:** `runtime.StopGracer` is implemented by both drivers; `Platform.closeTimeout`
  gives `Shutdown` the runtime's grace plus 2 s on both of Run's paths, its stop and its abort, and 3 s + 2 s for a
  runtime that reports none (the old 5 s).
- **Decision 4, the unit:** `funcd install` writes `TimeoutStopSec=45`, with a comment on the 37 s worst case, and
  `scripts/lane-provision.sh` and every `systemd-run` line of the Venom suites match it (the lane-unit guard passes).
  `examples/funcdconfig.yaml` documents the key and moves the budget onto `server.shutdownTimeout`. ADR-0167 and
  ADR-0163 carry their `Superseded in part by ADR-0277` back-links.
- **Scenarios:** `one-grace-for-both-stops` is `TestScenarioOneGraceForBothStops` (a 300 ms grace, a fake task that
  ignores SIGTERM: SIGKILL lands within the grace and one second after SIGTERM, at `Stop` and at `Close`);
  `default-grace` is `TestScenarioDefaultGrace` (the default is 10 s and the driver reports it; a worker that exits on
  SIGTERM ends either stop within the output wait, with no SIGKILL). It does not wait two 10 s graces: with the first
  test, it shows that both stops wait the driver's grace. `bad-grace-refused` is `TestScenarioBadGraceRefused` (`0s`,
  `-1s`, `soon`, `1.5s` and `11s` refused in both modes by `serve`, naming the key); `daemon-stop-keeps-its-flush` is
  `TestScenarioDaemonStopKeepsItsFlush` (a 200 ms grace: `Shutdown` waits it out, its bound is the grace plus 2 s
  within 250 ms, and all 200 held log records reach the bucket before it closes, at a stop and at an abort).
- **Mutant checks:** the builder's three (the bound fixed at 5 s; `Close` and `Stop` on `DefaultStopGrace`) are
  killed, and so are this review's: a 3 s default in `containerdStopGrace` fails `TestScenarioBadGraceRefused`, and
  a bound that ignores the runtime's grace fails both subtests of `TestScenarioDaemonStopKeepsItsFlush`.
- **Tracking:** ADR-0277's substance is unchanged since its acceptance commit; F190 is at `reviewing`.

### Checks
- `go test -race` on `internal/platform/config`, `internal/runtime/...`, the `cmd/funcd` scenario, bounds, install
  and lane-unit tests, and `pkg/funcd`'s flush scenario with `TestIssue453`: OK.
- `just test-linux` with `-run` on `internal/runtime/containerd`, before the rebase: `TestScenarioOneGraceForBothStops`,
  `TestScenarioDefaultGrace` and `TestClose_*`, OK.
- `golangci-lint` on the host (`internal/runtime/...`, `internal/platform/config`, `cmd/funcd`, `pkg/funcd`) and for
  Linux (`internal/runtime/...`), `go vet` for Linux: 0 issues.
- `scripts/agent/wave-check.sh` on the merged wave (main + ADR-0275 + ADR-0276 + ADR-0277 at 26880143): every
  branch merged cleanly; gate PASS (audit, `ci-full` 690 s, Linux build, Linux tests 39 s, stress 328 s,
  tree-unchanged).
- `just lima-example duckdb` at 26880143, the top of the wave: PASS, every case; `issue1232-daemon-stop-keeps-commits`
  takes 13.8 s with the 10 s grace.

### Recommendation
Pass: stamp ADR-0277 `Implemented` and F190 `implemented`.
