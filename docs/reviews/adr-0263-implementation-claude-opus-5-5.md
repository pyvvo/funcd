## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0263 implementation, model: claude-opus-5-5)

This report reviews branch `docs/adr-duckdb-2` at 8140c183, rebased on main b183deb1. The work is `git log
origin/main..HEAD`, all committed:

- c58868b5 → 4974dbf8 draft, three judge rounds and acceptance of the ADR (DuckLake 1.1 catalogs, examples move with
  the engine, the back-links on ADR-0086 and ADR-0260);
- 1eeea932 feat(lake): DuckDB 2.0.0 everywhere, new catalogs at DuckLake 1.1, `extension_directories`, the shim's
  allow-list sandbox, the dev engine on port 0 with the whole-line row watcher and `DUCKDB_AGENT_MODE=0`, the
  re-captured 2.0.0 handshake and the 1.1 `LakeFiles` fixture;
- eddd8c0b build(deps): pin funcd-python v0.16.0;
- c9408eb2 test(e2e): the spill step runs the image's shim in the 256Mi engine's container; ADR moved to Reviewing;
- 8140c183 docs(adr): renumber 0262 → 0263 and F181 → F183 (0262 and F181 went to the function-owned volumes ADR,
  merged first in #1189).

ADR-0263 realizes FEAT-0003/F183; the PR that carries this branch closes feature issue #1184 (`Fixes #1184` in its
description, as the ADR's References and Implementation plan say). Two defects found on the way, both pre-existing and
outside this ADR, are filed `needs-adr` (#1185 workers see no cgroup limits; #1186 the shim does not apply ADR-0087's
`DUCKDB_*` settings); neither is made worse by this work — see Verified correct.

### Minor
- **The Implementation plan's `docs/PROJECT-SUMMARY.md` update was not made** · attribution: model · evidence: the
  plan (item 2) lists "the F183 row and `docs/PROJECT-SUMMARY.md`"; the F183 row is present
  (`docs/feat/0003-feat-data-platform.md`), but `git diff origin/main..HEAD -- docs/PROJECT-SUMMARY.md` is empty. The
  summary's ADR index ends at ADR-0261 and its ADR-0086 row still reads "DuckDB 1.5.4 + ducklake v1.0" with no
  supersession note.
  - Non-blocking: `PROJECT-SUMMARY.md` is a derived document with its own reconciler (`project-summary` skill) and is
    not one of the four doc layers; the Reviewing transition does not owe it (CLAUDE.md propagation table), and it is
    absent from the ADR's Review checklist and Definition of done. It lags by two ADRs already (0262 is also absent).
  - Fix, optional: append the ADR-0263 row and note ADR-0086's superseded pins, in this PR or via the
    `project-summary` skill.

### ✅ Verified correct (keep it)
- **DuckDB 2.0.0 everywhere, every input pinned (Decision 1, Contracts; checklist 1).**
  - `images/runtime/duckdb/requirements.txt` is `duckdb==2.0.0` with its two `cp312-abi3` wheel hashes; the first
    duckdb lane run's image build downloaded `duckdb-2.0.0-cp312-abi3-manylinux…aarch64.whl` and printed
    `Successfully installed … duckdb-2.0.0`.
  - `images/runtime/duckdb/duckdb.sha256` holds only `v2.0.0/…` lines — the CLI tarballs for linux/osx × amd64/arm64
    and the four extensions per platform; no `1.5.6` line remains.
  - `scripts/fetch-lake-engine.sh` defaults to `2.0.0`, fetches `duckdb-cli-<cli>.tar.gz` (not the old `.zip`), maps
    darwin to its own-arch CLI (`osx-amd64`/`osx-arm64`, no more `osx-universal`), and checks each file against the
    pins. The real darwin/arm64 2.0.0 bundle in the worktree (`internal/lake/embedengine/engine.tar.gz`, skip-worktree)
    carries `VERSION: duckdb 2.0.0` and four `v2.0.0/osx_arm64` extensions whose SHA-256s each match
    `duckdb.sha256`.
  - A repo-wide grep outside `docs/`, `go.mod` and `go.sum` finds no DuckDB 1.5 version; the only `1.5` hits elsewhere
    are unrelated indirect Go modules.
- **New catalogs are DuckLake 1.1, no migration (Decision 3, Contracts; checklist 2).** Both engines' ATTACH adds
  `DUCKLAKE_VERSION '1.1'` and keeps `OVERRIDE_DATA_PATH TRUE` — `internal/lake/devengine/devengine.go:415` and
  `images/runtime/duckdb/shim.py:271-275`. Nothing passes `AUTOMATIC_MIGRATION`; `TestBuildInitSQLAttachesDuckLake`
  (`devengine_test.go:420`) asserts its absence and that the deprecated `extension_directory=` form is gone. The
  duckdb lane's `new-lake-is-1-1` step read `VERSION=1.1` from the checkpointed catalog (lane-duckdb-2.log). On a real
  DuckLake 1.1 catalog with a nested schema and delete files, `TestLakeFilesDuckLake11Catalog`
  (`internal/backup/workload/workload_test.go:154`, fixture `testdata/ducklake-1.1.sql`) lists every data and delete
  file — passed under `-race`.
- **`extension_directories`, not the deprecated singular (Decision 5; checklist 3).** The list form is set in the dev
  init SQL (`devengine.go:396`), the shim connection (`shim.py:116`), the Dockerfile install step and the examples'
  five sites; `DUCKDB_EXTENSION_DIRECTORY` keeps its name. `requireNoDeprecation` (`devengine_test.go:282`) runs the
  real bundled 2.0.0 CLI on `buildInitSQL`'s script and fails on any `deprecated` line — it passed inside
  `TestConvergeLaunchesEngineWhenBundled` (24.40 s, real bundle). `shim_test.py`'s
  `TestScenarioNoDeprecatedExtensionSetting` asserts `_connect` passes `{"extension_directories": [ext]}`.
- **The dev engine owns its port; the port-hold machinery is gone (Decision 4, Contracts; checklist 4).**
  `holdLoopbackPort`, `listenReusePort` and the `Runtime.holdPort` seam are removed (`git grep` finds them only in the
  one negated test assertion). `buildInitSQL` serves `quack_serve('quack:127.0.0.1:0', …)`; `waitServing` returns the
  address the watcher read from the engine's own `listen_url` row. `rowWatcher.Write` (`devengine.go:363`) buffers to
  whole lines, bounds an unended line at `maxLine`, and matches `serveRow = http://(127\.0\.0\.1:[1-9][0-9]*)`.
  `TestRowWatcherSplitRow` proves a line before the row matches nothing and a port split across writes is read whole;
  `TestScenarioDevEnginePortZero` dials the reported port and confirms a plain bind of it fails;
  `TestScenarioDevEngineEarlyExit` covers both the no-row timeout and the immediate error-output exit. All passed
  under `-race -tags dev`.
- **`DUCKDB_AGENT_MODE=0` (Decision 6; checklist 4).** `launch` sets it on the engine's env (`devengine.go:295`).
  `TestScenarioAgentEnvironmentSameOutput` sets `AI_AGENT=1` and `DUCKDB_AGENT_MODE=1` in the test env, and the fake
  engine fails unless the launched process overrides it to `0` — passed.
- **The PEP proxy handshake is unchanged and re-captured from a 2.0.0 client (Decision 7; checklist 5).**
  `swapHandshakeToken` logic is untouched; `handshake.go` only re-dates its comment to 2.0.0.
  `testdata/handshake_golden.bin` is a fresh frame (56 → 98 bytes); `TestSwapHandshakeToken` round-trips it and
  passed. Readiness (`GET /` → 200) is unchanged.
- **The shim's allow-list sandbox, in Contracts' order (Decision 8, Contracts; checklist 6).** `shim.py` splits the
  old `_configure_and_confine` into `_configure` (S3 pins, `s3_enable_deprecated_url_parameters=false`,
  `disabled_filesystems='HTTPFileSystem'`, `temp_directory`, `allow_persistent_secrets=false`) run before the ATTACH,
  and `_confine` (`allowed_directories=['s3://', '<tmp>/']`, `enable_external_access=false`, `lock_configuration=true`)
  run after it. `shim_test.py`'s `TestScenarioOnlyAllowedPaths` asserts the exact statement order and the created temp
  directory. The duckdb lane's `only-allowed-paths` steps confirmed a `read_text('http://example.invalid/x')` and a
  `read_text('/etc/passwd')` each return 500 with a "disabled by configuration" Permission Error (lane-duckdb-2.log).
  I re-ran the shim's statement sequence against the real bundled 2.0.0 engine on a local temp dir: reads of
  non-allowed local files and arbitrary paths were refused, path traversal out of the temp dir was refused, and after
  the lock no `SET`/`RESET` of `allowed_directories` or `lock_configuration` could re-open it — the allow-list holds
  as written.
- **Scenarios → tests, all un-skipped and passing.** Each of the seven ADR scenarios has a named test or lane check:
  `lake-on-duckdb-2` (duckdb lane deploy+Ready+round-trip), `new-lake-is-1-1`
  (`TestBuildInitSQLAttachesDuckLake` + `TestLakeFilesDuckLake11Catalog` + the lane's `VERSION=1.1` + shim order test +
  app-todo D18), `dev-engine-port-zero` (`TestScenarioDevEnginePortZero` + `TestRowWatcherSplitRow`),
  `dev-engine-early-exit` (`TestScenarioDevEngineEarlyExit`), `agent-environment-same-output`
  (`TestScenarioAgentEnvironmentSameOutput`), `no-deprecated-extension-setting` (`requireNoDeprecation` +
  `TestScenarioNoDeprecatedExtensionSetting`), `only-allowed-paths` (the lane's two refusals + the shim order test).
  No scenario is dropped and no assertion is weakened.
- **#1185 and #1186 are not made worse.** The spill step (`c9408eb2`) does not mask #1185: it runs the image's own
  shim `_connect`/`_configure`/`_confine` in the 256Mi engine's container with an explicit 64MB `memory_limit`,
  records the engine's real settings (`memory_limit` 3.0 GiB, from the VM, lane-duckdb-2.log) and proves DuckDB spills
  to the allowed temp directory (`SPILLED=19999999`). The `only-allowed-paths` and allow-list behaviour is DuckDB's,
  not ADR-0087's `DUCKDB_*` path (#1186), so this ADR neither relies on nor regresses it.
- **Tracking.** Module path `github.com/pyvvo/funcd`; feat row F183 at `reviewing`; ADR at `Reviewing`. The ADR's
  substance is unchanged from acceptance (4974dbf8) to HEAD: the only diff is the status line gaining `Reviewing` and
  the sanctioned renumber (0262 → 0263, F181 → F183, References #1184) forced because ADR-0262/F181 were taken by the
  function-owned-volumes ADR that merged first (#1189) — not a substance edit. ADR-0086 and ADR-0260 carry only their
  `Superseded in part by ADR-0263` back-links. go.mod/go.sum bump only funcd-python 0.15.0 → 0.16.0, whose examples
  carry `duckdb>=2.0.0,<2.1` locked at 2.0.0, the five `extension_directories` sites, and a `PINS` block whose httpfs
  and quack hashes equal funcd's 2.0.0 lines.
- **Checks on 8140c183** (through `scripts/agent/d`, darwin/arm64):
  - `go build ./...` and `go build -tags dev ./...` exit 0.
  - `go vet ./...` and `go vet -tags dev ./...` exit 0.
  - `golangci-lint` reports 0 issues on the touched packages (`internal/lake/gateway`, `internal/backup/workload`,
    `internal/services/lake`, `internal/runtime/embedimg`) and, with `--build-tags dev`, on those plus
    `internal/lake/devengine`, `internal/lake/embedengine` and `cmd/funcdctl`.
  - `go test -race -count=1` passes on `internal/lake/gateway` (1.6 s), `internal/backup/workload` (2.0 s),
    `internal/services/lake` (1.4 s) and `internal/runtime/embedimg` (61 s, which runs `shim_test.py`); with
    `-tags dev`, `internal/lake/devengine` (39 s) and `internal/lake/gateway` pass.
  - `go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` passes.
  - `python3 images/runtime/duckdb/shim_test.py` passes (5 tests).
  - Evidence read, not re-run (each runs once per PR in the gate and in CI): the duckdb lane (final PASS,
    lane-duckdb-2.log; its first run's two failures — the local-file probe hitting the exempt attached catalog, and
    the spill against an engine with no cgroup view, #1185 — were fixed by reading `/etc/passwd` and by running the
    64MB-capped probe in the engine's container), the app-todo lane with D18 (final PASS), `just dev-example`
    lake-quack and releve-lakehouse on the real 2.0.0 bundle, and the `release.yml` no-tag dispatch (all four
    `build-funcdctl` targets built, publish skipped).

### Definition of Done
7 / 7: the 6 Review-checklist items plus the Definition-of-done paragraph. This review ran build, vet, lint and the
touched packages' tests on 8140c183 with and without `-tags dev`, and read the once-per-PR evidence (both lanes, the
real-bundle dev-examples, the release dispatch). `scripts/agent/gate.sh` runs once after this review, and CI again on
the PR. The one Minor is outside the checklist and the DoD, so it does not reduce the count.

Note (env, not scored): `TestPlaceholderReportsNotBundled` (`internal/lake/embedengine`) fails in this worktree
because the real darwin/arm64 2.0.0 bundle is swapped into the skip-worktree `engine.tar.gz`; the committed blob is
the placeholder (`VERSION: placeholder`), so the test passes in CI. This is the same bundle-gating that lets
`TestConvergeLaunchesEngineWhenBundled` and `TestWithLakeDirPersistsLake` run here — a sequencing artifact, not a
model defect.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0263 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 7/7. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass. The single Minor (the un-updated `PROJECT-SUMMARY.md`) does not block the stamp; the builder may append the row
in this PR or leave it to the `project-summary` reconciler. ADR-0263 is stamped `Reviewing → Implemented` (2026-10-23)
and F183 `reviewing → implemented`. The PR closes #1184.
