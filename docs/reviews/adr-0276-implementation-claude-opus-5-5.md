## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0276 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/adr-0276-failing-saves-visible` against ADR-0276's Contracts, Scenarios, Review
checklist and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no review
agents), by running every check below.

### Minor
- **The implement gate's status move came late** (`model`): the implementation commit left ADR-0276 `Accepted` and
  F189 `accepted`; the move to `Reviewing` came in its own commit after the lane, before this review. Fixed; nothing
  else depended on it.

- **`go.mod` was not tidy** (`model`): `report_linux_test.go` imports `containerd/typeurl/v2`, which `go mod tidy`
  then lists as a direct dependency; the gate's `ci` step failed on it, and only on it. Fixed in its own commit (an
  indirect dependency already, so nothing new enters); `go mod tidy` leaves `go.mod` and `go.sum` unchanged after it.

### Verified correct (keep)
- **Decision 1, the report:** only an engine worker gets `FUNCD_REPORTFILE` (`internal/runtime/containerd`
  `containerd_linux.go`: the variable is set when the owner kind is not Function); `readReport` (`helpers_linux.go`)
  opens the file with `O_NOFOLLOW|O_NONBLOCK`, keeps it only when the open file is regular, and reads at most
  `runtime.MaxReportBytes`. `Instance.Report` → `ProviderStatus.Report` (`internal/provider/runtime.go`) is the ready
  replica's report.
- **Decision 2, the condition:** `setCatalogSaved` (`internal/services/lake/reconcile.go`) sets `CatalogSaved` `True`
  `Saved` on `saved …` and `False` `SavesFailing` on `failing since …`, with the report as its message and
  `ObservedGeneration`; any other report keeps the value. It runs only in the Ready branch and changes neither the
  phase, the PEP proxy nor the edge entry; it logs a warning when saves start failing and an info line when they
  recover.
- **Decision 3, the shim:** a recovered catalog is saved once at start and retried each tick until a save lands; a
  fresh one is not saved (no empty catalog over a real one after a misread 404). Every save replaces the report whole
  (temporary file, then `os.replace`), keeping the first failure's time while saves fail. The stop's own failure is
  logged as `funcd duckdb shim: the stop failed: <error>`.
- **Decision 4, engine output:** `pkg/funcd` installs the output capture whatever funclog's setting; an engine's
  stdout and stderr lines go to the daemon log at info with the stream, kind, namespace, name and replica
  (`engineoutput.go`). A Function worker with funclog off is unchanged: both drivers create the worker's
  `workerpipe.Output` with or without the hook, and lines queue only once `Reader` is called, so a hook that returns
  keeps the tail-only behavior and cannot stall the pipe.
- **Scenarios:** `failing-saves-show-on-the-lake`, `saves-recover` and `replaced-engine-keeps-the-failure` are
  `internal/services/lake` `TestScenarioFailingSavesShowOnTheLake`, `TestScenarioSavesRecover` and
  `TestScenarioReplacedEngineKeepsTheFailure` (plus `TestCatalogSavedNeedsAReport`); `unsafe-report-reads-empty` is
  `internal/runtime/containerd` `TestScenarioUnsafeReportReadsEmpty` (a regular, a large, a symlink, a FIFO, a
  directory, no report, a Function worker) and `TestReportFileOnlyForEngines`, run in the Linux VM as a non-root
  user; `engine-output-in-the-daemon-log` is `pkg/funcd` `TestScenarioEngineOutputInTheDaemonLog` (funclog on and
  off); `failed-stop-logged` is `shim_test.py` `TestScenarioFailedStopLogged`; the start-up save is
  `TestScenarioStartUpSave` and `TestScenarioEachSaveIsReported`.
- **Mutant checks:** each fails its tests. The `failing since` prefix not matched: `TestScenarioFailingSavesShowOnTheLake`
  and `TestScenarioReplacedEngineKeepsTheFailure` fail. The engine branch only with funclog on:
  `TestScenarioEngineOutputInTheDaemonLog/funclog_off` fails. No start-up save: two `TestScenarioStartUpSave` tests
  fail. `O_NOFOLLOW` dropped: `TestScenarioUnsafeReportReadsEmpty/a_symlink_is_not_followed` fails in the Linux VM.
- **The real platform:** the duckdb lane's `issue1233-failing-saves-show` case gives Lake `lake`'s `gold` prefix to a
  Function that does not exist. A one-row commit still lands (DuckLake inlines it in the catalog, checked on the
  runtime image's DuckDB 2.0.0); then `CatalogSaved` reads `False SavesFailing`, with the message `failing since
  …: An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.`, while the phase stays
  `Ready` and lake-reader reads `[[1233]]`; the daemon's journal holds the engine's `catalog checkpoint failed`
  line; with the prefix given back, `CatalogSaved` reads `True Saved` 10 s after the failure. The case takes 19 s.
- **Tracking:** ADR-0276's substance is unchanged since its acceptance commit (only the status line moved); F189 is
  at `reviewing`; the board card "A Lake reports catalog saves that keep failing as a status condition (ADR-0274
  follow-up)" is In Progress.

### Checks
- `scripts/agent/gate.sh` at ff42b8c5: audit, Linux build, Linux tests (43 s), stress (38 s) PASS; every `ci-full` test
  passed, and its tidy check failed as above. The branch was then rebased onto ADR-0275, its lane fixture's owner
  written `{kind: Function, name: other}`; the merged wave is gated once more before the queue.
- `just lima-example duckdb` at 09520c92, before the rebase: PASS, every case, `issue1233-failing-saves-show` included.
- `just test-duckdb-engine`: 11 tests, OK, 76.2 s.
- `shim_test.py`: 31 tests, OK.
- `just test-linux`: every package OK, `internal/runtime/containerd` included; the two report tests also run alone
  with `-v`.
- `go test -race` on `internal/provider`, `internal/services/lake` and the `pkg/funcd` scenario; `go vet` and
  `golangci-lint` on `internal/provider`, `internal/runtime`, `internal/runtime/containerd`, `internal/services/lake`
  and `pkg/funcd`, and the Linux lint of the containerd, provider and lake packages: clean.

### Recommendation
Pass: stamp ADR-0276 `Implemented`, F189 `implemented`, and move its board card to Done when the PR merges.
